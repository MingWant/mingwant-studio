package service

import (
	"encoding/json"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNormalizeTaskInputMakesTypedProviderConfigBillable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	channelModel := model.ChannelModel{
		ID: "model-1", ChannelID: "channel-1", ModelKey: "text-model", Capability: "text",
		BillingMode: "fixed_request", UnitPriceMicrocredits: 100_000, PriceConfigured: true, Enabled: true,
	}
	if err := db.Create(&channelModel).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db)}
	input, err := normalizeTaskInput(map[string]any{
		"mode":   "text",
		"config": providerConfig{ChannelID: "channel-1", Model: "text-model", APIKey: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := svc.taskBillingOrder("user-1", &model.Task{ID: "task-1", Type: "agent_storyboard"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if order == nil || order.ChannelID != "channel-1" || order.AmountMicrocredits != 100_000 {
		t.Fatalf("taskBillingOrder() = %#v", order)
	}
}

func TestNormalizeTaskInputStillAllowsSecretProtection(t *testing.T) {
	input, err := normalizeTaskInput(map[string]any{
		"config": providerConfig{BaseURL: "https://example.com", APIKey: "private-key", Model: "text-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, ok := input["config"].(map[string]any)
	if !ok || config["apiKey"] != "private-key" {
		t.Fatalf("normalized config = %#v", input["config"])
	}
	svc := &Service{dataDir: t.TempDir()}
	if err := svc.protectTaskSecrets(input); err != nil {
		t.Fatal(err)
	}
	protected, _ := config["apiKey"].(string)
	if protected == "private-key" || !strings.HasPrefix(protected, encryptedSettingPrefix) {
		t.Fatalf("protected apiKey = %q", protected)
	}
}

func TestTaskInputRejectsInlineMedia(t *testing.T) {
	input, err := normalizeTaskInput(map[string]any{
		"referenceImages": []providerMedia{{DataURL: testReferenceImageDataURL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsInlineMediaDataURL(input) {
		t.Fatal("containsInlineMediaDataURL() = false")
	}
}

func TestGenerationTaskRequestKeyUsesCanvasOperationIdentity(t *testing.T) {
	input := map[string]any{"metadata": map[string]any{"nodeId": "node-1"}}
	first, err := generationTaskRequestKey("canvas_text", "", "canvas-1", "text", input)
	if err != nil || first == "" {
		t.Fatalf("generationTaskRequestKey() = %q, %v", first, err)
	}
	second, err := generationTaskRequestKey("canvas_text", "", "canvas-1", "text", input)
	if err != nil || second != first {
		t.Fatalf("stable request key = %q, %v; want %q", second, err, first)
	}
	different, err := generationTaskRequestKey("canvas_text", "", "canvas-1", "rewrite", input)
	if err != nil || different == first {
		t.Fatalf("operation-specific request key = %q, %v", different, err)
	}
}

func TestGenerationTaskRequestKeyRejectsMissingCanvasIdentity(t *testing.T) {
	if _, err := generationTaskRequestKey("canvas_text", "", "", "text", map[string]any{}); err == nil {
		t.Fatal("missing canvas ID error = nil")
	}
	if _, err := generationTaskRequestKey("agent_storyboard_rows", "", "canvas-1", "storyboard_rows", map[string]any{}); err == nil {
		t.Fatal("missing node ID error = nil")
	}
	key, err := generationTaskRequestKey("video_image_to_video", "", "", "", map[string]any{})
	if err != nil || key != "" {
		t.Fatalf("non-canvas request key = %q, %v", key, err)
	}
}

func TestGenerationTaskRequestKeyUsesStoryboardSessionIdentity(t *testing.T) {
	first, err := generationTaskRequestKey("agent_storyboard", "session-1", "project-1", "storyboard", map[string]any{})
	if err != nil || first == "" {
		t.Fatalf("storyboard session request key = %q, %v", first, err)
	}
	second, err := generationTaskRequestKey("agent_storyboard", "session-1", "other-project", "storyboard", map[string]any{})
	if err != nil || second != first {
		t.Fatalf("same storyboard session key = %q, %v; want %q", second, err, first)
	}
	if _, err := generationTaskRequestKey("agent_storyboard", "", "project-1", "storyboard", map[string]any{}); err == nil {
		t.Fatal("missing storyboard session ID error = nil")
	}
}

func TestNormalizeSessionRequestKey(t *testing.T) {
	key, err := normalizeSessionRequestKey(" cinematic_123 ")
	if err != nil || key != "cinematic_123" {
		t.Fatalf("normalizeSessionRequestKey() = %q, %v", key, err)
	}
	for _, value := range []string{"short", "contains space", strings.Repeat("a", 65)} {
		if _, err := normalizeSessionRequestKey(value); err == nil {
			t.Fatalf("normalizeSessionRequestKey(%q) error = nil", value)
		}
	}
}

func TestPublicTaskInputOnlyExposesValidatedFields(t *testing.T) {
	value := publicTaskInputJSON(`{"mode":"image","allowPaidStructureRepair":true,"config":{"size":"16:9","apiKey":"encrypted-secret","baseUrl":"https://private.example","systemPrompt":"private"}}`)
	var output map[string]any
	if err := json.Unmarshal([]byte(value), &output); err != nil {
		t.Fatal(err)
	}
	config, ok := output["config"].(map[string]any)
	if !ok || config["size"] != "16:9" || len(config) != 1 {
		t.Fatalf("public config = %#v", output["config"])
	}
	if output["allowPaidStructureRepair"] != true {
		t.Fatalf("repair consent = %#v", output["allowPaidStructureRepair"])
	}
	if leaked := publicTaskInputJSON(`{"allowPaidStructureRepair":"secret","config":{"size":"16:9;apiKey=secret","apiKey":"secret"}}`); leaked != "" {
		t.Fatalf("invalid public config = %s", leaked)
	}
}

func TestCreateSessionRemovesDraftWhenTaskCreationFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.Asset{}, &model.CanvasProject{}, &model.Session{}, &model.Message{}, &model.Task{}, &model.TaskLog{}, &model.Result{}, &model.ApiCallLog{}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := db.Create(&model.Task{ID: newID(), UserID: "user-1", Status: model.TaskStatusQueued, Prompt: "queued"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{repo: repository.New(db), dataDir: t.TempDir()}
	if _, err := svc.CreateSession("user-1", CreateSessionRequest{Prompt: "new session"}); err == nil {
		t.Fatal("CreateSession() error = nil")
	}
	var sessionCount int64
	var messageCount int64
	if err := db.Model(&model.Session{}).Where("user_id = ?", "user-1").Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Message{}).Where("user_id = ?", "user-1").Count(&messageCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 || messageCount != 0 {
		t.Fatalf("draft counts = sessions:%d messages:%d", sessionCount, messageCount)
	}
}

func TestFailedTaskTerminalUpdatesSessionAndMessageAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Session{}, &model.Message{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	session := model.Session{ID: "session-1", UserID: "user-1", Status: model.SessionStatusActive}
	task := model.Task{ID: "task-1", UserID: "user-1", SessionID: session.ID, Status: model.TaskStatusRunning, LeaseOwner: "worker-1", Prompt: "storyboard"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	task.Status = model.TaskStatusFailed
	task.Error = "上游网关超时（524）"
	repo := repository.New(db)
	if err := repo.SaveClaimedTaskTerminal(&task, model.TaskStatusRunning, "worker-1"); err != nil {
		t.Fatal(err)
	}
	var storedSession model.Session
	if err := db.First(&storedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	var messages []model.Message
	if err := db.Find(&messages, "session_id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedSession.Status != model.SessionStatusFailed || len(messages) != 1 || messages[0].ID != task.ID || messages[0].Content != task.Error {
		t.Fatalf("terminal session = status:%s messages:%#v", storedSession.Status, messages)
	}
	if err := repo.MarkSessionFailedForTask(task, "更新后的失败说明"); err != nil {
		t.Fatal(err)
	}
	messages = nil
	if err := db.Find(&messages, "session_id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "更新后的失败说明" {
		t.Fatalf("idempotent failure messages = %#v", messages)
	}
}

func TestRetryTaskRestoresSessionAndRecordsConfirmationAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Session{}, &model.Task{}, &model.TaskLog{}); err != nil {
		t.Fatal(err)
	}
	session := model.Session{ID: "session-retry", UserID: "user-1", Status: model.SessionStatusFailed, CanvasOpsJSON: `[{"op":"old"}]`}
	task := model.Task{ID: "task-retry", UserID: "user-1", SessionID: session.ID, Status: model.TaskStatusFailed, Error: "old failure", ProviderCallState: model.TaskProviderCallPrepared}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	retryLog := &model.TaskLog{ID: "retry-log", UserID: task.UserID, TaskID: task.ID, Level: "info", Message: "任务已重新入队", Payload: "用户已确认新的供应商请求"}
	repo := repository.New(db)
	retried, err := repo.RetryTaskWithBilling(task.UserID, task.ID, nil, 5, retryLog)
	if err != nil {
		t.Fatal(err)
	}
	var savedSession model.Session
	var logs []model.TaskLog
	if err := db.First(&savedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Find(&logs, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retried.Status != model.TaskStatusQueued || retried.ProviderCallState != model.TaskProviderCallPending || savedSession.Status != model.SessionStatusActive || savedSession.CanvasOpsJSON != "" || len(logs) != 1 || logs[0].ID != retryLog.ID {
		t.Fatalf("retry state = task:%#v session:%#v logs:%#v", retried, savedSession, logs)
	}
}

func TestRetryTaskAuditFailureRollsBackTaskSessionAndBilling(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Session{}, &model.Task{}, &model.TaskLog{}, &model.CreditAccount{}, &model.BillingOrder{}, &model.CreditLedgerEntry{}); err != nil {
		t.Fatal(err)
	}
	session := model.Session{ID: "session-retry-rollback", UserID: "user-1", Status: model.SessionStatusFailed, CanvasOpsJSON: "old-ops"}
	task := model.Task{ID: "task-retry-rollback", UserID: "user-1", SessionID: session.ID, Status: model.TaskStatusFailed, Error: "old failure", ProviderCallState: model.TaskProviderCallPrepared}
	account := model.CreditAccount{UserID: task.UserID, AvailableMicrocredits: 1_000}
	existingLog := model.TaskLog{ID: "duplicate-retry-log", UserID: task.UserID, TaskID: task.ID, Level: "info", Message: "existing"}
	for _, value := range []any{&session, &task, &account, &existingLog} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	order := &model.BillingOrder{ID: "retry-order", UserID: task.UserID, TaskID: task.ID, AmountMicrocredits: 100, Status: model.BillingStatusReserved}
	duplicateLog := &model.TaskLog{ID: existingLog.ID, UserID: task.UserID, TaskID: task.ID, Level: "info", Message: "duplicate"}
	if _, err := repository.New(db).RetryTaskWithBilling(task.UserID, task.ID, order, 5, duplicateLog); err == nil {
		t.Fatal("RetryTaskWithBilling() error = nil")
	}
	var savedTask model.Task
	var savedSession model.Session
	var savedAccount model.CreditAccount
	var orderCount int64
	if err := db.First(&savedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&savedAccount, "user_id = ?", account.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BillingOrder{}).Where("id = ?", order.ID).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if savedTask.Status != model.TaskStatusFailed || savedTask.ProviderCallState != model.TaskProviderCallPrepared || savedSession.Status != model.SessionStatusFailed || savedSession.CanvasOpsJSON != "old-ops" || savedAccount.AvailableMicrocredits != 1_000 || savedAccount.ReservedMicrocredits != 0 || orderCount != 0 {
		t.Fatalf("rollback state = task:%#v session:%#v account:%#v orders:%d", savedTask, savedSession, savedAccount, orderCount)
	}
}

func TestSessionDetailRepairsLegacyActiveFailedSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Session{}, &model.Message{}, &model.Task{}, &model.Result{}); err != nil {
		t.Fatal(err)
	}
	session := model.Session{ID: "session-legacy", UserID: "user-1", Status: model.SessionStatusActive}
	task := model.Task{
		ID: "task-legacy", UserID: "user-1", SessionID: session.ID, Type: "agent_storyboard",
		Status: model.TaskStatusFailed, Stage: "任务失败", Progress: 35, Prompt: "storyboard", Error: "模型返回 524",
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db)}
	detail, err := svc.SessionDetail("user-1", session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Session.Status != model.SessionStatusFailed || len(detail.Tasks) != 1 {
		t.Fatalf("SessionDetail() = %#v", detail)
	}
	if detail.Tasks[0].Stage != task.Stage || detail.Tasks[0].Progress != task.Progress || detail.Tasks[0].Error != task.Error {
		t.Fatalf("session task summary = %#v", detail.Tasks[0])
	}
	if len(detail.Messages) != 1 || detail.Messages[0].ID != task.ID || detail.Messages[0].Content != task.Error {
		t.Fatalf("session failure messages = %#v", detail.Messages)
	}
}
