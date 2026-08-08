package service

import (
	"encoding/json"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

type AdminUserDetail struct {
	User             model.User                  `json:"user"`
	Account          model.CreditAccount         `json:"account"`
	Counts           repository.AdminUserCounts  `json:"counts"`
	StorageUsage     repository.UserStorageUsage `json:"storageUsage"`
	StoredFileBytes  int64                       `json:"storedFileBytes"`
	DailyUploadBytes int64                       `json:"dailyUploadBytes"`
	Quota            RuntimeResourcePolicy       `json:"quota"`
}

type AdminTaskPage struct {
	Tasks []model.Task `json:"tasks"`
	Total int64        `json:"total"`
	Page  int          `json:"page"`
	Limit int          `json:"limit"`
}

type AdminAuditPage struct {
	Events []model.AdminAuditEvent `json:"events"`
	Total  int64                   `json:"total"`
	Page   int                     `json:"page"`
	Limit  int                     `json:"limit"`
}

func (s *Service) appendAdminAudit(actor *model.User, action string, targetType string, targetID string, summary string, metadata any) error {
	return appendAdminAuditWithRepository(s.repo, actor, action, targetType, targetID, summary, metadata)
}

func appendAdminAuditWithRepository(repo *repository.Repository, actor *model.User, action string, targetType string, targetID string, summary string, metadata any) error {
	event, err := newAdminAuditEvent(actor, action, targetType, targetID, summary, metadata)
	if err != nil {
		return err
	}
	return repo.AppendAdminAudit(event)
}

func newAdminAuditEvent(actor *model.User, action string, targetType string, targetID string, summary string, metadata any) (*model.AdminAuditEvent, error) {
	if actor == nil {
		return nil, Unauthorized("请先登录")
	}
	encoded := ""
	if metadata != nil {
		data, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		encoded = string(data)
	}
	return &model.AdminAuditEvent{
		ID: newID(), ActorUserID: actor.ID, Action: strings.TrimSpace(action), TargetType: strings.TrimSpace(targetType),
		TargetID: strings.TrimSpace(targetID), Summary: truncateRunes(strings.TrimSpace(summary), 500), MetadataJSON: encoded, CreatedAt: time.Now(),
	}, nil
}

// saveSystemSettingUnchanged 通过单条条件写入复核页面读取版本，不能把凭据留空的旧快照覆盖到新配置上。
func saveSystemSettingUnchanged(repo *repository.Repository, setting *model.SystemSetting, initial *model.SystemSetting) error {
	saved, err := repo.SaveSystemSettingIfUnchanged(setting, initial)
	if err != nil {
		return err
	}
	if !saved {
		return Conflict("系统设置已被其他管理员修改、创建或重置，本次未覆盖新配置，请刷新后重试")
	}
	return nil
}

func deleteSystemSettingUnchanged(repo *repository.Repository, key string, initial *model.SystemSetting) error {
	deleted, err := repo.DeleteSystemSettingIfUnchanged(key, initial)
	if err != nil {
		return err
	}
	if !deleted {
		return Conflict("系统设置已被其他管理员修改、创建或重置，本次未覆盖新配置，请刷新后重试")
	}
	return nil
}

func (s *Service) AdminUserDetail(actor *model.User, userID string) (*AdminUserDetail, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	user, err := s.repo.User(strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	account, err := s.repo.CreditAccount(user.ID)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.AdminUserCounts(user.ID)
	if err != nil {
		return nil, err
	}
	usage, err := s.repo.UserStorageUsage(user.ID)
	if err != nil {
		return nil, err
	}
	storedFileBytes, err := s.repo.UserStoredFileBytes(user.ID)
	if err != nil {
		return nil, err
	}
	dailyUploadBytes, err := s.repo.DailyUploadBytes(user.ID, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	return &AdminUserDetail{
		User: *user, Account: *account, Counts: counts, StorageUsage: usage,
		StoredFileBytes: storedFileBytes, DailyUploadBytes: dailyUploadBytes, Quota: policy.Resource,
	}, nil
}

func (s *Service) AdminUserLedger(actor *model.User, userID string, entryType string, page int, limit int) (*WalletSummary, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if _, err := s.repo.User(userID); err != nil {
		return nil, err
	}
	page, limit = normalizeAdminPage(page, limit)
	account, err := s.repo.CreditAccount(userID)
	if err != nil {
		return nil, err
	}
	entries, total, err := s.repo.CreditLedger(userID, entryType, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	return &WalletSummary{Account: *account, Entries: entries, Total: total, Page: page, Limit: limit}, nil
}

func (s *Service) AdminUserTasks(actor *model.User, userID string, page int, limit int) (*AdminTaskPage, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if _, err := s.repo.User(userID); err != nil {
		return nil, err
	}
	page, limit = normalizeAdminPage(page, limit)
	tasks, total, err := s.repo.AdminUserTasks(userID, limit, (page-1)*limit)
	for index := range tasks {
		if tasks[index].Type == channelProbeTaskType {
			tasks[index].Error = redactSystemChannelProbeText(tasks[index].Error)
		}
	}
	return &AdminTaskPage{Tasks: tasks, Total: total, Page: page, Limit: limit}, err
}

func (s *Service) AdminUserAuditEvents(actor *model.User, userID string, page int, limit int) (*AdminAuditPage, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	if _, err := s.repo.User(userID); err != nil {
		return nil, err
	}
	page, limit = normalizeAdminPage(page, limit)
	events, total, err := s.repo.AdminAuditEvents("user", userID, limit, (page-1)*limit)
	return &AdminAuditPage{Events: events, Total: total, Page: page, Limit: limit}, err
}
