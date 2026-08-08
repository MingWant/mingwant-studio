package repository

import (
	"errors"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClaimRecoverableTaskProviderRecoveryHonorsNextPollAt(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	nextPollAt := time.Now().Add(time.Minute)
	task := model.Task{
		ID: "task-1", UserID: "user-1", Type: "canvas_video_generate",
		Status: model.TaskStatusFailed, Prompt: "video", ProviderRequestID: "provider-task-1", NextPollAt: &nextPollAt,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	if err := repo.ClaimRecoverableTaskProviderRecovery(task.ID, task.UserID, "manual-recovery:early", time.Minute); !errors.Is(err, ErrTaskProviderRecoveryNotDue) {
		t.Fatalf("early recovery error = %v", err)
	}
	var saved model.Task
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.LeaseOwner != "" || saved.LeaseExpiresAt != nil {
		t.Fatalf("early recovery acquired lease = %#v", saved)
	}
	past := time.Now().Add(-time.Second)
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("next_poll_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRecoverableTaskProviderRecovery(task.ID, task.UserID, "manual-recovery:due", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.LeaseOwner != "manual-recovery:due" || saved.LeaseExpiresAt == nil {
		t.Fatalf("due recovery lease = %#v", saved)
	}
}
