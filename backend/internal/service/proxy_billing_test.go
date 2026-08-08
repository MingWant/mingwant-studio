package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestProxyBillingIdempotencyRejectsDuplicateWithoutNewReservation(t *testing.T) {
	svc, db := newProxyBillingTestService(t)
	key, err := svc.PrepareProxyBillingIdempotency("user-1", " canvas-agent:session-1:turn-1:1 ")
	if err != nil || key != "canvas-agent:session-1:turn-1:1" {
		t.Fatalf("PrepareProxyBillingIdempotency() = %q, %v", key, err)
	}
	order, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "text", "canvas_agent", key, 1)
	if err != nil {
		t.Fatal(err)
	}
	if order.IdempotencyKey != proxyBillingIdempotencyPrefix+key {
		t.Fatalf("order idempotency key = %q", order.IdempotencyKey)
	}

	if _, err := svc.PrepareProxyBillingIdempotency("user-1", key); !isAuthStatus(err, 409) {
		t.Fatalf("duplicate preflight error = %v", err)
	}
	if _, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "text", "canvas_agent", key, 1); !isAuthStatus(err, 409) {
		t.Fatalf("duplicate reservation error = %v", err)
	}

	var orderCount int64
	if err := db.Model(&model.BillingOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	var reserveCount int64
	if err := db.Model(&model.CreditLedgerEntry{}).Where("type = ?", model.CreditLedgerReserve).Count(&reserveCount).Error; err != nil {
		t.Fatal(err)
	}
	var account model.CreditAccount
	if err := db.First(&account, "user_id = ?", "user-1").Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 || reserveCount != 1 || account.AvailableMicrocredits != 900_000 || account.ReservedMicrocredits != 100_000 {
		t.Fatalf("orders=%d reserves=%d account=%#v", orderCount, reserveCount, account)
	}
}

func TestProxyBillingIdempotencyRejectsMissingOrUnsafeKey(t *testing.T) {
	svc, _ := newProxyBillingTestService(t)
	for _, value := range []string{"", "contains space", "line\nbreak", strings.Repeat("a", proxyBillingIdempotencyMaxLength+1)} {
		if _, err := svc.PrepareProxyBillingIdempotency("user-1", value); !isAuthStatus(err, 400) {
			t.Fatalf("PrepareProxyBillingIdempotency(%q) error = %v", value, err)
		}
	}
}

func TestProxyBillingRejectsCapabilityMismatch(t *testing.T) {
	svc, db := newProxyBillingTestService(t)
	if _, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "image", "image", "image-request-1", 1); !isAuthStatus(err, 400) {
		t.Fatalf("capability mismatch error = %v", err)
	}
	var count int64
	if err := db.Model(&model.BillingOrder{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("billing orders = %d, want 0", count)
	}
}

func TestAutomaticSettlementCannotCrossManualReviewBoundary(t *testing.T) {
	svc, db := newProxyBillingTestService(t)
	order, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "text", "canvas_agent", "review-boundary-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingRunning(order.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingUncertain(order.ID, "成功日志缺失"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SettleBillingFromExecution(order.ID, "provider-request-1"); !errors.Is(err, repository.ErrBillingReviewRequired) {
		t.Fatalf("automatic settlement error = %v", err)
	}

	var pending model.BillingOrder
	if err := db.First(&pending, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != model.BillingStatusUncertain {
		t.Fatalf("status after automatic settlement = %q", pending.Status)
	}
	if err := svc.SettleBilling(order.ID, "provider-request-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingUncertain(order.ID, "不得覆盖已结算终态"); !errors.Is(err, repository.ErrBillingStateConflict) {
		t.Fatalf("uncertain after settlement error = %v", err)
	}
}

func TestAutomaticSettlementStillSettlesRunningOrder(t *testing.T) {
	svc, db := newProxyBillingTestService(t)
	order, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "text", "canvas_agent", "normal-settlement-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingRunning(order.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SettleBillingFromExecution(order.ID, "provider-request-2"); err != nil {
		t.Fatal(err)
	}
	var settled model.BillingOrder
	if err := db.First(&settled, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.BillingStatusSettled {
		t.Fatalf("status = %q", settled.Status)
	}
}

func TestAutomaticRefundCannotCrossManualReviewBoundary(t *testing.T) {
	svc, db := newProxyBillingTestService(t)
	order, err := svc.ReserveProxyBilling("user-1", "channel-1", "text-model", "text", "canvas_agent", "review-refund-boundary-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingRunning(order.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkBillingUncertain(order.ID, "供应商状态不确定"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefundBillingFromExecution(order.ID, "迟到的明确拒绝"); !errors.Is(err, repository.ErrBillingReviewRequired) {
		t.Fatalf("automatic refund error = %v", err)
	}

	var pending model.BillingOrder
	if err := db.First(&pending, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != model.BillingStatusUncertain {
		t.Fatalf("status after automatic refund = %q", pending.Status)
	}
	if err := svc.RefundBilling(order.ID, "管理员已核对供应商未计费"); err != nil {
		t.Fatal(err)
	}
}

func newProxyBillingTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "proxy-billing.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}, &model.CreditAccount{}, &model.CreditLedgerEntry{}, &model.BillingOrder{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	channelModel := model.ChannelModel{
		ID: "model-1", ChannelID: "channel-1", ModelKey: "text-model", Capability: "text",
		BillingMode: "fixed_request", UnitPriceMicrocredits: 100_000, PriceConfigured: true, Enabled: true, PriceVersion: 1,
	}
	if err := db.Create(&channelModel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CreditAccount{UserID: "user-1", AvailableMicrocredits: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db)}, db
}

func isAuthStatus(err error, status int) bool {
	var authErr *AuthError
	return errors.As(err, &authErr) && authErr.Status == status
}
