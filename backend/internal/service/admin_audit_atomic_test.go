package service

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRuntimePolicyRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.SystemSetting{})
	svc.ConfigureShutdownDrainTimeout(40 * time.Minute)
	policy := defaultRuntimePolicy()
	policy.Task.ActiveTaskLimit++
	if _, err := svc.UpdateRuntimePolicySetting(adminAuditTestActor(), policy); err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.SystemSetting{}, "key = ?", runtimePolicySettingKey)
}

func TestCreditPolicyRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.SystemSetting{})
	policy := defaultCreditPolicy()
	policy.CheckinBonusMicrocredits++
	if _, err := svc.UpdateCreditPolicy(adminAuditTestActor(), policy); err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.SystemSetting{}, "key = ?", creditPolicySettingKey)
}

func TestSystemChannelCreationRollsBackWhenAuditWriteFails(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	svc, db := newAdminAuditTestService(t, &model.ModelChannel{}, &model.ChannelModel{})
	useGlobalConcurrency := true
	_, err := svc.CreateSystemChannel(adminAuditTestActor(), ChannelRequest{
		Name: "原子审计渠道", BaseURL: upstream.URL, APIKey: "test-key", InterfaceType: string(model.ChannelInterfaceChatCompletion),
		Models: []string{"audit-model"}, UseGlobalConcurrency: &useGlobalConcurrency,
	})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.ModelChannel{}, "1 = 1")
	assertAdminAuditRollbackCount(t, db, &model.ChannelModel{}, "1 = 1")
}

func TestChannelModelPriceRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.ModelChannel{}, &model.ChannelModel{})
	channel := model.ModelChannel{
		ID: "channel-audit", UserID: "admin-audit", Scope: model.ChannelScopeSystem, Enabled: true, Name: "测试渠道",
		BaseURL: "https://example.com/v1", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion, ModelsJSON: "[]",
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	enabled := true
	_, err := svc.SaveAdminChannelModel(adminAuditTestActor(), channel.ID, "", ChannelModelRequest{
		ModelKey: "priced-model", Capability: "text", Protocol: string(model.ChannelInterfaceChatCompletion), BillingMode: "fixed_request",
		UnitPriceMicrocredits: 123, PriceConfigured: true, Enabled: &enabled,
	})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.ChannelModel{}, "channel_id = ?", channel.ID)
	var stored model.ModelChannel
	if err := db.First(&stored, "id = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ModelsJSON != "[]" {
		t.Fatalf("channel models projection changed after rollback: %s", stored.ModelsJSON)
	}
}

func TestAdminUserSecurityUpdateRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.User{}, &model.AuthSession{})
	user := model.User{ID: "audit-user", Username: "audit-user", Role: model.UserRoleUser, Status: model.UserStatusActive}
	session := model.AuthSession{ID: "audit-session", UserID: user.ID, TokenHash: "hash", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	_, err := svc.UpdateUser(adminAuditTestActor(), user.ID, UpdateUserRequest{Status: model.UserStatusDisabled})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	var stored model.User
	if err := db.First(&stored, "id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.UserStatusActive {
		t.Fatalf("user status changed after rollback: %s", stored.Status)
	}
	var sessionCount int64
	if err := db.Model(&model.AuthSession{}).Where("user_id = ?", user.ID).Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("auth sessions after rollback = %d, want 1", sessionCount)
	}
}

func TestAdminCreditAdjustmentRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.User{}, &model.CreditAccount{}, &model.CreditLedgerEntry{})
	user := model.User{ID: "credit-user", Username: "credit-user", Role: model.UserRoleUser, Status: model.UserStatusActive}
	account := model.CreditAccount{UserID: user.ID, AvailableMicrocredits: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	_, err := svc.AdminAdjustCredits(adminAuditTestActor(), user.ID, AdminCreditAdjustmentRequest{AmountMicrocredits: 50, Note: "原子调账"})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	var stored model.CreditAccount
	if err := db.First(&stored, "user_id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AvailableMicrocredits != account.AvailableMicrocredits {
		t.Fatalf("credit balance changed after rollback: %d", stored.AvailableMicrocredits)
	}
	assertAdminAuditRollbackCount(t, db, &model.CreditLedgerEntry{}, "user_id = ?", user.ID)
}

func TestRedeemBatchCreationRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.RedeemBatch{}, &model.RedeemCode{})
	_, err := svc.AdminCreateRedeemBatch(adminAuditTestActor(), CreateRedeemBatchRequest{AmountMicrocredits: CreditScale, Count: 2})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.RedeemBatch{}, "1 = 1")
	assertAdminAuditRollbackCount(t, db, &model.RedeemCode{}, "1 = 1")
}

func TestRedeemCodeDisableRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.RedeemBatch{}, &model.RedeemCode{})
	batch := model.RedeemBatch{ID: "redeem-batch", AmountMicrocredits: CreditScale, Count: 1, CreatedBy: "admin-audit"}
	code := model.RedeemCode{ID: "redeem-code", BatchID: batch.ID, CodeHash: "redeem-code-hash", CodeSuffix: "hash", AmountMicrocredits: CreditScale, Status: model.RedeemCodeUnused}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.AdminDisableRedeemCode(adminAuditTestActor(), batch.ID, code.ID); err == nil {
		t.Fatal("expected missing audit table error")
	}
	var stored model.RedeemCode
	if err := db.First(&stored, "id = ?", code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != model.RedeemCodeUnused {
		t.Fatalf("redeem code status changed after rollback: %s", stored.Status)
	}
}

func TestBillingResolutionRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.BillingOrder{}, &model.CreditAccount{}, &model.CreditLedgerEntry{})
	account := model.CreditAccount{UserID: "billing-user", ReservedMicrocredits: 100}
	order := model.BillingOrder{
		ID: "billing-order", UserID: account.UserID, IdempotencyKey: "billing-audit-key", AmountMicrocredits: 100,
		Status: model.BillingStatusUncertain, ProviderRequestID: "provider-request",
	}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	_, err := svc.ResolveBillingOrder(adminAuditTestActor(), order.ID, ResolveBillingRequest{Action: "settle", Note: "供应商账单已确认"})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	var storedOrder model.BillingOrder
	if err := db.First(&storedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedOrder.Status != model.BillingStatusUncertain || storedOrder.ResolvedBy != "" {
		t.Fatalf("billing order changed after rollback: %#v", storedOrder)
	}
	var storedAccount model.CreditAccount
	if err := db.First(&storedAccount, "user_id = ?", account.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if storedAccount.ReservedMicrocredits != account.ReservedMicrocredits {
		t.Fatalf("reserved balance changed after rollback: %d", storedAccount.ReservedMicrocredits)
	}
	assertAdminAuditRollbackCount(t, db, &model.CreditLedgerEntry{}, "user_id = ?", account.UserID)
}

func TestRegistrationSettingRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.SystemSetting{})
	if _, err := svc.UpdateRegistrationSetting(adminAuditTestActor(), RegistrationSettingRequest{Enabled: true}); err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.SystemSetting{}, "key = ?", registrationSettingKey)
}

func TestSystemSettingVersionCheckRejectsStaleAdminWrite(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.SystemSetting{})
	setting := model.SystemSetting{Key: "stale-setting", ValueJSON: `{"enabled":false}`, UpdatedBy: "admin-a"}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	initial, err := svc.repo.SystemSetting(setting.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", setting.Key).Updates(map[string]any{
		"value_json": `{"enabled":true}`, "updated_by": "admin-b", "updated_at": time.Now().Add(time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}
	err = svc.repo.WithTransaction(func(txRepo *repository.Repository) error {
		candidate := model.SystemSetting{Key: setting.Key, ValueJSON: `{"enabled":false}`, UpdatedBy: "admin-a"}
		return saveSystemSettingUnchanged(txRepo, &candidate, initial)
	})
	authErr, ok := err.(*AuthError)
	if !ok || authErr.Status != http.StatusConflict {
		t.Fatalf("stale setting error = %#v, want HTTP 409", err)
	}
	var stored model.SystemSetting
	if err := db.First(&stored, "key = ?", setting.Key).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ValueJSON != `{"enabled":true}` || stored.UpdatedBy != "admin-b" {
		t.Fatalf("newer setting was overwritten: %#v", stored)
	}
}

func TestAnnouncementCreationRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.Announcement{})
	_, err := svc.CreateAnnouncement(adminAuditTestActor(), CreateAnnouncementRequest{Title: "原子公告", Content: "公告正文", Level: model.AnnouncementLevelInfo})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.Announcement{}, "1 = 1")
}

func TestModelPricingCreationRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.ModelPricing{})
	_, err := svc.SaveModelPricing(adminAuditTestActor(), "", ModelPricingRequest{Model: "audit-model", Capability: "text", Currency: "USD", PerRequestMicros: 100})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.ModelPricing{}, "1 = 1")
}

func TestStoryboardPromptCreationRollsBackWhenAuditWriteFails(t *testing.T) {
	svc, db := newAdminAuditTestService(t, &model.StoryboardPromptTemplate{})
	enabled := false
	_, err := svc.CreateStoryboardPromptTemplate(adminAuditTestActor(), StoryboardPromptTemplateRequest{Name: "原子模板", Content: "{{剧情}}", Enabled: &enabled})
	if err == nil {
		t.Fatal("expected missing audit table error")
	}
	assertAdminAuditRollbackCount(t, db, &model.StoryboardPromptTemplate{}, "1 = 1")
}

func TestSystemChannelAuditIsWrittenOnceWithoutSecrets(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	svc, db := newAdminAuditTestService(t, &model.ModelChannel{}, &model.ChannelModel{}, &model.AdminAuditEvent{})
	useGlobalConcurrency := true
	const apiKey = "audit-secret-key"
	created, err := svc.CreateSystemChannel(adminAuditTestActor(), ChannelRequest{
		Name: "安全审计渠道", BaseURL: upstream.URL, APIKey: apiKey, InterfaceType: string(model.ChannelInterfaceChatCompletion),
		Models: []string{"audit-model"}, UseGlobalConcurrency: &useGlobalConcurrency,
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []model.AdminAuditEvent
	if err := db.Order("created_at asc").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "channel.create" || events[0].TargetID != created.ID {
		t.Fatalf("audit events = %#v", events)
	}
	if strings.Contains(events[0].MetadataJSON, apiKey) || strings.Contains(events[0].MetadataJSON, upstream.URL) {
		t.Fatalf("audit metadata contains channel credential or address: %s", events[0].MetadataJSON)
	}
}

func newAdminAuditTestService(t *testing.T, models ...any) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin-audit-atomic.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	return New(repository.New(db), t.TempDir()), db
}

func adminAuditTestActor() *model.User {
	return &model.User{ID: "admin-audit", Role: model.UserRoleAdmin}
}

func assertAdminAuditRollbackCount(t *testing.T, db *gorm.DB, value any, query string, args ...any) {
	t.Helper()
	var count int64
	if err := db.Model(value).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback count = %d, want 0", count)
	}
}
