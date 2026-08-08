package repository

import (
	"errors"
	"strings"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDuplicateProbeRequest = errors.New("duplicate channel probe submission")

func (r *Repository) ActiveTaskForGlobalRequestKey(taskType string, requestKey string) (*model.Task, error) {
	if strings.TrimSpace(taskType) == "" || strings.TrimSpace(requestKey) == "" {
		return nil, nil
	}
	var task model.Task
	err := r.db.Where("type = ? AND request_key = ? AND status IN ?", taskType, requestKey, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}).Order("created_at desc").First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &task, err
}

func (r *Repository) ChannelProbeForSubmissionKey(userID string, submissionKey string) (*model.Task, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(submissionKey) == "" {
		return nil, nil
	}
	var submission model.ChannelProbeSubmission
	aliasErr := r.db.First(&submission, "user_id = ? AND submission_key = ?", userID, submissionKey).Error
	if aliasErr == nil {
		var task model.Task
		if err := r.db.First(&task, "id = ? AND type = ?", submission.TaskID, "channel_health_probe").Error; err != nil {
			return nil, err
		}
		return &task, nil
	}
	if !errors.Is(aliasErr, gorm.ErrRecordNotFound) {
		return nil, aliasErr
	}
	// 主提交键同时保存在任务上，因此即使服务在创建任务后、写映射前退出，重发仍能找回原任务。
	var task model.Task
	err := r.db.Where("user_id = ? AND type = ? AND probe_request_key = ?", userID, "channel_health_probe", submissionKey).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &task, err
}

func (r *Repository) BindChannelProbeSubmission(userID string, submissionKey string, configRequestKey string, taskID string) error {
	userID = strings.TrimSpace(userID)
	submissionKey = strings.TrimSpace(submissionKey)
	configRequestKey = strings.TrimSpace(configRequestKey)
	taskID = strings.TrimSpace(taskID)
	if userID == "" || submissionKey == "" || configRequestKey == "" || taskID == "" {
		return ErrDuplicateProbeRequest
	}
	item := model.ChannelProbeSubmission{
		ID: newRepositoryID(), UserID: userID, SubmissionKey: submissionKey,
		ConfigRequestKey: configRequestKey, TaskID: taskID,
	}
	result := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "submission_key"}},
		DoNothing: true,
	}).Create(&item)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var existing model.ChannelProbeSubmission
	if err := r.db.First(&existing, "user_id = ? AND submission_key = ?", userID, submissionKey).Error; err != nil {
		return err
	}
	if existing.ConfigRequestKey == configRequestKey && existing.TaskID == taskID {
		return nil
	}
	return ErrDuplicateProbeRequest
}
