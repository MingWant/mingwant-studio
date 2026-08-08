package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClaimNextTaskMarksExpiredRunningLeaseAsRecovery(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	expired := time.Now().Add(-time.Minute)
	task := model.Task{ID: "task-recovered", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, LeaseOwner: "dead-worker", LeaseExpiresAt: &expired}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.repo.ClaimNextTask("new-worker", workerTaskLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || !claimed.LeaseRecovered || claimed.LeaseOwner != "new-worker" {
		t.Fatalf("claimed task = %#v", claimed)
	}
}

func TestClaimNextTaskDoesNotMarkFreshQueuedTaskAsRecovery(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	task := model.Task{ID: "task-queued", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusQueued}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.repo.ClaimNextTask("worker-1", workerTaskLeaseDuration)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.LeaseRecovered || claimed.ProviderCallState != model.TaskProviderCallPending {
		t.Fatalf("claimed task = %#v", claimed)
	}
}

func TestRecoveredRunningTaskWithoutProviderIDStopsBeforeReplay(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	order := model.BillingOrder{ID: "order-running", UserID: "user-1", TaskID: "task-running", Status: model.BillingStatusRunning}
	task := model.Task{ID: "task-running", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, LeaseOwner: "worker-2", BillingOrderID: order.ID, LeaseRecovered: true}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	canContinue, storedOrder, err := svc.recoveredTaskCanContinue(&task)
	if err != nil {
		t.Fatal(err)
	}
	if canContinue || storedOrder == nil || storedOrder.Status != model.BillingStatusRunning {
		t.Fatalf("recovery decision = continue:%v order:%#v", canContinue, storedOrder)
	}
	if err := svc.stopRecoveredTaskReplay(&task, "worker-2", storedOrder); err == nil || !strings.Contains(err.Error(), "未再次调用供应商") {
		t.Fatalf("stopRecoveredTaskReplay() error = %v", err)
	}
	var savedTask model.Task
	var savedOrder model.BillingOrder
	if err := db.First(&savedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedTask.Status != model.TaskStatusFailed || savedOrder.Status != model.BillingStatusUncertain || !strings.Contains(savedTask.Error, "未再次调用供应商") {
		t.Fatalf("saved task/order = %#v %#v", savedTask, savedOrder)
	}
}

func TestRecoveredReservedTaskAndVideoPollingRemainResumable(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	order := model.BillingOrder{ID: "order-reserved", UserID: "user-1", TaskID: "task-reserved", Status: model.BillingStatusReserved}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reservedTask := model.Task{ID: "task-reserved", Type: "canvas_text", BillingOrderID: order.ID, LeaseRecovered: true}
	canContinue, _, err := svc.recoveredTaskCanContinue(&reservedTask)
	if err != nil || !canContinue {
		t.Fatalf("reserved recovery = (%v, %v)", canContinue, err)
	}
	videoTask := model.Task{ID: "task-video", Type: "canvas_video", ProviderRequestID: "provider-video-1", LeaseRecovered: true}
	canContinue, _, err = svc.recoveredTaskCanContinue(&videoTask)
	if err != nil || !canContinue {
		t.Fatalf("video polling recovery = (%v, %v)", canContinue, err)
	}
	pendingCustomTask := model.Task{ID: "task-custom-pending", Type: "canvas_text", LeaseRecovered: true, ProviderCallState: model.TaskProviderCallPending}
	canContinue, _, err = svc.recoveredTaskCanContinue(&pendingCustomTask)
	if err != nil || !canContinue {
		t.Fatalf("pending custom recovery = (%v, %v)", canContinue, err)
	}
	preflightCustomTask := model.Task{ID: "task-custom-preflight", Type: "canvas_text", LeaseRecovered: true, ProviderCallState: model.TaskProviderCallPreflight}
	canContinue, _, err = svc.recoveredTaskCanContinue(&preflightCustomTask)
	if err != nil || !canContinue {
		t.Fatalf("preflight custom recovery = (%v, %v)", canContinue, err)
	}
	runningPreflightOrder := model.BillingOrder{ID: "order-running-preflight", UserID: "user-1", TaskID: "task-running-preflight", Status: model.BillingStatusRunning}
	if err := db.Create(&runningPreflightOrder).Error; err != nil {
		t.Fatal(err)
	}
	runningPreflightTask := model.Task{ID: "task-running-preflight", Type: "canvas_text", BillingOrderID: runningPreflightOrder.ID, LeaseRecovered: true, ProviderCallState: model.TaskProviderCallPreflight}
	canContinue, _, err = svc.recoveredTaskCanContinue(&runningPreflightTask)
	if err != nil || !canContinue {
		t.Fatalf("running preflight recovery = (%v, %v)", canContinue, err)
	}
	preparedCustomTask := model.Task{ID: "task-custom-prepared", Type: "canvas_text", LeaseRecovered: true, ProviderCallState: model.TaskProviderCallPrepared}
	canContinue, _, err = svc.recoveredTaskCanContinue(&preparedCustomTask)
	if err != nil || canContinue {
		t.Fatalf("prepared custom recovery = (%v, %v)", canContinue, err)
	}
}

func TestWorkerPanicIsolationMarksRunningBillingUncertain(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	order := model.BillingOrder{ID: "order-panic", UserID: "user-1", TaskID: "task-panic", Status: model.BillingStatusRunning}
	task := model.Task{ID: "task-panic", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, LeaseOwner: "worker-panic", BillingOrderID: order.ID}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	svc.recoverWorkerTaskPanic(&task, "worker-panic", "simulated panic")
	var savedTask model.Task
	var savedOrder model.BillingOrder
	if err := db.First(&savedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedTask.Status != model.TaskStatusFailed || savedOrder.Status != model.BillingStatusUncertain || !strings.Contains(savedTask.Error, "不会自动重放") {
		t.Fatalf("saved task/order = %#v %#v", savedTask, savedOrder)
	}
}

func TestWorkerPanicBeforeDispatchRefundsRunningBilling(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	if err := db.AutoMigrate(&model.CreditAccount{}, &model.CreditLedgerEntry{}); err != nil {
		t.Fatal(err)
	}
	account := model.CreditAccount{UserID: "user-panic-safe", AvailableMicrocredits: 900, ReservedMicrocredits: 100}
	order := model.BillingOrder{ID: "order-panic-safe", UserID: account.UserID, TaskID: "task-panic-safe", AmountMicrocredits: 100, Status: model.BillingStatusRunning}
	task := model.Task{ID: order.TaskID, UserID: account.UserID, Type: "canvas_text", Status: model.TaskStatusRunning, LeaseOwner: "worker-panic-safe", BillingOrderID: order.ID, ProviderCallState: model.TaskProviderCallPreflight}
	for _, value := range []any{&account, &order, &task} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc.recoverWorkerTaskPanic(&task, task.LeaseOwner, "simulated pre-dispatch panic")
	var savedTask model.Task
	var savedOrder model.BillingOrder
	var savedAccount model.CreditAccount
	if err := db.First(&savedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedAccount, "user_id = ?", account.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if savedTask.Status != model.TaskStatusFailed || savedOrder.Status != model.BillingStatusRefunded || savedAccount.AvailableMicrocredits != 1_000 || savedAccount.ReservedMicrocredits != 0 || !strings.Contains(savedTask.Error, "没有发出供应商请求") {
		t.Fatalf("pre-dispatch panic = task:%#v order:%#v account:%#v", savedTask, savedOrder, savedAccount)
	}
}

func TestPrepareClaimedTaskProviderCallRequiresLiveLeaseAndWritesAudit(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	originalExpiry := time.Now().Add(5 * time.Second)
	task := model.Task{ID: "task-provider-boundary", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, Stage: "后端接管任务", Progress: 15, LeaseOwner: "worker-live", LeaseExpiresAt: &originalExpiry, ProviderCallState: model.TaskProviderCallPending}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.prepareClaimedTaskProviderCall(&task, "调用生成模型", 35, "info", "供应商调用前检查已通过", "已确认租约"); err != nil {
		t.Fatal(err)
	}
	var saved model.Task
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	logs, err := svc.repo.TaskLogs(task.UserID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Stage != "调用生成模型" || saved.Progress != 35 || saved.ProviderCallState != model.TaskProviderCallPreflight || saved.LeaseExpiresAt == nil || !saved.LeaseExpiresAt.After(originalExpiry) || len(logs) != 1 {
		t.Fatalf("saved task/logs = %#v %#v", saved, logs)
	}
}

func TestProviderDispatchCheckpointAndCancellationRaceUseOneDatabaseBoundary(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	expiresAt := time.Now().Add(time.Minute)
	dispatched := model.Task{ID: "task-dispatched", UserID: "user-1", Status: model.TaskStatusRunning, LeaseOwner: "worker-dispatch", LeaseExpiresAt: &expiresAt, ProviderCallState: model.TaskProviderCallPreflight}
	if err := db.Create(&dispatched).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.markClaimedTaskProviderCallDispatched(dispatched.ID, dispatched.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	var savedDispatched model.Task
	if err := db.First(&savedDispatched, "id = ?", dispatched.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedDispatched.ProviderCallState != model.TaskProviderCallDispatched {
		t.Fatalf("dispatch state = %q", savedDispatched.ProviderCallState)
	}
	if err := svc.prepareClaimedTaskProviderCall(&savedDispatched, "修复分镜结构", 55, "warn", "准备第二次调用", "首轮结构无效"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedDispatched, "id = ?", dispatched.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedDispatched.ProviderCallState != model.TaskProviderCallDispatched {
		t.Fatalf("repair preparation erased earlier dispatch state: %q", savedDispatched.ProviderCallState)
	}
	if cancelled, err := svc.repo.CancelTaskIfStatusAndProviderStates(dispatched.UserID, dispatched.ID, model.TaskStatusRunning, []model.TaskProviderCallState{model.TaskProviderCallPending, model.TaskProviderCallPreflight}, time.Now()); err != nil || cancelled {
		t.Fatalf("safe cancellation after dispatch = (%v, %v)", cancelled, err)
	}

	cancelledFirst := model.Task{ID: "task-cancelled-first", UserID: "user-1", Status: model.TaskStatusRunning, LeaseOwner: "worker-cancel", LeaseExpiresAt: &expiresAt, ProviderCallState: model.TaskProviderCallPreflight}
	if err := db.Create(&cancelledFirst).Error; err != nil {
		t.Fatal(err)
	}
	cancelled, err := svc.repo.CancelTaskIfStatusAndProviderStates(cancelledFirst.UserID, cancelledFirst.ID, model.TaskStatusRunning, []model.TaskProviderCallState{model.TaskProviderCallPending, model.TaskProviderCallPreflight}, time.Now())
	if err != nil || !cancelled {
		t.Fatalf("pre-dispatch cancellation = (%v, %v)", cancelled, err)
	}
	if dispatchErr := svc.markClaimedTaskProviderCallDispatched(cancelledFirst.ID, cancelledFirst.LeaseOwner); dispatchErr == nil || !providerRequestDefinitelyNotSent(dispatchErr) {
		t.Fatalf("dispatch after cancellation error = %v", dispatchErr)
	}
}

func TestCancelRunningTaskUsesPersistedDispatchBoundary(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	if err := db.AutoMigrate(&model.CreditAccount{}, &model.CreditLedgerEntry{}); err != nil {
		t.Fatal(err)
	}
	createBillingTask := func(userID string, taskID string, orderID string, state model.TaskProviderCallState) {
		t.Helper()
		account := model.CreditAccount{UserID: userID, AvailableMicrocredits: 900, ReservedMicrocredits: 100}
		order := model.BillingOrder{ID: orderID, UserID: userID, TaskID: taskID, AmountMicrocredits: 100, Status: model.BillingStatusRunning}
		task := model.Task{ID: taskID, UserID: userID, Status: model.TaskStatusRunning, BillingOrderID: orderID, ProviderCallState: state}
		for _, value := range []any{&account, &order, &task} {
			if err := db.Create(value).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	createBillingTask("user-safe", "task-safe-cancel", "order-safe-cancel", model.TaskProviderCallPreflight)
	safeTask, err := svc.CancelTask("user-safe", "task-safe-cancel")
	if err != nil {
		t.Fatal(err)
	}
	var safeOrder model.BillingOrder
	var safeAccount model.CreditAccount
	if err := db.First(&safeOrder, "id = ?", "order-safe-cancel").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&safeAccount, "user_id = ?", "user-safe").Error; err != nil {
		t.Fatal(err)
	}
	if safeTask.Status != model.TaskStatusCancelled || safeOrder.Status != model.BillingStatusRefunded || safeAccount.AvailableMicrocredits != 1_000 || safeAccount.ReservedMicrocredits != 0 || !strings.Contains(safeTask.Error, "没有调用供应商") {
		t.Fatalf("pre-dispatch cancellation = task:%#v order:%#v account:%#v", safeTask, safeOrder, safeAccount)
	}

	createBillingTask("user-risk", "task-risk-cancel", "order-risk-cancel", model.TaskProviderCallDispatched)
	riskTask, err := svc.CancelTask("user-risk", "task-risk-cancel")
	if err != nil {
		t.Fatal(err)
	}
	var riskOrder model.BillingOrder
	var riskAccount model.CreditAccount
	if err := db.First(&riskOrder, "id = ?", "order-risk-cancel").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&riskAccount, "user_id = ?", "user-risk").Error; err != nil {
		t.Fatal(err)
	}
	if riskTask.Status != model.TaskStatusCancelled || riskOrder.Status != model.BillingStatusUncertain || riskAccount.AvailableMicrocredits != 900 || riskAccount.ReservedMicrocredits != 100 || !strings.Contains(riskTask.Error, "请勿立即重试") {
		t.Fatalf("post-dispatch cancellation = task:%#v order:%#v account:%#v", riskTask, riskOrder, riskAccount)
	}
}

func TestPrepareClaimedTaskProviderCallRejectsExpiredLeaseWithoutAudit(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	expired := time.Now().Add(-time.Second)
	task := model.Task{ID: "task-expired-boundary", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, Stage: "后端接管任务", Progress: 15, LeaseOwner: "worker-expired", LeaseExpiresAt: &expired, ProviderCallState: model.TaskProviderCallPending}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	err := svc.prepareClaimedTaskProviderCall(&task, "调用生成模型", 35, "info", "不应写入", "")
	if err == nil || !strings.Contains(err.Error(), "没有发送新的供应商请求") {
		t.Fatalf("prepareClaimedTaskProviderCall() error = %v", err)
	}
	var saved model.Task
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	logs, logErr := svc.repo.TaskLogs(task.UserID, task.ID)
	if logErr != nil {
		t.Fatal(logErr)
	}
	if saved.Stage != "后端接管任务" || saved.Progress != 15 || saved.ProviderCallState != model.TaskProviderCallPending || len(logs) != 0 {
		t.Fatalf("expired checkpoint changed task/logs = %#v %#v", saved, logs)
	}
}

func TestPrepareClaimedTaskProviderCallRollsBackProgressWhenAuditFails(t *testing.T) {
	svc, db := newWorkerSafetyTestService(t)
	expiresAt := time.Now().Add(time.Minute)
	task := model.Task{ID: "task-audit-rollback", UserID: "user-1", Type: "canvas_text", Status: model.TaskStatusRunning, Stage: "后端接管任务", Progress: 15, LeaseOwner: "worker-audit", LeaseExpiresAt: &expiresAt, ProviderCallState: model.TaskProviderCallPending}
	existing := model.TaskLog{ID: "duplicate-log", UserID: task.UserID, TaskID: task.ID, Level: "info", Message: "existing"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := &model.TaskLog{ID: existing.ID, UserID: task.UserID, TaskID: task.ID, Level: "info", Message: "duplicate"}
	if err := svc.repo.PrepareClaimedTaskProviderCall(task.ID, task.LeaseOwner, "调用生成模型", 35, workerTaskLeaseDuration, duplicate); err == nil {
		t.Fatal("PrepareClaimedTaskProviderCall() error = nil")
	}
	var saved model.Task
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Stage != "后端接管任务" || saved.Progress != 15 || saved.ProviderCallState != model.TaskProviderCallPending {
		t.Fatalf("failed audit did not roll back progress: %#v", saved)
	}
}

func TestMarshalTaskCompletionRejectsUnsupportedValues(t *testing.T) {
	if _, _, err := marshalTaskCompletion(map[string]interface{}{"invalid": func() {}}, nil); err == nil || !strings.Contains(err.Error(), "serialize task result") {
		t.Fatalf("result serialization error = %v", err)
	}
	if _, _, err := marshalTaskCompletion(map[string]interface{}{"ok": true}, []map[string]interface{}{{"invalid": func() {}}}); err == nil || !strings.Contains(err.Error(), "serialize canvas operations") {
		t.Fatalf("canvas operations serialization error = %v", err)
	}
	resultJSON, opsJSON, err := marshalTaskCompletion(map[string]interface{}{"ok": true}, []map[string]interface{}{{"type": "create"}})
	if err != nil || string(resultJSON) != `{"ok":true}` || string(opsJSON) != `[{"type":"create"}]` {
		t.Fatalf("serialized completion = %s %s, error = %v", resultJSON, opsJSON, err)
	}
}

func newWorkerSafetyTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "worker-safety.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Session{}, &model.Message{}, &model.BillingOrder{}); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db), workerSlotIssue: make(map[string]struct{})}, db
}
