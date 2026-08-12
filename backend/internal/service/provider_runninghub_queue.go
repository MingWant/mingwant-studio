package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/model"
)

const (
	runningHubWorkflowSlotTTL           = 2 * time.Minute
	runningHubWorkflowSlotRenewInterval = 30 * time.Second
	runningHubWorkflowSlotOperationTime = 5 * time.Second
	runningHubWorkflowCapacityRetry      = 5 * time.Second
	runningHubWorkflowBillingRetry       = 30 * time.Second
	runningHubWorkflowCapacityPollStage  = "rh_capacity_wait"
	runningHubWorkflowBillingPollStage   = "rh_billing_wait"
	runningHubWorkflowGuardPollStage     = "rh_guard_wait"
)

type runningHubWorkflowSlotContextKey struct{}

type runningHubWorkflowSlotSpec struct {
	Scope      string
	ChannelIDs []string
	Limit      int
}

type runningHubWorkflowCapacityWait struct {
	Stage     string
	PollStage string
	Delay     time.Duration
	LogDetail string
}

type runningHubWorkflowSlotGuard struct {
	service *Service
	lease   *runtimeSlotLease
	userID  string
	taskID  string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func runningHubWorkflowSlotHeld(ctx context.Context) bool {
	held, _ := ctx.Value(runningHubWorkflowSlotContextKey{}).(bool)
	return held
}

func withRunningHubWorkflowSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, runningHubWorkflowSlotContextKey{}, true)
}

// prepareClaimedRunningHubWorkflowCapacity 在计费进入 running 和供应商 preflight 之前抢占
// 整个 RHWorkspace 工作流生命周期槽位。抢不到时归还 Worker，任务留在本地等待。
func (s *Service) prepareClaimedRunningHubWorkflowCapacity(ctx context.Context, task *model.Task) (context.Context, *runningHubWorkflowSlotGuard, bool, error) {
	spec, err := s.runningHubWorkflowSlotSpecForTask(task)
	if err != nil {
		return ctx, nil, false, err
	}
	if spec == nil {
		return ctx, nil, false, nil
	}
	guard, wait, err := s.tryAcquireRunningHubWorkflowSlot(ctx, *spec, task.UserID, task.ID)
	if err != nil {
		return ctx, nil, false, err
	}
	if guard != nil {
		return withRunningHubWorkflowSlot(ctx), guard, false, nil
	}
	if wait == nil {
		return ctx, nil, false, errors.New("RunningHub 工作流容量状态无效")
	}
	nextPollAt := time.Now().Add(wait.Delay)
	resetStartedAt := strings.TrimSpace(task.ProviderRequestID) == "" && providerDispatchDefinitelyNotStarted(task.ProviderCallState)
	if err := s.repo.DeferClaimedTaskForProviderCapacity(task.ID, task.LeaseOwner, wait.Stage, wait.PollStage, nextPollAt, resetStartedAt); err != nil {
		return ctx, nil, false, err
	}
	if task.PollStage != wait.PollStage {
		s.logTaskEventBestEffort(task.UserID, task.ID, "info", wait.Stage, wait.LogDetail)
	}
	task.Stage = wait.Stage
	task.Progress = 10
	task.PollStage = wait.PollStage
	task.NextPollAt = &nextPollAt
	task.LeaseOwner = ""
	task.LeaseExpiresAt = nil
	if resetStartedAt {
		task.StartedAt = nil
	}
	return ctx, nil, true, nil
}

// 已有 taskId 的恢复任务遇到协调器/数据库短暂故障时只能延后原任务，
// 不能按“调用前失败”退款，更不能绕过门禁重新创建工作流。
func (s *Service) deferClaimedRunningHubWorkflowGuardRecovery(task *model.Task) error {
	if task == nil {
		return errors.New("RunningHub 恢复任务为空")
	}
	stage := "等待 RunningHub 排队保护恢复"
	nextPollAt := time.Now().Add(runningHubWorkflowBillingRetry)
	if err := s.repo.DeferClaimedTaskForProviderCapacity(task.ID, task.LeaseOwner, stage, runningHubWorkflowGuardPollStage, nextPollAt, false); err != nil {
		return err
	}
	if task.PollStage != runningHubWorkflowGuardPollStage {
		s.logTaskEventBestEffort(task.UserID, task.ID, "warn", stage, "系统不会重建已有供应商任务；排队保护恢复后只继续查询原 taskId")
	}
	task.Stage = stage
	task.Progress = 10
	task.PollStage = runningHubWorkflowGuardPollStage
	task.NextPollAt = &nextPollAt
	task.LeaseOwner = ""
	task.LeaseExpiresAt = nil
	return nil
}

func (s *Service) runningHubWorkflowSlotSpecForTask(task *model.Task) (*runningHubWorkflowSlotSpec, error) {
	if task == nil || (!strings.HasPrefix(task.Type, "canvas_video") && !strings.HasPrefix(task.Type, "video_")) {
		return nil, nil
	}
	decryptedInput, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return nil, fmt.Errorf("读取任务中的 RunningHub 排队配置失败：%w", err)
	}
	var input struct {
		Config providerConfig `json:"config"`
	}
	if err := json.Unmarshal([]byte(decryptedInput), &input); err != nil {
		return nil, fmt.Errorf("解析任务中的 RunningHub 排队配置失败：%w", err)
	}
	interfaceType := model.ChannelInterfaceType(strings.TrimSpace(input.Config.InterfaceType))
	channelID := strings.TrimSpace(input.Config.ChannelID)
	if interfaceType != "" && interfaceType != model.ChannelInterfaceRunningHub {
		return nil, nil
	}
	if interfaceType == "" {
		if channelID == "" {
			return nil, nil
		}
		channel, channelErr := s.repo.SystemChannel(channelID)
		if channelErr != nil {
			return nil, nil
		}
		interfaceType = channel.InterfaceType
		if modelKey := strings.TrimPrefix(strings.TrimSpace(input.Config.Model), "models/"); modelKey != "" {
			if item, modelErr := s.repo.ChannelModelByKeyIncludingDisabled(channelID, modelKey); modelErr == nil && item.Protocol != "" {
				interfaceType = item.Protocol
			}
		}
		if interfaceType != model.ChannelInterfaceRunningHub {
			return nil, nil
		}
	}
	if channelID == "" {
		return nil, errors.New("RunningHub 工作流任务缺少系统渠道 ID")
	}
	return s.runningHubWorkflowSlotSpecForChannel(channelID)
}

func (s *Service) runningHubWorkflowSlotSpecForChannel(channelID string) (*runningHubWorkflowSlotSpec, error) {
	channelID = strings.TrimSpace(channelID)
	channel, err := s.repo.SystemChannel(channelID)
	if err != nil {
		return nil, fmt.Errorf("读取 RunningHub 系统渠道失败：%w", err)
	}
	apiKey := strings.TrimSpace(channel.APIKey)
	if apiKey == "" {
		return nil, errors.New("RunningHub 系统渠道未配置 API Key")
	}
	limit := runningHubWorkflowConcurrencyLimit(channel.ConcurrencyLimit)
	channelIDs := []string{channel.ID}
	channels, err := s.repo.SystemChannels(true)
	if err != nil {
		return nil, fmt.Errorf("读取 RunningHub API Key 关联渠道失败：%w", err)
	}
	seen := map[string]struct{}{channel.ID: struct{}{}}
	for index := range channels {
		candidate := channels[index]
		if strings.TrimSpace(candidate.APIKey) != apiKey {
			continue
		}
		if _, exists := seen[candidate.ID]; !exists {
			seen[candidate.ID] = struct{}{}
			channelIDs = append(channelIDs, candidate.ID)
		}
		if candidate.Enabled && (candidate.ID == channel.ID || candidate.InterfaceType == model.ChannelInterfaceRunningHub) {
			candidateLimit := runningHubWorkflowConcurrencyLimit(candidate.ConcurrencyLimit)
			if candidateLimit < limit {
				limit = candidateLimit
			}
		}
	}
	digest := sha256.Sum256([]byte(apiKey))
	return &runningHubWorkflowSlotSpec{
		Scope:      fmt.Sprintf("runninghub-workflow-key:%x", digest),
		ChannelIDs: channelIDs,
		Limit:      limit,
	}, nil
}

// 跟随全局并发适合短 HTTP 请求，但消费级 RunningHub Key 的活动工作流上限是 1。
// 只有管理员给渠道显式填写并发数时，才把更高额度用于完整工作流生命周期。
func runningHubWorkflowConcurrencyLimit(configured int) int {
	if configured < minChannelConcurrencyLimit || configured > maxChannelConcurrencyLimit {
		return 1
	}
	return configured
}

func (s *Service) tryAcquireRunningHubWorkflowSlot(ctx context.Context, spec runningHubWorkflowSlotSpec, userID string, taskID string) (*runningHubWorkflowSlotGuard, *runningHubWorkflowCapacityWait, error) {
	wait, err := s.runningHubWorkflowCapacityBlocker(spec, taskID)
	if err != nil || wait != nil {
		return nil, wait, err
	}
	if s.coordinator == nil {
		return nil, nil, errors.New("RunningHub 工作流协调器未初始化")
	}
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, runningHubWorkflowSlotOperationTime)
	lease, acquired, err := s.coordinator.acquireLease(acquireCtx, spec.Scope, spec.Limit, runningHubWorkflowSlotTTL)
	cancelAcquire()
	if err != nil {
		return nil, nil, err
	}
	if !acquired {
		wait := runningHubWorkflowCapacityWaitDefault()
		return nil, &wait, nil
	}
	// 槽位和数据库状态分别防运行期竞态与进程崩溃；拿到槽位后必须再核对一次数据库真相。
	wait, err = s.runningHubWorkflowCapacityBlocker(spec, taskID)
	if err != nil || wait != nil {
		lease.Release()
		return nil, wait, err
	}
	guard := &runningHubWorkflowSlotGuard{
		service: s,
		lease:   lease,
		userID:  strings.TrimSpace(userID),
		taskID:  strings.TrimSpace(taskID),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go guard.renew()
	return guard, nil, nil
}

func (s *Service) runningHubWorkflowCapacityBlocker(spec runningHubWorkflowSlotSpec, taskID string) (*runningHubWorkflowCapacityWait, error) {
	activeCount, hasUncertain, err := s.repo.ChannelProviderExecutionState(spec.ChannelIDs, taskID)
	if err != nil {
		return nil, err
	}
	if hasUncertain {
		return &runningHubWorkflowCapacityWait{
			Stage:     "等待 RunningHub 费用核对后放行",
			PollStage: runningHubWorkflowBillingPollStage,
			Delay:     runningHubWorkflowBillingRetry,
			LogDetail: "同一 API Key 存在费用或执行状态不确定的任务；在管理员核对前不会创建新的工作流",
		}, nil
	}
	if activeCount >= int64(spec.Limit) {
		wait := runningHubWorkflowCapacityWaitDefault()
		return &wait, nil
	}
	return nil, nil
}

func runningHubWorkflowCapacityWaitDefault() runningHubWorkflowCapacityWait {
	return runningHubWorkflowCapacityWait{
		Stage:     "等待 RunningHub 上个任务结束",
		PollStage: runningHubWorkflowCapacityPollStage,
		Delay:     runningHubWorkflowCapacityRetry,
		LogDetail: "任务保留在本地队列，当前没有创建新的 RunningHub 工作流",
	}
}

func (guard *runningHubWorkflowSlotGuard) renew() {
	defer close(guard.done)
	ticker := time.NewTicker(runningHubWorkflowSlotRenewInterval)
	defer ticker.Stop()
	hadTransientFailure := false
	issueKey := "runninghub:" + guard.taskID
	for {
		select {
		case <-ticker.C:
			renewCtx, cancelRenew := context.WithTimeout(context.Background(), runningHubWorkflowSlotOperationTime)
			err := guard.lease.Renew(renewCtx, runningHubWorkflowSlotTTL)
			cancelRenew()
			if errors.Is(err, errRuntimeSlotLeaseLost) {
				log.Printf("runninghub workflow slot lease lost task=%s: %v", guard.taskID, err)
				guard.service.recordWorkerSlotIssue(issueKey)
				guard.service.logTaskEventBestEffort(guard.userID, guard.taskID, "error", "RunningHub 工作流并发租约已失效", "当前上游任务不会被取消；数据库活动任务门禁继续阻止新建工作流，请管理员检查 Redis 与 Worker 日志")
				return
			}
			if err != nil {
				if !hadTransientFailure {
					log.Printf("runninghub workflow slot renewal failed task=%s: %v", guard.taskID, err)
					guard.service.recordWorkerSlotIssue(issueKey)
					guard.service.logTaskEventBestEffort(guard.userID, guard.taskID, "warn", "RunningHub 工作流并发租约续期暂时失败", "当前上游任务不会因此被取消或重新创建")
				}
				hadTransientFailure = true
				continue
			}
			if hadTransientFailure {
				log.Printf("runninghub workflow slot renewal recovered task=%s", guard.taskID)
				guard.service.clearWorkerSlotIssue(issueKey)
				guard.service.logTaskEventBestEffort(guard.userID, guard.taskID, "info", "RunningHub 工作流并发租约续期已恢复", "")
				hadTransientFailure = false
			}
		case <-guard.stop:
			return
		}
	}
}

func (guard *runningHubWorkflowSlotGuard) Release() {
	if guard == nil {
		return
	}
	guard.once.Do(func() {
		close(guard.stop)
		<-guard.done
		guard.service.clearWorkerSlotIssue("runninghub:" + guard.taskID)
		guard.lease.Release()
	})
}

// 人工恢复也必须占用同一生命周期槽位；普通 Worker 已在计费前持有时用 context 标记避免重复抢占。
func ensureRunningHubWorkflowSlot(ctx context.Context, config providerConfig) (context.Context, *runningHubWorkflowSlotGuard, error) {
	if runningHubWorkflowSlotHeld(ctx) {
		return ctx, nil, nil
	}
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil {
		return ctx, nil, nil
	}
	spec, err := metadata.Service.runningHubWorkflowSlotSpecForChannel(config.ChannelID)
	if err != nil {
		return ctx, nil, markProviderPreparationFailure(err)
	}
	guard, wait, err := metadata.Service.tryAcquireRunningHubWorkflowSlot(ctx, *spec, metadata.UserID, metadata.TaskID)
	if err != nil {
		return ctx, nil, markProviderPreparationFailure(err)
	}
	if guard == nil {
		message := "RunningHub 同一 API Key 的上个工作流尚未结束，本次没有发出新的供应商请求；请等待原任务结束后再查询"
		if wait != nil && wait.PollStage == runningHubWorkflowBillingPollStage {
			message = "RunningHub 同一 API Key 存在费用待核对任务，本次没有发出新的供应商请求；请先完成费用核对"
		}
		return ctx, nil, newProviderRequestNotSentError(message, errors.New("runninghub workflow capacity unavailable"))
	}
	return withRunningHubWorkflowSlot(ctx), guard, nil
}
