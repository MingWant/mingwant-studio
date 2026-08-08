package service

import (
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRuntimePolicyDefaultsAndSelfUseModeValidate(t *testing.T) {
	defaults := defaultRuntimePolicy()
	if err := validateRuntimePolicy(defaults); err != nil {
		t.Fatalf("default runtime policy error = %v", err)
	}
	if defaults.Task.TextTimeoutMinutes != 15 || defaults.Task.StoryboardTimeoutMinutes != 30 {
		t.Fatalf("slow model timeouts = text %d, storyboard %d", defaults.Task.TextTimeoutMinutes, defaults.Task.StoryboardTimeoutMinutes)
	}
	if defaults.Request.CustomRelayTimeoutMinutes != 35 {
		t.Fatalf("custom relay timeout = %d", defaults.Request.CustomRelayTimeoutMinutes)
	}
	selfUse := selfUseRuntimePolicy()
	if err := validateRuntimePolicy(selfUse); err != nil {
		t.Fatalf("self-use runtime policy error = %v", err)
	}
	if selfUse.Task.WorkerConcurrency != 999 || selfUse.Resource.ResourceUploadMB != 999 {
		t.Fatalf("self-use maxima = worker %d, upload %d", selfUse.Task.WorkerConcurrency, selfUse.Resource.ResourceUploadMB)
	}
}

func TestRuntimePolicyCannotExceedShutdownDrainWindow(t *testing.T) {
	svc := &Service{shutdownDrainTimeout: 40 * time.Minute}
	policy := defaultRuntimePolicy()
	if err := svc.validateRuntimePolicyShutdownWindow(policy); err != nil {
		t.Fatal(err)
	}
	policy.Request.CustomRelayTimeoutMinutes = 41
	if err := svc.validateRuntimePolicyShutdownWindow(policy); err == nil {
		t.Fatal("relay timeout above shutdown drain window must be rejected")
	}
	policy = defaultRuntimePolicy()
	policy.Task.StoryboardTimeoutMinutes = 41
	if err := svc.validateRuntimePolicyShutdownWindow(policy); err == nil {
		t.Fatal("task timeout above shutdown drain window must be rejected")
	}
}

func TestSelfUseRuntimePolicyIsCappedToShutdownDrainWindow(t *testing.T) {
	svc := &Service{shutdownDrainTimeout: 40 * time.Minute}
	result, err := svc.AdminSelfUseRuntimePolicy(&model.User{Role: model.UserRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.TextTimeoutMinutes != 40 || result.Task.StoryboardTimeoutMinutes != 40 || result.Request.CustomRelayTimeoutMinutes != 40 {
		t.Fatalf("self-use timeouts = text %d, storyboard %d, relay %d", result.Task.TextTimeoutMinutes, result.Task.StoryboardTimeoutMinutes, result.Request.CustomRelayTimeoutMinutes)
	}
}

func TestRuntimePolicyRejectsSingleFileAboveAccountCapacity(t *testing.T) {
	policy := defaultRuntimePolicy()
	policy.Resource.StoredFileGB = 1
	policy.Resource.ResourceUploadMB = 999
	if err := validateRuntimePolicy(policy); err != nil {
		t.Fatalf("999MB should fit in 1GB: %v", err)
	}
	policy.Resource.StoredFileGB = 0
	if err := validateRuntimePolicy(policy); err == nil {
		t.Fatal("zero account capacity should be rejected")
	}
}

func TestRuntimePolicySaveAndResetTakeEffectImmediately(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.AdminAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	svc := New(repository.New(db), t.TempDir())
	actor := &model.User{ID: "admin", Role: model.UserRoleAdmin}
	policy := defaultRuntimePolicy()
	policy.Task.ActiveTaskLimit = 17
	if _, err := svc.UpdateRuntimePolicySetting(actor, policy); err != nil {
		t.Fatal(err)
	}
	effective, err := svc.RuntimePolicy()
	if err != nil || effective.Task.ActiveTaskLimit != 17 {
		t.Fatalf("effective active task limit = %d, error = %v", effective.Task.ActiveTaskLimit, err)
	}
	if _, err := svc.ResetRuntimePolicySetting(actor); err != nil {
		t.Fatal(err)
	}
	effective, err = svc.RuntimePolicy()
	if err != nil || effective.Task.ActiveTaskLimit != 5 {
		t.Fatalf("reset active task limit = %d, error = %v", effective.Task.ActiveTaskLimit, err)
	}
}
