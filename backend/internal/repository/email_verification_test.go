package repository

import (
	"errors"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEmailVerificationCodeRequiresSentStateAndLocksAfterFailures(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.EmailVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	now := time.Now()
	previousSentAt := now.Add(-time.Minute)
	previous := model.EmailVerificationCode{
		ID: "previous", Email: "user@example.com", Purpose: "registration", CodeHash: "old",
		SentAt: &previousSentAt, ExpiresAt: now.Add(time.Minute), CreatedAt: now.Add(-time.Minute),
	}
	if err := db.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	pending := model.EmailVerificationCode{
		ID: "pending", Email: previous.Email, Purpose: previous.Purpose, CodeHash: "new",
		ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := repo.ReplaceEmailVerificationCode(&pending, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LatestEmailVerificationCode(pending.Email, pending.Purpose); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("pending code must not be verifiable, got %v", err)
	}
	var invalidated model.EmailVerificationCode
	if err := db.First(&invalidated, "id = ?", previous.ID).Error; err != nil {
		t.Fatal(err)
	}
	if invalidated.UsedAt == nil {
		t.Fatal("previous code was not invalidated")
	}
	if err := repo.MarkEmailVerificationCodeSent(pending.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	active, err := repo.LatestEmailVerificationCode(pending.Email, pending.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != pending.ID {
		t.Fatalf("active code = %q, want %q", active.ID, pending.ID)
	}

	for attempt := 1; attempt <= 5; attempt++ {
		locked, err := repo.RecordEmailVerificationFailure(pending.ID, 5, now.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if locked != (attempt == 5) {
			t.Fatalf("attempt %d locked = %v", attempt, locked)
		}
	}
	user := model.User{ID: "user-1", Username: "user-1", Email: pending.Email, DisplayName: "User", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := repo.CreateUserWithEmailVerification(&user, pending.ID, pending.Purpose, 5, now.Add(7*time.Second)); !errors.Is(err, ErrEmailVerificationUnavailable) {
		t.Fatalf("locked code registration error = %v", err)
	}
	var userCount int64
	if err := db.Model(&model.User{}).Count(&userCount).Error; err != nil {
		t.Fatal(err)
	}
	if userCount != 0 {
		t.Fatalf("locked code created %d users", userCount)
	}
}

func TestCreateUserWithEmailVerificationBindsEmailAndConsumesOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.EmailVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	now := time.Now()
	code := model.EmailVerificationCode{
		ID: "code", Email: "user@example.com", Purpose: "registration", CodeHash: "hash",
		SentAt: &now, ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now,
	}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	mismatched := model.User{ID: "wrong", Username: "wrong", Email: "other@example.com", DisplayName: "Wrong", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := repo.CreateUserWithEmailVerification(&mismatched, code.ID, code.Purpose, 5, now.Add(time.Second)); !errors.Is(err, ErrEmailVerificationUnavailable) {
		t.Fatalf("mismatched email error = %v", err)
	}
	user := model.User{ID: "user", Username: "user", Email: code.Email, DisplayName: "User", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := repo.CreateUserWithEmailVerification(&user, code.ID, code.Purpose, 5, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	replay := model.User{ID: "replay", Username: "replay", Email: code.Email, DisplayName: "Replay", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := repo.CreateUserWithEmailVerification(&replay, code.ID, code.Purpose, 5, now.Add(3*time.Second)); !errors.Is(err, ErrEmailVerificationUnavailable) {
		t.Fatalf("replayed code error = %v", err)
	}
	var userCount int64
	if err := db.Model(&model.User{}).Count(&userCount).Error; err != nil {
		t.Fatal(err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d, want 1", userCount)
	}
}
