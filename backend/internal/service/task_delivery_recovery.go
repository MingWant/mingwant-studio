package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

var errTaskDeliveryRecoveryLeaseLost = errors.New("task delivery recovery lease lost")

type TaskDeliveryRecoveryResult struct {
	Task           *model.Task `json:"task"`
	BillingSettled bool        `json:"billingSettled"`
}

// 供应商已经成功时，即使媒体持久化或角色绑定失败，也要先留下可重放检查点，阻止用户被迫付费重试。
func (s *Service) checkpointProviderResultAfterLocalFailure(task *model.Task, result map[string]interface{}, canvasOps []map[string]interface{}) bool {
	resultJSON, opsJSON, err := marshalTaskCompletion(result, canvasOps)
	if err == nil {
		err = s.checkpointTaskResultWithinStorageQuota(task, resultJSON, opsJSON)
	}
	if err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "供应商成功后的本地失败结果检查点保存失败", err)
		return false
	}
	s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "供应商结果已保存为本地恢复检查点", "恢复操作不会再次调用供应商")
	return true
}

// RecoverTaskDelivery 只重放本系统已经持久化的结果与画布操作，不访问供应商，也不创建新订单。
func (s *Service) RecoverTaskDelivery(userID string, taskID string) (*TaskDeliveryRecoveryResult, error) {
	userID = strings.TrimSpace(userID)
	taskID = strings.TrimSpace(taskID)
	task, err := s.repo.TaskForUser(userID, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == model.TaskStatusSucceeded {
		if strings.TrimSpace(task.ResultJSON) == "" {
			return nil, BadAuthRequest("任务已完成但没有可恢复结果")
		}
		billingSettled := task.BillingOrderID == ""
		if task.BillingOrderID != "" {
			order, err := s.repo.BillingOrder(task.BillingOrderID)
			if err != nil {
				return nil, err
			}
			if order.UserID != task.UserID || order.TaskID != task.ID {
				return nil, BadAuthRequest("任务与计费订单归属不一致")
			}
			billingSettled = order.Status == model.BillingStatusSettled
		}
		return &TaskDeliveryRecoveryResult{Task: taskForOutput(*task), BillingSettled: billingSettled}, nil
	}
	if task.Status != model.TaskStatusFailed && task.Status != model.TaskStatusCancelled {
		return nil, BadAuthRequest("只有交付失败或已取消的任务可以恢复已保存结果")
	}
	if !taskHasDeliveryCheckpoint(*task) {
		return nil, BadAuthRequest("该任务没有完整的本地结果检查点，不能伪装为无需供应商的恢复")
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(task.ResultJSON), &result); err != nil || result == nil {
		return nil, BadAuthRequest("任务结果检查点已损坏，请联系管理员按任务 ID 核对")
	}
	var canvasOps []map[string]interface{}
	if err := json.Unmarshal([]byte(task.DeliveryOpsJSON), &canvasOps); err != nil {
		return nil, BadAuthRequest("任务画布操作检查点已损坏，请联系管理员按任务 ID 核对")
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
			return nil, BadAuthRequest("该任务已经退款，不能把已退款尝试恢复为成功；请联系管理员核对")
		}
	}

	owner := "manual-recovery:delivery:" + newID()
	if err := s.repo.ClaimRecoverableTaskProviderRecovery(task.ID, userID, owner, providerTaskRecoveryLeaseDuration); err != nil {
		if errors.Is(err, repository.ErrTaskProviderRecoveryConflict) {
			return nil, &AuthError{Status: 409, Message: "该任务正在恢复、查询上游或已被重新入队，请稍后刷新"}
		}
		return nil, err
	}
	task.LeaseOwner = owner
	defer func() {
		if err := s.repo.ReleaseTaskProviderRecovery(task.ID, owner); err != nil {
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "本地结果恢复租约释放失败", err)
		}
	}()
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
					return
				}
			case <-leaseDone:
				return
			}
		}
	}()
	defer close(leaseDone)
	ensureLease := func() error {
		select {
		case leaseErr := <-leaseLost:
			return fmt.Errorf("%w: %v", errTaskDeliveryRecoveryLeaseLost, leaseErr)
		default:
			return nil
		}
	}

	result, persistErr := s.persistRecoveringTaskGeneratedMediaResultWithCheck(*task, result, ensureLease)
	if errors.Is(persistErr, errTaskDeliveryRecoveryLeaseLost) {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果恢复租约失效", persistErr)
		return nil, Conflict("本地结果恢复租约已失效，本次未调用供应商；请刷新任务后再恢复")
	}
	resultJSON, opsJSON, checkpointErr := marshalTaskCompletion(result, canvasOps)
	if checkpointErr == nil {
		checkpointErr = ensureLease()
	}
	if checkpointErr == nil {
		checkpointErr = s.checkpointRecoveringTaskResultWithinStorageQuota(task, resultJSON, opsJSON)
	}
	if errors.Is(checkpointErr, errTaskDeliveryRecoveryLeaseLost) {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "恢复后的本地结果检查点写入前租约失效", checkpointErr)
		return nil, Conflict("本地结果恢复租约已失效，本次未调用供应商；请刷新任务后再恢复")
	}
	if persistErr != nil {
		if checkpointErr != nil {
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果恢复失败且部分恢复检查点保存失败", errors.Join(persistErr, checkpointErr))
			return nil, errors.Join(persistErr, checkpointErr)
		}
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果的媒体持久化仍未完成", persistErr)
		s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "已保存结果恢复未完成，未再次调用供应商", "请检查资源存储和账号配额后再次恢复")
		return nil, BadAuthRequest("已保存结果的本地媒体持久化仍未完成，本次未调用供应商；请检查资源存储和账号配额后再次恢复")
	}
	if checkpointErr != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "恢复后的本地结果检查点保存失败", checkpointErr)
		return nil, checkpointErr
	}
	if _, err := s.finalizeCharacterTurnaroundTask(*task, result); err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果的角色资源绑定仍未完成", err)
		return nil, BadAuthRequest("已保存结果的项目资源绑定仍未完成，本次未调用供应商；请联系管理员按任务 ID 核对")
	}
	if err := ensureLease(); err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "本地结果终态提交前恢复租约失效", err)
		return nil, Conflict("本地结果恢复租约已失效，本次未调用供应商；请刷新任务后再恢复")
	}
	task.Error = ""
	if err := s.saveTaskCompletionWithinStorageQuota(task, resultJSON, opsJSON, len(canvasOps) > 0); err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果重新交付失败", err)
		return nil, err
	}
	if _, err := s.RegisterTaskOutputFromTask(*task); err != nil {
		// 成功任务会在项目详情读取时按任务唯一键补登记，不回滚已经完成的本地交付。
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "本地结果恢复成功但项目产物登记失败", err)
	}

	s.hydrateTaskProviderRequestID(task)
	billingSettled := true
	if err := s.SettleBilling(task.BillingOrderID, task.ProviderRequestID); err != nil {
		billingSettled = false
		uncertainErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记本地结果恢复结算失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "已保存结果恢复成功，但积分结算失败；请按任务与订单核对服务端日志"))
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "已保存结果恢复成功但积分结算失败", errors.Join(err, uncertainErr))
	}
	s.logTaskEventBestEffort(task.UserID, task.ID, "info", "已从本地检查点恢复任务交付，未再次调用供应商", "")
	return &TaskDeliveryRecoveryResult{Task: taskForOutput(*task), BillingSettled: billingSettled}, nil
}
