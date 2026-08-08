package repository

import (
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRecordChannelModelProbeDoesNotLetOlderRequestOverwriteNewerObservation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ChannelModel{}); err != nil {
		t.Fatal(err)
	}
	item := model.ChannelModel{ID: "model-1", ChannelID: "channel-1", ModelKey: "slow-model", Capability: "text", Enabled: true}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	base := time.Now().Add(-time.Minute)
	if err := repo.RecordChannelModelProbe(item.ChannelID, item.ModelKey, "succeeded", "stream", 1_000, "new-hash", base.Add(10*time.Second), base.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordChannelModelProbe(item.ChannelID, item.ModelKey, "failed", "", 30_000, "old-hash", base, base.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	var saved model.ChannelModel
	if err := db.First(&saved, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ProbeStatus != "succeeded" || saved.ProbeTransport != "stream" || saved.ProbeConfigHash != "new-hash" {
		t.Fatalf("probe = status %q transport %q hash %q", saved.ProbeStatus, saved.ProbeTransport, saved.ProbeConfigHash)
	}
}

func TestBindChannelProbeSubmissionReplaysCrossAdminTaskAfterTerminal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.ChannelProbeSubmission{}); err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		ID: "probe-1", UserID: "admin-1", Type: "channel_health_probe", Status: model.TaskStatusRunning,
		RequestKey: "probe-system-config", ProbeRequestKey: "owner-submit-key",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	if err := repo.BindChannelProbeSubmission("admin-2", "takeover-submit-key", task.RequestKey, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.ChannelProbeForSubmissionKey("admin-2", "takeover-submit-key")
	if err != nil {
		t.Fatal(err)
	}
	if replayed == nil || replayed.ID != task.ID || replayed.Status != model.TaskStatusSucceeded {
		t.Fatalf("replayed probe = %#v", replayed)
	}
	if err := repo.BindChannelProbeSubmission("admin-2", "takeover-submit-key", "probe-system-other", "probe-2"); err != ErrDuplicateProbeRequest {
		t.Fatalf("conflicting takeover submission error = %v", err)
	}
}
