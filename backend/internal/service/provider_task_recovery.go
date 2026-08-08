package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

const providerTaskRecoveryLeaseDuration = 10 * time.Minute

type ProviderTaskQueryResult struct {
	Task           *model.Task `json:"task"`
	ProviderStatus string      `json:"providerStatus"`
	Recovered      bool        `json:"recovered"`
	BillingSettled bool        `json:"billingSettled"`
}

func (s *Service) QueryFailedVideoTask(ctx context.Context, userID string, taskID string) (*ProviderTaskQueryResult, error) {
	task, err := s.repo.TaskForUser(strings.TrimSpace(userID), strings.TrimSpace(taskID))
	if err != nil {
		return nil, err
	}
	return s.queryFailedVideoTask(ctx, task, strings.TrimSpace(userID), nil, "")
}

func (s *Service) AdminQueryFailedVideoTask(ctx context.Context, actor *model.User, logID string) (*ProviderTaskQueryResult, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	log, err := s.repo.APICallLog(strings.TrimSpace(logID))
	if err != nil {
		return nil, err
	}
	if log.Capability != "video" || strings.TrimSpace(log.TaskID) == "" || strings.TrimSpace(log.ProviderRequestID) == "" {
		return nil, BadAuthRequest("该请求日志没有可恢复的视频任务信息")
	}
	task, err := s.repo.Task(log.TaskID)
	if err != nil {
		return nil, err
	}
	if task.UserID != log.UserID {
		return nil, BadAuthRequest("请求日志与任务归属不一致")
	}
	if task.BillingOrderID != log.BillingOrderID {
		return nil, BadAuthRequest("该请求日志不属于任务当前计费尝试，不能用于恢复")
	}
	if task.StartedAt != nil && log.CreatedAt.Before(*task.StartedAt) {
		return nil, BadAuthRequest("该请求日志属于任务的历史重试，不能覆盖当前任务")
	}
	s.hydrateTaskProviderRequestID(task)
	providerRequestID := strings.TrimSpace(log.ProviderRequestID)
	if task.ProviderRequestID != "" && task.ProviderRequestID != providerRequestID {
		return nil, BadAuthRequest("请求日志中的供应商任务 ID 与当前任务不一致")
	}
	task.ProviderRequestID = providerRequestID
	return s.queryFailedVideoTask(ctx, task, "", actor, log.ID)
}

func (s *Service) queryFailedVideoTask(ctx context.Context, task *model.Task, claimUserID string, actor *model.User, apiCallLogID string) (*ProviderTaskQueryResult, error) {
	if task == nil || task.ID == "" {
		return nil, BadAuthRequest("任务不存在")
	}
	if task.Status != model.TaskStatusFailed && task.Status != model.TaskStatusCancelled {
		return nil, BadAuthRequest("只能人工查询状态为失败或已取消的任务")
	}
	if !strings.HasPrefix(task.Type, "canvas_video") && !strings.HasPrefix(task.Type, "video_") {
		return nil, BadAuthRequest("该任务不是视频生成任务")
	}
	if err := providerTaskRecoveryWaitError(task.NextPollAt); err != nil {
		return nil, err
	}

	s.hydrateTaskProviderRequestID(task)
	providerRequestID := strings.TrimSpace(task.ProviderRequestID)
	if providerRequestID == "" {
		return nil, BadAuthRequest("该任务没有可恢复的上游任务 ID")
	}
	if task.BillingOrderID != "" {
		order, err := s.repo.BillingOrder(task.BillingOrderID)
		if err != nil {
			return nil, err
		}
		if order.UserID != task.UserID || order.TaskID != task.ID {
			return nil, BadAuthRequest("任务与计费订单归属不一致")
		}
		if order.Status == model.BillingStatusRefunded {
			return nil, BadAuthRequest("该任务已退款，不能恢复并重新扣费")
		}
	}

	owner := "manual-recovery:" + newID()
	if err := s.repo.ClaimRecoverableTaskProviderRecovery(task.ID, claimUserID, owner, providerTaskRecoveryLeaseDuration); err != nil {
		if errors.Is(err, repository.ErrTaskProviderRecoveryNotDue) {
			latest, latestErr := s.repo.Task(task.ID)
			if latestErr == nil {
				if waitErr := providerTaskRecoveryWaitError(latest.NextPollAt); waitErr != nil {
					return nil, waitErr
				}
			}
			return nil, &AuthError{Status: http.StatusTooManyRequests, Message: "尚未到供应商建议的下次查询时间，请稍后再试"}
		}
		if errors.Is(err, repository.ErrTaskProviderRecoveryConflict) {
			return nil, &AuthError{Status: 409, Message: "该任务正在查询上游状态或已被重新入队，请稍后刷新"}
		}
		return nil, err
	}
	task.LeaseOwner = owner
	defer func() {
		if err := s.repo.ReleaseTaskProviderRecovery(task.ID, owner); err != nil {
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "人工恢复租约释放失败", err)
		}
	}()

	decryptedInput, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return nil, fmt.Errorf("读取任务配置失败：%w", err)
	}
	var input canvasGenerationInput
	if err := json.Unmarshal([]byte(decryptedInput), &input); err != nil {
		return nil, fmt.Errorf("任务输入解析失败：%w", err)
	}
	config, err := s.resolveProviderPollingConfig(ctx, input.Config)
	if err != nil {
		return nil, markProviderPreparationFailure(err)
	}
	if err := validateGenerationInterface("video", config.InterfaceType); err != nil {
		return nil, BadAuthRequest("该任务的供应商协议不支持视频恢复")
	}
	input.Config = config
	task.InputJSON = decryptedInput
	task.ProviderRequestID = providerRequestID
	if err := s.repo.UpdateRecoveringTaskProviderState(task.ID, owner, providerRequestID, task.PollStage, task.NextPollAt); err != nil {
		return nil, err
	}
	if actor != nil {
		// 外部查询无法回滚，因此在真正访问供应商前先提交审计；审计失败时不会发送查询请求。
		if err := s.appendAdminAudit(actor, "task.provider_query", "task", task.ID, "管理员人工查询上游视频任务", map[string]any{"apiCallLogId": apiCallLogID, "billingOrderId": task.BillingOrderID}); err != nil {
			return nil, err
		}
	}
	recoveryCtx, cancelRecovery := context.WithCancel(ctx)
	defer cancelRecovery()
	leaseDone := make(chan struct{})
	leaseLost := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(providerTaskRecoveryLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.repo.RenewTaskProviderRecovery(task.ID, owner, providerTaskRecoveryLeaseDuration); err != nil {
					leaseLost <- err
					cancelRecovery()
					return
				}
			case <-leaseDone:
				return
			}
		}
	}()
	defer close(leaseDone)

	queryCtx := withProviderAnalytics(recoveryCtx, s, *task)
	result, err := runVideoTask(queryCtx, input)
	providerStatus := ""
	var deferredPoll *providerPollDeferredError
	if err != nil && !errors.As(err, &deferredPoll) {
		if deferred := deferTransientProviderPoll(recoveryCtx, task.PollStage, err); deferred != nil {
			err = deferred
		}
	}
	if errors.As(err, &deferredPoll) {
		providerStatus = deferredPoll.ProviderStatus
		result = nil
		err = nil
	}
	if err != nil {
		s.logTaskEventBestEffort(task.UserID, task.ID, "error", "人工查询上游视频任务失败", taskFailureMessage(err))
		return nil, err
	}
	select {
	case leaseErr := <-leaseLost:
		return nil, fmt.Errorf("人工恢复租约已失效：%w", leaseErr)
	default:
	}
	if result == nil {
		if deferredPoll == nil {
			return nil, errors.New("人工查询没有取得上游状态或结果，请检查供应商协议；本次没有创建新任务")
		}
		task.PollStage = deferredPoll.PollStage
		task.NextPollAt = &deferredPoll.NextPollAt
		if err := s.repo.UpdateRecoveringTaskProviderState(task.ID, owner, providerRequestID, task.PollStage, task.NextPollAt); err != nil {
			return nil, err
		}
		level := "info"
		message := "人工查询完成，上游任务仍在处理"
		if strings.HasPrefix(task.PollStage, providerPollRetryStagePrefix) || strings.HasPrefix(task.PollStage, newAPIChannel2RetryStagePrefix) {
			level = "warn"
			message = "人工查询暂时失败，已按退避时间限制下次查询"
		}
		s.logTaskEventBestEffort(task.UserID, task.ID, level, message, providerStatus)
		return &ProviderTaskQueryResult{Task: taskForOutput(*task), ProviderStatus: providerStatus, Recovered: false}, nil
	}

	result, err = s.persistRecoveringTaskGeneratedMediaResultWithCheck(*task, result, func() error {
		select {
		case leaseErr := <-leaseLost:
			return fmt.Errorf("人工恢复租约已失效：%w", leaseErr)
		default:
			return nil
		}
	})
	if err != nil {
		resultJSON, marshalErr := json.Marshal(result)
		checkpointErr := marshalErr
		if checkpointErr == nil {
			checkpointErr = s.checkpointRecoveringTaskResultWithinStorageQuota(task, resultJSON, []byte("null"))
		}
		billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记人工查询部分结果保存失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "人工查询确认上游成功，但本地媒体持久化尚未完成；请恢复已保存结果，不要重新生成"))
		if checkpointErr != nil {
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "人工查询已取得视频，但部分结果检查点保存失败", errors.Join(err, checkpointErr, billingErr))
			return nil, errors.Join(err, checkpointErr, billingErr)
		}
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "人工查询已取得视频，但媒体持久化尚未完成", errors.Join(err, billingErr))
		s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "人工查询已确认上游成功，部分结果已保存", "请使用“恢复已保存结果”继续本地交付，不要重新生成")
		return nil, BadAuthRequest("上游任务已经成功，部分结果已保存在本地；请使用“恢复已保存结果”继续交付，本次没有重新创建供应商任务")
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := s.checkpointRecoveringTaskResultWithinStorageQuota(task, resultJSON, []byte("null")); err != nil {
		billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记人工恢复结果检查点失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "人工查询确认上游成功，但任务结果检查点未保存；请按任务与订单核对服务端日志"))
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "人工查询已取得视频，但结果检查点保存失败", err)
		return nil, errors.Join(err, billingErr)
	}
	task.Error = ""
	task.PollStage = strings.ToLower(providerStatus)
	task.NextPollAt = nil
	if err := s.saveTaskCompletionWithinStorageQuota(task, resultJSON, nil, false); err != nil {
		billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记人工恢复结果保存失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "人工查询确认上游成功，但任务结果未保存；请按任务与订单核对服务端日志"))
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "人工查询已取得视频，但任务恢复失败", err)
		if errors.Is(err, repository.ErrTaskStateConflict) {
			return nil, &AuthError{Status: 409, Message: "任务状态或恢复租约已变化，请刷新后再确认"}
		}
		return nil, errors.Join(err, billingErr)
	}
	if _, err := s.RegisterTaskOutputFromTask(*task); err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务恢复成功但项目产物登记失败", err)
	}
	billingSettled := true
	if err := s.SettleBilling(task.BillingOrderID, providerRequestID); err != nil {
		billingSettled = false
		uncertainErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记人工恢复结算失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "人工查询确认生成成功，但积分结算失败；请按任务与订单核对服务端日志"))
		message := "任务恢复成功但积分结算失败，订单保持待核对"
		if uncertainErr != nil {
			message = "任务恢复成功但积分结算失败，且待核对状态更新失败"
		}
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, message, errors.Join(err, uncertainErr))
	} else {
		s.logTaskEventBestEffort(task.UserID, task.ID, "info", "人工查询确认生成成功，任务已恢复并完成结算", providerStatus)
	}
	return &ProviderTaskQueryResult{Task: taskForOutput(*task), ProviderStatus: providerStatus, Recovered: true, BillingSettled: billingSettled}, nil
}

func providerTaskRecoveryWaitError(nextPollAt *time.Time) error {
	if nextPollAt == nil {
		return nil
	}
	wait := time.Until(*nextPollAt)
	if wait <= 0 {
		return nil
	}
	seconds := int64((wait + time.Second - 1) / time.Second)
	return &AuthError{
		Status:  http.StatusTooManyRequests,
		Message: fmt.Sprintf("上游任务仍在处理中，请在约 %d 秒后再次查询；本次没有访问供应商", seconds),
	}
}
