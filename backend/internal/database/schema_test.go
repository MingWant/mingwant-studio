package database

import (
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateSchemaConstrainsActiveSystemProbeAcrossAdmins(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	systemKey := "probe-system-" + strings.Repeat("a", 51)
	first := model.Task{ID: "probe-1", UserID: "admin-1", Type: "channel_health_probe", Status: model.TaskStatusQueued, RequestKey: systemKey}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.ID = "probe-2"
	duplicate.UserID = "admin-2"
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("cross-admin active system probe duplicate error = nil")
	}
	terminal := duplicate
	terminal.ID = "probe-3"
	terminal.Status = model.TaskStatusSucceeded
	if err := db.Create(&terminal).Error; err != nil {
		t.Fatalf("terminal system probe history insert error = %v", err)
	}
	customKey := "probe-user-" + strings.Repeat("b", 53)
	customTaskIDs := []string{"custom-probe-1", "custom-probe-2"}
	for index, userID := range []string{"user-1", "user-2"} {
		task := model.Task{ID: customTaskIDs[index], UserID: userID, Type: "channel_health_probe", Status: model.TaskStatusQueued, RequestKey: customKey}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("cross-user custom probe insert error = %v", err)
		}
	}
	submission := model.Task{ID: "submission-1", UserID: "user-3", Type: "channel_health_probe", Status: model.TaskStatusSucceeded, ProbeRequestKey: "probe_submit_123"}
	if err := db.Create(&submission).Error; err != nil {
		t.Fatal(err)
	}
	replayedSubmission := submission
	replayedSubmission.ID = "submission-2"
	replayedSubmission.Status = model.TaskStatusQueued
	if err := db.Create(&replayedSubmission).Error; err == nil {
		t.Fatal("same-user terminal probe submission replay error = nil")
	}
	otherUserSubmission := replayedSubmission
	otherUserSubmission.ID = "submission-3"
	otherUserSubmission.UserID = "user-4"
	if err := db.Create(&otherUserSubmission).Error; err != nil {
		t.Fatalf("cross-user probe submission insert error = %v", err)
	}
	alias := model.ChannelProbeSubmission{
		ID: "alias-1", UserID: "admin-2", SubmissionKey: "takeover_submit_123",
		ConfigRequestKey: systemKey, TaskID: first.ID,
	}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	duplicateAlias := alias
	duplicateAlias.ID = "alias-2"
	duplicateAlias.TaskID = terminal.ID
	if err := db.Create(&duplicateAlias).Error; err == nil {
		t.Fatal("same-user probe takeover alias duplicate error = nil")
	}
	crossUserAlias := duplicateAlias
	crossUserAlias.ID = "alias-3"
	crossUserAlias.UserID = "admin-3"
	if err := db.Create(&crossUserAlias).Error; err != nil {
		t.Fatalf("cross-user probe takeover alias insert error = %v", err)
	}
}

func TestMigrateSchemaConstrainsGeneratedResourcePerTaskAttemptPath(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	resource := model.Resource{
		ID: "resource-1", UserID: "user-1", Status: model.ResourceStatusReady, Size: 1,
		SourceTaskID: "task-1", SourceAttempt: "billing:order-1", SourcePath: "$/images/0", ContentSHA256: "digest",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := resource
	duplicate.ID = "resource-2"
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate ready task output insert error = nil")
	}
	otherAttempt := resource
	otherAttempt.ID = "resource-3"
	otherAttempt.SourceAttempt = "billing:order-2"
	if err := db.Create(&otherAttempt).Error; err != nil {
		t.Fatalf("new attempt insert error = %v", err)
	}
	failed := resource
	failed.ID = "resource-4"
	failed.Status = model.ResourceStatusFailed
	if err := db.Create(&failed).Error; err != nil {
		t.Fatalf("failed diagnostic row insert error = %v", err)
	}
}
