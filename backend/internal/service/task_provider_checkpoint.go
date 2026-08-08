package service

import (
	"errors"
	"log"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const providerCallCheckpointFailureMessage = "供应商调用前无法确认当前 Worker 的有效任务租约，本次没有发送新的供应商请求；请刷新任务状态，避免手工重复提交"
const providerDispatchCheckpointFailureMessage = "供应商请求发出前无法确认当前 Worker 仍持有任务，本次没有发送新的供应商请求；请刷新任务状态，避免手工重复提交"

// 供应商调用前必须先原子落下有效租约、阶段和审计日志；这条边界不能用内存状态或页面提示代替。
func (s *Service) prepareClaimedTaskProviderCall(task *model.Task, stage string, progress int, level string, message string, payload string) error {
	if task == nil || strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.LeaseOwner) == "" {
		return errors.New(providerCallCheckpointFailureMessage)
	}
	entry := &model.TaskLog{
		ID:      newID(),
		UserID:  task.UserID,
		TaskID:  task.ID,
		Level:   level,
		Message: message,
		Payload: truncateTaskLogPayload(payload),
	}
	if err := s.repo.PrepareClaimedTaskProviderCall(task.ID, task.LeaseOwner, stage, progress, workerTaskLeaseDuration, entry); err != nil {
		// 底层数据库和租约错误只进服务端日志，任务可见错误保持稳定且不暴露部署细节。
		log.Printf("provider call checkpoint failed task=%s stage=%q: %v", task.ID, stage, err)
		return errors.New(providerCallCheckpointFailureMessage)
	}
	task.Stage = stage
	task.Progress = progress
	if task.ProviderCallState == "" || task.ProviderCallState == model.TaskProviderCallPending {
		task.ProviderCallState = model.TaskProviderCallPreflight
	}
	task.LeaseExpiresAt = ptr(time.Now().Add(workerTaskLeaseDuration))
	return nil
}

// HTTP 建连前必须把“可能已经发送”原子写入数据库；若用户取消先抢到终态，本次请求必须停在本地。
func (s *Service) markClaimedTaskProviderCallDispatched(taskID string, leaseOwner string) error {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(leaseOwner) == "" {
		return newProviderRequestNotSentError(providerDispatchCheckpointFailureMessage, errors.New("missing task dispatch ownership"))
	}
	if err := s.repo.MarkClaimedTaskProviderCallDispatched(taskID, leaseOwner); err != nil {
		log.Printf("provider dispatch checkpoint failed task=%s: %v", taskID, err)
		return newProviderRequestNotSentError(providerDispatchCheckpointFailureMessage, err)
	}
	return nil
}

func providerDispatchDefinitelyNotStarted(state model.TaskProviderCallState) bool {
	return state == model.TaskProviderCallPending || state == model.TaskProviderCallPreflight
}

// 非关键任务日志允许降级，但写入失败必须进入服务端日志，不能继续静默丢失诊断事实。
func (s *Service) logTaskEventBestEffort(userID string, taskID string, level string, message string, payload string) {
	if err := s.log(userID, taskID, level, message, payload); err != nil {
		log.Printf("task log write failed task=%s message=%q: %v", taskID, message, err)
	}
}

// 底层错误可能包含数据库、Redis 或上游地址；普通用户只看到核对指引，原始原因仅写 Backend 日志。
func (s *Service) logTaskInternalErrorBestEffort(userID string, taskID string, message string, cause error) {
	if cause == nil {
		return
	}
	log.Printf("task internal operation failed task=%s message=%q: %v", taskID, message, cause)
	s.logTaskEventBestEffort(userID, taskID, "error", message, "内部操作未完成；请联系管理员按任务 ID 核对服务端日志")
}
