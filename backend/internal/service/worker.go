package service

import (
	"context"
	"errors"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const (
	taskWorkerConcurrency       = 3
	workerTaskLeaseDuration     = 45 * time.Second
	workerSlotLeaseDuration     = 2 * time.Minute
	workerSlotRenewInterval     = 30 * time.Second
	workerSlotOperationTimeout  = 5 * time.Second
	workerDispatcherInterval    = 2 * time.Second
	workerInterruptedTaskStage  = "任务中断，等待费用核对"
	workerRecoveredTaskLogTitle = "中断任务未自动重放"
)

func (s *Service) StartWorker() {
	s.workerLifecycleMu.Lock()
	s.ensureWorkerLifecycleLocked()
	if s.workerStarted || s.workerStopping {
		s.workerLifecycleMu.Unlock()
		return
	}
	s.workerStarted = true
	s.workerLifecycleMu.Unlock()
	s.recordWorkerHeartbeat(nil)
	go func() {
		s.runWorkerDispatcher()
		// dispatcher 退出后不会再增加 WaitGroup，等待才不会与 Add 竞争。
		s.workerTasks.Wait()
		close(s.workerDone)
	}()
}

func (s *Service) runWorkerDispatcher() {
	for s.runWorkerDispatcherCycle() {
		timer := time.NewTimer(workerDispatcherInterval)
		select {
		case <-timer.C:
		case <-s.workerStop:
			timer.Stop()
			return
		}
	}
}

func (s *Service) runWorkerDispatcherCycle() (restart bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("worker dispatcher panic panic_type=%T\n%s", recovered, debug.Stack())
			if !s.workerShutdownRequested() {
				s.recordWorkerHeartbeat(errors.New("Worker 调度器发生异常，正在自动恢复"))
				restart = true
			}
		}
	}()
	slots := make(chan struct{}, maxChannelConcurrencyLimit)
	dispatch := func() error {
		if s.workerShutdownRequested() {
			return nil
		}
		if s.hasWorkerSlotIssue() {
			return errors.New("全局 Worker 并发槽位续租异常，等待恢复或受影响任务结束")
		}
		setting, err := s.runtimeConcurrencySetting()
		if err != nil {
			return err
		}
		workerConcurrency := setting.WorkerConcurrency
		for len(slots) < workerConcurrency {
			if s.workerShutdownRequested() {
				return nil
			}
			acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), workerSlotOperationTimeout)
			lease, acquired, err := s.coordinator.acquireLease(acquireCtx, "workers", workerConcurrency, workerSlotLeaseDuration)
			cancelAcquire()
			if err != nil {
				return err
			}
			if !acquired {
				return nil
			}
			if s.workerShutdownRequested() {
				lease.Release()
				return nil
			}
			task, err := s.repo.ClaimNextTask(s.workerID, workerTaskLeaseDuration)
			if err != nil {
				lease.Release()
				return err
			}
			if task == nil {
				lease.Release()
				return nil
			}
			slots <- struct{}{}
			s.workerTasks.Add(1)
			go func() {
				defer s.workerTasks.Done()
				s.runClaimedTask(task, lease, slots)
			}()
		}
		return nil
	}

	s.recordWorkerHeartbeat(dispatch())
	ticker := time.NewTicker(workerDispatcherInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.recordWorkerHeartbeat(dispatch())
		case <-s.workerStop:
			return false
		}
	}
}

// BeginWorkerShutdown 先停止领取新任务；已经进入供应商边界的任务继续执行，避免部署重启制造费用不确定状态。
func (s *Service) BeginWorkerShutdown() {
	s.workerLifecycleMu.Lock()
	s.ensureWorkerLifecycleLocked()
	if s.workerStopping {
		s.workerLifecycleMu.Unlock()
		return
	}
	s.workerStopping = true
	started := s.workerStarted
	close(s.workerStop)
	if !started {
		close(s.workerDone)
	}
	s.workerLifecycleMu.Unlock()
	s.recordWorkerHeartbeat(errors.New("Backend 正在优雅停机，Worker 已停止领取新任务"))
	s.runtimeHealthMu.Lock()
	s.runtimeHealthAt = time.Time{}
	s.runtimeHealthMu.Unlock()
}

func (s *Service) ShutdownWorker(ctx context.Context) error {
	s.BeginWorkerShutdown()
	if ctx == nil {
		ctx = context.Background()
	}
	s.workerLifecycleMu.Lock()
	done := s.workerDone
	s.workerLifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) workerShutdownRequested() bool {
	s.workerLifecycleMu.Lock()
	stopping := s.workerStopping
	s.workerLifecycleMu.Unlock()
	return stopping
}

func (s *Service) ensureWorkerLifecycleLocked() {
	if s.workerStop == nil {
		s.workerStop = make(chan struct{})
	}
	if s.workerDone == nil {
		s.workerDone = make(chan struct{})
	}
}

func (s *Service) runClaimedTask(task *model.Task, lease *runtimeSlotLease, slots chan struct{}) {
	claimOwner := task.LeaseOwner
	leaseStop := make(chan struct{})
	leaseDone := make(chan struct{})
	go s.renewWorkerSlotLease(task, lease, leaseStop, leaseDone)
	defer func() {
		close(leaseStop)
		<-leaseDone
		s.clearWorkerSlotIssue(task.ID)
		<-slots
		lease.Release()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			// 二次保护避免诊断写入本身异常时再次击穿整个 Backend 进程。
			func() {
				defer func() {
					if nested := recover(); nested != nil {
						log.Printf("worker panic recovery failed task=%s panic_type=%T", task.ID, nested)
					}
				}()
				s.recoverWorkerTaskPanic(task, claimOwner, recovered)
			}()
		}
	}()
	if err := s.processClaimedTask(task); err != nil {
		log.Printf("worker task processing returned error task=%s: %v", task.ID, err)
	}
}

// Worker 槽位只限制全局并发，不代表供应商请求所有权；续租失败不能粗暴取消当前请求，否则仍可能计费且更难恢复。
func (s *Service) renewWorkerSlotLease(task *model.Task, lease *runtimeSlotLease, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(workerSlotRenewInterval)
	defer ticker.Stop()
	hadTransientFailure := false
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), workerSlotOperationTimeout)
			err := lease.Renew(ctx, workerSlotLeaseDuration)
			cancel()
			if errors.Is(err, errRuntimeSlotLeaseLost) {
				log.Printf("worker slot lease lost task=%s: %v", task.ID, err)
				s.recordWorkerSlotIssue(task.ID)
				s.logWorkerLeaseEvent(task, "error", "全局 Worker 并发槽位租约已失效，请管理员检查 Redis 与 Worker 日志")
				return
			}
			if err != nil {
				if !hadTransientFailure {
					log.Printf("worker slot renewal failed task=%s: %v", task.ID, err)
					s.recordWorkerSlotIssue(task.ID)
					s.logWorkerLeaseEvent(task, "warn", "全局 Worker 并发槽位续租暂时失败，当前任务不会因此自动重试供应商请求")
				}
				hadTransientFailure = true
				continue
			}
			if hadTransientFailure {
				log.Printf("worker slot renewal recovered task=%s", task.ID)
				s.clearWorkerSlotIssue(task.ID)
				s.logWorkerLeaseEvent(task, "info", "全局 Worker 并发槽位续租已恢复")
				hadTransientFailure = false
			}
		case <-stop:
			return
		}
	}
}

func (s *Service) recordWorkerSlotIssue(taskID string) {
	s.workerSlotMu.Lock()
	if s.workerSlotIssue == nil {
		s.workerSlotIssue = make(map[string]struct{})
	}
	s.workerSlotIssue[taskID] = struct{}{}
	s.workerSlotMu.Unlock()
}

func (s *Service) clearWorkerSlotIssue(taskID string) {
	s.workerSlotMu.Lock()
	delete(s.workerSlotIssue, taskID)
	s.workerSlotMu.Unlock()
}

func (s *Service) hasWorkerSlotIssue() bool {
	s.workerSlotMu.RLock()
	hasIssue := len(s.workerSlotIssue) > 0
	s.workerSlotMu.RUnlock()
	return hasIssue
}

func (s *Service) logWorkerLeaseEvent(task *model.Task, level string, message string) {
	if task == nil {
		return
	}
	if err := s.log(task.UserID, task.ID, level, message, ""); err != nil {
		log.Printf("worker lease task log failed task=%s: %v", task.ID, err)
	}
}

func (s *Service) recordWorkerHeartbeat(err error) {
	if err == nil && s.workerShutdownRequested() {
		return
	}
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	s.workerHealthMu.Lock()
	previousError := s.workerError
	s.workerHeartbeat = time.Now()
	s.workerError = errorText
	s.workerHealthMu.Unlock()
	if errorText != previousError {
		if errorText == "" {
			log.Printf("worker dispatcher recovered")
		} else {
			log.Printf("worker dispatcher unavailable: %s", errorText)
		}
	}
}

// 过期租约重新领取时，只允许继续明确未 dispatched 的新请求或查询已有异步任务；模糊状态绝不能自动重放。
func (s *Service) recoveredTaskCanContinue(task *model.Task) (bool, *model.BillingOrder, error) {
	if task == nil || !task.LeaseRecovered {
		return true, nil, nil
	}
	// pending/preflight 都在新请求的 dispatched 检查点之前；旧空值与 prepared 仍是模糊状态，不能据此重放。
	if providerDispatchDefinitelyNotStarted(task.ProviderCallState) && strings.TrimSpace(task.BillingOrderID) == "" {
		return true, nil, nil
	}
	if recoverableProviderPollingTask(task) {
		return true, nil, nil
	}
	if strings.TrimSpace(task.BillingOrderID) == "" {
		return false, nil, nil
	}
	order, err := s.repo.BillingOrder(task.BillingOrderID)
	if err != nil {
		return false, nil, err
	}
	// reserved 证明计费运行边界尚未建立；running 还必须同时由新状态证明 HTTP 请求没有进入 dispatched。
	if order.Status == model.BillingStatusReserved || (order.Status == model.BillingStatusRunning && providerDispatchDefinitelyNotStarted(task.ProviderCallState)) {
		return true, order, nil
	}
	return false, order, nil
}

func recoverableProviderPollingTask(task *model.Task) bool {
	if task == nil || strings.TrimSpace(task.ProviderRequestID) == "" {
		return false
	}
	return strings.HasPrefix(task.Type, "canvas_video") || strings.HasPrefix(task.Type, "video_")
}

func (s *Service) failClaimedTaskBeforeProvider(task *model.Task, claimOwner string, message string) error {
	task.Status = model.TaskStatusFailed
	task.Stage = "供应商调用前停止"
	task.Error = message
	task.PollStage = "failed"
	task.NextPollAt = nil
	task.LeaseOwner = ""
	task.LeaseExpiresAt = nil
	task.CompletedAt = ptr(time.Now())
	terminalErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner)
	refundErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "退回供应商调用前停止任务的预留积分", s.RefundBillingFromExecution(task.BillingOrderID, message))
	logErr := s.log(task.UserID, task.ID, "warn", "任务在供应商调用前停止", "未创建新的供应商请求")
	return errors.Join(errors.New(message), terminalErr, refundErr, logErr)
}

func (s *Service) stopRecoveredTaskReplay(task *model.Task, claimOwner string, order *model.BillingOrder) error {
	message := "检测到 Backend 重启或 Worker 租约中断，且没有可查询的供应商任务 ID；为避免重复计费，系统未再次调用供应商。请先核对供应商后台或账单，再由用户明确重试。"
	task.Status = model.TaskStatusFailed
	task.Stage = workerInterruptedTaskStage
	task.Error = message
	task.PollStage = "failed"
	task.NextPollAt = nil
	task.LeaseOwner = ""
	task.LeaseExpiresAt = nil
	task.CompletedAt = ptr(time.Now())
	terminalErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner)
	var billingErr error
	if order != nil && (order.Status == model.BillingStatusRunning || order.Status == model.BillingStatusUncertain) {
		billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记中断任务的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, message))
	}
	logErr := s.log(task.UserID, task.ID, "warn", workerRecoveredTaskLogTitle, "系统在供应商调用边界不明确时停止恢复，未创建新的供应商请求")
	return errors.Join(errors.New(message), terminalErr, billingErr, logErr)
}

func (s *Service) recoverWorkerTaskPanic(task *model.Task, claimOwner string, recovered any) {
	if task == nil {
		log.Printf("worker task panic task=<nil> panic_type=%T\n%s", recovered, debug.Stack())
		return
	}
	log.Printf("worker task panic task=%s panic_type=%T\n%s", task.ID, recovered, debug.Stack())
	message := "后台 Worker 在处理任务时发生异常，系统已停止本次执行；供应商请求可能已经发出并产生费用，系统不会自动重放。请先核对任务详情、供应商后台或账单。"
	providerNotDispatched := false
	if latest, stateErr := s.repo.Task(task.ID); stateErr != nil {
		log.Printf("worker panic provider state read failed task=%s: %v", task.ID, stateErr)
	} else {
		task.ProviderCallState = latest.ProviderCallState
		providerNotDispatched = providerDispatchDefinitelyNotStarted(latest.ProviderCallState)
		if providerNotDispatched {
			message = "后台 Worker 在供应商请求发出前发生异常，本次没有发出供应商请求；系统已停止任务。"
		}
	}
	var order *model.BillingOrder
	var orderErr error
	if strings.TrimSpace(task.BillingOrderID) != "" {
		order, orderErr = s.repo.BillingOrder(task.BillingOrderID)
		if orderErr == nil && (order.Status == model.BillingStatusReserved || (order.Status == model.BillingStatusRunning && providerNotDispatched)) {
			message = "后台 Worker 在供应商调用前发生异常，本次没有发出供应商请求；系统已停止任务并退回预留积分。"
		}
	}
	task.Status = model.TaskStatusFailed
	task.Stage = "Worker 异常中断"
	task.Error = message
	task.PollStage = "failed"
	task.NextPollAt = nil
	task.LeaseOwner = ""
	task.LeaseExpiresAt = nil
	task.CompletedAt = ptr(time.Now())
	terminalErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner)
	var billingErr error
	if orderErr != nil {
		billingErr = orderErr
	} else if order != nil {
		switch order.Status {
		case model.BillingStatusReserved:
			billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "退回 Worker 调用前异常的预留积分", s.RefundBillingFromExecution(task.BillingOrderID, message))
		case model.BillingStatusRunning:
			if providerNotDispatched {
				billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "退回 Worker 请求发出前异常的预留积分", s.RefundBillingFromExecution(task.BillingOrderID, message))
			} else {
				billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记 Worker 异常的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, message))
			}
		case model.BillingStatusUncertain:
			billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记 Worker 异常的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, message))
		}
	}
	logErr := s.log(task.UserID, task.ID, "error", "Worker 异常已隔离", message)
	if combined := errors.Join(terminalErr, billingErr, logErr); combined != nil {
		log.Printf("worker panic recovery incomplete task=%s: %v", task.ID, combined)
	}
}
