package repository

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInsufficientCredits    = errors.New("insufficient credits")
	ErrRedeemCodeInvalid      = errors.New("redeem code invalid")
	ErrActiveTaskLimit        = errors.New("active task limit reached")
	ErrDuplicateActiveTask    = errors.New("duplicate active task request")
	ErrTaskNotRetryable       = errors.New("task is not retryable")
	ErrTaskSessionUnavailable = errors.New("task session is unavailable")
	ErrTaskBillingPending     = errors.New("task billing is still pending")
	ErrBillingStateConflict   = errors.New("billing state conflict")
	ErrBillingReviewRequired  = errors.New("billing order requires manual review")
)

// 先抢占唯一业务键再更新账户，确保注册和签到奖励在多实例并发下只入账一次。
func (r *Repository) GrantCreditsOnce(userID string, entryType model.CreditLedgerType, amount int64, referenceKey string, note string) (*model.CreditAccount, bool, error) {
	var account model.CreditAccount
	granted := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		account = model.CreditAccount{UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return err
		}
		entry := model.CreditLedgerEntry{ID: newRepositoryID(), UserID: userID, Type: entryType, AmountMicrocredits: amount, ReferenceKey: &referenceKey, Note: note}
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "reference_key"}}, DoNothing: true}).Create(&entry)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			return tx.First(&account, "user_id = ?", userID).Error
		}
		granted = true
		if err := tx.Model(&model.CreditAccount{}).Where("user_id = ?", userID).Updates(map[string]any{
			"available_microcredits": gorm.Expr("available_microcredits + ?", amount),
			"version":                gorm.Expr("version + 1"),
			"updated_at":             time.Now(),
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		return tx.Model(&entry).Updates(map[string]any{
			"available_delta_microcredits": amount,
			"available_after_microcredits": account.AvailableMicrocredits,
			"reserved_after_microcredits":  account.ReservedMicrocredits,
		}).Error
	})
	return &account, granted, err
}

type AdminRedeemCodeRow struct {
	model.RedeemCode
	RedeemedUsername    string `json:"redeemedUsername" gorm:"column:redeemed_username"`
	RedeemedDisplayName string `json:"redeemedDisplayName" gorm:"column:redeemed_display_name"`
}

func (r *Repository) ChannelModels(channelID string, includeDisabled bool) ([]model.ChannelModel, error) {
	var items []model.ChannelModel
	query := r.db.Where("channel_id = ?", channelID).Order("created_at asc")
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}
	return items, query.Find(&items).Error
}

func (r *Repository) ChannelModelHistoryExists(channelID string) (bool, error) {
	var count int64
	err := r.db.Unscoped().Model(&model.ChannelModel{}).Where("channel_id = ?", channelID).Limit(1).Count(&count).Error
	return count > 0, err
}

func (r *Repository) ChannelModelByID(channelID string, id string) (*model.ChannelModel, error) {
	var item model.ChannelModel
	if err := r.db.First(&item, "id = ? AND channel_id = ?", id, channelID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) ChannelModelByKey(channelID string, modelKey string) (*model.ChannelModel, error) {
	var item model.ChannelModel
	if err := r.db.First(&item, "channel_id = ? AND model_key = ? AND enabled = ?", channelID, modelKey, true).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) ChannelModelByKeyIncludingDisabled(channelID string, modelKey string) (*model.ChannelModel, error) {
	var item model.ChannelModel
	if err := r.db.First(&item, "channel_id = ? AND model_key = ?", channelID, modelKey).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) SaveChannelModel(item *model.ChannelModel) error {
	return r.db.Save(item).Error
}

// AdminSystemChannelForUpdate 串行化同一渠道的模型清单写入，避免并发管理员留下过期 models_json。
func (r *Repository) AdminSystemChannelForUpdate(id string) (*model.ModelChannel, error) {
	var channel model.ModelChannel
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&channel, "id = ? AND scope = ?", id, model.ChannelScopeSystem).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// 流式观察只更新探针字段，并以请求开始时间阻止迟到的旧请求覆盖更新的测活结论。
func (r *Repository) RecordChannelModelProbe(channelID string, modelKey string, status string, transport string, durationMs int64, configHash string, startedAt time.Time, checkedAt time.Time) error {
	var item model.ChannelModel
	if err := r.db.Select("id", "probe_checked_at").First(&item, "channel_id = ? AND model_key = ?", channelID, modelKey).Error; err != nil {
		return err
	}
	if item.ProbeCheckedAt != nil && item.ProbeCheckedAt.After(startedAt) {
		return nil
	}
	return r.db.Model(&model.ChannelModel{}).
		Where("id = ? AND (probe_checked_at IS NULL OR probe_checked_at <= ?)", item.ID, startedAt).
		Updates(map[string]any{
			"probe_status": status, "probe_transport": transport, "probe_duration_ms": durationMs,
			"probe_checked_at": checkedAt, "probe_config_hash": configHash, "updated_at": checkedAt,
		}).Error
}

// 工具诊断独立保存，不能覆盖文本测活状态；同样按请求开始时间拒绝迟到旧结果。
func (r *Repository) RecordChannelModelToolProbe(channelID string, modelKey string, status string, verifierVersion string, configHash string, startedAt time.Time, checkedAt time.Time) error {
	var item model.ChannelModel
	if err := r.db.Select("id", "tool_probe_checked_at").First(&item, "channel_id = ? AND model_key = ?", channelID, modelKey).Error; err != nil {
		return err
	}
	if item.ToolProbeCheckedAt != nil && item.ToolProbeCheckedAt.After(startedAt) {
		return nil
	}
	return r.db.Model(&model.ChannelModel{}).
		Where("id = ? AND (tool_probe_checked_at IS NULL OR tool_probe_checked_at <= ?)", item.ID, startedAt).
		Updates(map[string]any{
			"tool_probe_status": status, "tool_probe_checked_at": checkedAt, "tool_probe_verifier_version": verifierVersion,
			"tool_probe_config_hash": configHash, "updated_at": checkedAt,
		}).Error
}

func (r *Repository) ClearChannelModelProbes(channelID string) error {
	return r.db.Model(&model.ChannelModel{}).Where("channel_id = ?", channelID).Updates(map[string]any{
		"probe_status": "", "probe_transport": "", "probe_duration_ms": 0, "probe_checked_at": nil, "probe_config_hash": "",
		"tool_probe_status": "", "tool_probe_checked_at": nil, "tool_probe_verifier_version": "", "tool_probe_config_hash": "",
	}).Error
}

func (r *Repository) DeleteChannelModel(channelID string, id string, now time.Time) error {
	result := r.db.Model(&model.ChannelModel{}).
		Where("id = ? AND channel_id = ?", id, channelID).
		Updates(map[string]any{"enabled": false, "price_version": gorm.Expr("price_version + 1"), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return r.db.Where("id = ? AND channel_id = ?", id, channelID).Delete(&model.ChannelModel{}).Error
}

func (r *Repository) CreateMissingChannelModels(items []model.ChannelModel) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	// 拉取目录可能与其他管理员操作并发，唯一键冲突时保留已有定价配置；分批写入避免 SQLite/PostgreSQL 参数上限被异常大目录击穿。
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&items, 100)
	return result.RowsAffected, result.Error
}

func (r *Repository) CreditAccount(userID string) (*model.CreditAccount, error) {
	account := model.CreditAccount{UserID: userID}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
		return nil, err
	}
	if err := r.db.First(&account, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *Repository) CreditAccounts(userIDs []string) ([]model.CreditAccount, error) {
	if len(userIDs) == 0 {
		return []model.CreditAccount{}, nil
	}
	var accounts []model.CreditAccount
	err := r.db.Where("user_id IN ?", userIDs).Find(&accounts).Error
	return accounts, err
}

func (r *Repository) CreditLedger(userID string, entryType string, limit int, offset int) ([]model.CreditLedgerEntry, int64, error) {
	var items []model.CreditLedgerEntry
	var total int64
	query := r.db.Model(&model.CreditLedgerEntry{}).Where("user_id = ? AND type <> ?", userID, model.CreditLedgerReserve)
	switch entryType {
	case "income":
		query = query.Where("type IN ?", []model.CreditLedgerType{model.CreditLedgerRedeem, model.CreditLedgerAdminGrant, model.CreditLedgerAdminAdjust, model.CreditLedgerSignupBonus, model.CreditLedgerCheckinBonus})
	case "consume":
		query = query.Where("type = ?", model.CreditLedgerConsume)
	case "refund":
		query = query.Where("type = ?", model.CreditLedgerRefund)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	err := query.Order("created_at desc").Limit(limit).Offset(offset).Find(&items).Error
	return items, total, err
}

func (r *Repository) CreditLedgerReferenceExists(referenceKey string) (bool, error) {
	var count int64
	err := r.db.Model(&model.CreditLedgerEntry{}).Where("reference_key = ?", referenceKey).Count(&count).Error
	return count > 0, err
}

func (r *Repository) CreateTaskWithCreditReservation(task *model.Task, order *model.BillingOrder, activeTaskLimit int) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := enforceActiveTaskLimit(tx, task.UserID, activeTaskLimit); err != nil {
			return err
		}
		if err := enforceTaskRequestKeyAvailable(tx, task.UserID, task.RequestKey, ""); err != nil {
			return err
		}
		if err := reserveBillingOrder(tx, order); err != nil {
			return err
		}
		return taskRequestKeyError(tx.Create(task).Error)
	})
}

func (r *Repository) CreateTaskWithActiveLimit(task *model.Task, activeTaskLimit int) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := enforceActiveTaskLimit(tx, task.UserID, activeTaskLimit); err != nil {
			return err
		}
		if err := enforceTaskRequestKeyAvailable(tx, task.UserID, task.RequestKey, ""); err != nil {
			return err
		}
		return taskRequestKeyError(tx.Create(task).Error)
	})
}

func (r *Repository) ActiveTaskForRequestKey(userID string, requestKey string) (*model.Task, error) {
	if strings.TrimSpace(requestKey) == "" {
		return nil, nil
	}
	var task model.Task
	err := r.db.Where("user_id = ? AND request_key = ? AND status IN ?", userID, requestKey, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}).Order("created_at desc").First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &task, err
}

func (r *Repository) RetryTaskWithBilling(userID string, taskID string, order *model.BillingOrder, activeTaskLimit int, retryLog *model.TaskLog) (*model.Task, error) {
	return r.retryTaskWithBilling(userID, taskID, order, activeTaskLimit, retryLog, false)
}

// RetryTaskWithBillingConfirmed 只用于用户已经明确确认“原供应商请求可能仍在计费”的重试。
// 旧订单继续保留给管理员核账；新尝试使用新的订单和任务状态，不能把旧订单静默退款或结算。
func (r *Repository) RetryTaskWithBillingConfirmed(userID string, taskID string, order *model.BillingOrder, activeTaskLimit int, retryLog *model.TaskLog) (*model.Task, error) {
	return r.retryTaskWithBilling(userID, taskID, order, activeTaskLimit, retryLog, true)
}

func (r *Repository) retryTaskWithBilling(userID string, taskID string, order *model.BillingOrder, activeTaskLimit int, retryLog *model.TaskLog, allowBillingReview bool) (*model.Task, error) {
	var task model.Task
	if retryLog == nil || strings.TrimSpace(retryLog.ID) == "" || retryLog.TaskID != taskID || retryLog.UserID != userID {
		return nil, ErrTaskNotRetryable
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.First(&task, "id = ? AND user_id = ?", taskID, userID).Error; err != nil {
			return err
		}
		if task.Status != model.TaskStatusFailed && task.Status != model.TaskStatusCancelled {
			return ErrTaskNotRetryable
		}
		if strings.HasPrefix(task.LeaseOwner, "manual-recovery:") && task.LeaseExpiresAt != nil && task.LeaseExpiresAt.After(now) {
			return ErrTaskProviderRecoveryConflict
		}
		if task.BillingOrderID != "" {
			var pendingCount int64
			if err := tx.Model(&model.BillingOrder{}).
				Where("id = ? AND status IN ?", task.BillingOrderID, []model.BillingStatus{model.BillingStatusReserved, model.BillingStatusRunning, model.BillingStatusUncertain}).
				Count(&pendingCount).Error; err != nil {
				return err
			}
			if pendingCount > 0 && !allowBillingReview {
				return ErrTaskBillingPending
			}
		}
		if err := enforceActiveTaskLimit(tx, userID, activeTaskLimit); err != nil {
			return err
		}
		if err := enforceTaskRequestKeyAvailable(tx, userID, task.RequestKey, task.ID); err != nil {
			return err
		}
		if order != nil {
			if err := reserveBillingOrder(tx, order); err != nil {
				return err
			}
		}
		updates := map[string]any{
			"status": model.TaskStatusQueued, "stage": "等待队列调度", "progress": 5, "error": "", "result_json": "", "delivery_ops_json": "",
			"billing_order_id": "", "provider_request_id": "", "poll_stage": "", "next_poll_at": nil, "lease_owner": "", "lease_expires_at": nil,
			"provider_call_state": model.TaskProviderCallPending, "started_at": nil, "completed_at": nil, "updated_at": time.Now(),
		}
		if order != nil {
			updates["billing_order_id"] = order.ID
		}
		updated := tx.Model(&model.Task{}).
			Where(
				"id = ? AND user_id = ? AND status IN ? AND (lease_owner = '' OR lease_owner NOT LIKE ? OR lease_expires_at IS NULL OR lease_expires_at <= ?)",
				taskID, userID, []model.TaskStatus{model.TaskStatusFailed, model.TaskStatusCancelled}, "manual-recovery:%", now,
			).
			Updates(updates)
		if updated.Error != nil {
			return taskRequestKeyError(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrTaskNotRetryable
		}
		if task.SessionID != "" {
			sessionUpdate := tx.Model(&model.Session{}).
				Where("id = ? AND user_id = ?", task.SessionID, userID).
				Updates(map[string]any{"status": model.SessionStatusActive, "canvas_ops_json": "", "updated_at": now})
			if sessionUpdate.Error != nil {
				return sessionUpdate.Error
			}
			if sessionUpdate.RowsAffected != 1 {
				return ErrTaskSessionUnavailable
			}
		}
		if err := tx.Create(retryLog).Error; err != nil {
			return err
		}
		return tx.First(&task, "id = ? AND user_id = ?", taskID, userID).Error
	})
	return &task, err
}

func enforceActiveTaskLimit(tx *gorm.DB, userID string, activeTaskLimit int) error {
	var count int64
	if err := tx.Model(&model.Task{}).Where("user_id = ? AND status IN ?", userID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}).Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(activeTaskLimit) {
		return ErrActiveTaskLimit
	}
	return nil
}

func (r *Repository) ReserveBillingOrder(order *model.BillingOrder) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return reserveBillingOrder(tx, order)
	})
}

func reserveBillingOrder(tx *gorm.DB, order *model.BillingOrder) error {
	account := model.CreditAccount{UserID: order.UserID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
		return err
	}
	updated := tx.Model(&model.CreditAccount{}).
		Where("user_id = ? AND available_microcredits >= ?", order.UserID, order.AmountMicrocredits).
		Updates(map[string]any{
			"available_microcredits": gorm.Expr("available_microcredits - ?", order.AmountMicrocredits),
			"reserved_microcredits":  gorm.Expr("reserved_microcredits + ?", order.AmountMicrocredits),
			"version":                gorm.Expr("version + 1"),
			"updated_at":             time.Now(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrInsufficientCredits
	}
	if err := tx.First(&account, "user_id = ?", order.UserID).Error; err != nil {
		return err
	}
	if err := tx.Create(order).Error; err != nil {
		return err
	}
	return tx.Create(&model.CreditLedgerEntry{
		ID:                         newRepositoryID(),
		UserID:                     order.UserID,
		Type:                       model.CreditLedgerReserve,
		AvailableDeltaMicrocredits: -order.AmountMicrocredits,
		ReservedDeltaMicrocredits:  order.AmountMicrocredits,
		AvailableAfterMicrocredits: account.AvailableMicrocredits,
		ReservedAfterMicrocredits:  account.ReservedMicrocredits,
		BillingOrderID:             order.ID,
		Model:                      order.Model,
		ChannelID:                  order.ChannelID,
		Scene:                      order.Scene,
	}).Error
}

func (r *Repository) BillingOrder(id string) (*model.BillingOrder, error) {
	var order model.BillingOrder
	if err := r.db.First(&order, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *Repository) BillingOrderForUpdate(id string) (*model.BillingOrder, error) {
	var order model.BillingOrder
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *Repository) BillingOrderByIdempotencyKey(userID string, idempotencyKey string) (*model.BillingOrder, error) {
	var order model.BillingOrder
	if err := r.db.First(&order, "user_id = ? AND idempotency_key = ?", userID, idempotencyKey).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *Repository) BillingOrdersByTaskIDs(userID string, taskIDs []string) (map[string]model.BillingOrder, error) {
	result := make(map[string]model.BillingOrder, len(taskIDs))
	if len(taskIDs) == 0 {
		return result, nil
	}
	var orders []model.BillingOrder
	// 同一任务重试后会保留历史订单，只能按任务当前 billing_order_id 返回状态。
	if err := r.db.Model(&model.BillingOrder{}).
		Joins("JOIN tasks ON tasks.billing_order_id = billing_orders.id AND tasks.id = billing_orders.task_id AND tasks.user_id = billing_orders.user_id").
		Where("tasks.user_id = ? AND tasks.id IN ?", userID, taskIDs).
		Select("billing_orders.*").
		Find(&orders).Error; err != nil {
		return nil, err
	}
	for _, order := range orders {
		if order.TaskID != "" {
			result[order.TaskID] = order
		}
	}
	return result, nil
}

func (r *Repository) AdminBillingOrders(status string, keyword string, limit int, offset int) ([]model.BillingOrder, int64, error) {
	var items []model.BillingOrder
	var total int64
	query := r.db.Model(&model.BillingOrder{})
	if status == "review" {
		query = query.Joins("LEFT JOIN tasks ON tasks.id = billing_orders.task_id").Where(
			"billing_orders.status = ? OR (billing_orders.status = ? AND billing_orders.updated_at < ?) OR (billing_orders.status = ? AND tasks.status IN ?)",
			model.BillingStatusUncertain, model.BillingStatusRunning, time.Now().Add(-40*time.Minute), model.BillingStatusReserved,
			[]model.TaskStatus{model.TaskStatusFailed, model.TaskStatusCancelled},
		)
	} else if status != "" && status != "all" {
		query = query.Where("billing_orders.status = ?", status)
	}
	if value := strings.TrimSpace(keyword); value != "" {
		pattern := "%" + strings.ToLower(value) + "%"
		query = query.Joins("LEFT JOIN users ON users.id = billing_orders.user_id").Where(
			"lower(billing_orders.model) LIKE ? OR lower(billing_orders.scene) LIKE ? OR lower(billing_orders.provider_request_id) LIKE ? OR lower(users.username) LIKE ? OR lower(users.display_name) LIKE ?",
			pattern, pattern, pattern, pattern, pattern,
		)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := query.Select("billing_orders.*").Order("billing_orders.created_at desc").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *Repository) TaskHasSuccessfulBillableCall(taskID string) (bool, error) {
	var count int64
	err := r.db.Model(&model.ApiCallLog{}).
		Where("task_id = ? AND billable = ? AND status = ?", taskID, true, model.ApiCallStatusSucceeded).
		Count(&count).Error
	return count > 0, err
}

func (r *Repository) RecordBillingResolution(id string, actorUserID string, note string) error {
	return r.db.Model(&model.BillingOrder{}).Where("id = ?", id).Updates(map[string]any{
		"resolved_by": actorUserID, "resolution_note": note, "updated_at": time.Now(),
	}).Error
}

func (r *Repository) UpdateBillingProviderRequestID(id string, providerRequestID string) error {
	id = strings.TrimSpace(id)
	providerRequestID = strings.TrimSpace(providerRequestID)
	if id == "" || providerRequestID == "" {
		return nil
	}
	result := r.db.Model(&model.BillingOrder{}).
		Where("id = ? AND (provider_request_id = '' OR provider_request_id IS NULL OR provider_request_id = ?)", id, providerRequestID).
		Updates(map[string]any{
			"provider_request_id": providerRequestID, "updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrBillingStateConflict
	}
	return nil
}

func enforceTaskRequestKeyAvailable(tx *gorm.DB, userID string, requestKey string, excludeTaskID string) error {
	if strings.TrimSpace(requestKey) == "" {
		return nil
	}
	query := tx.Model(&model.Task{}).Where("user_id = ? AND request_key = ? AND status IN ?", userID, requestKey, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning})
	if excludeTaskID != "" {
		query = query.Where("id <> ?", excludeTaskID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrDuplicateActiveTask
	}
	return nil
}

func taskRequestKeyError(err error) error {
	if err == nil {
		return nil
	}
	value := strings.ToLower(err.Error())
	if strings.Contains(value, "idx_tasks_user_probe_request_key") || (strings.Contains(value, "unique constraint") && strings.Contains(value, "tasks.probe_request_key")) {
		return ErrDuplicateProbeRequest
	}
	if strings.Contains(value, "idx_tasks_user_active_request_key") || strings.Contains(value, "idx_tasks_active_system_probe_key") || (strings.Contains(value, "unique constraint") && strings.Contains(value, "tasks.request_key")) {
		return ErrDuplicateActiveTask
	}
	return err
}

func (r *Repository) MarkBillingRunning(id string) error {
	if id == "" {
		return nil
	}
	var order model.BillingOrder
	if err := r.db.Select("id", "status").First(&order, "id = ?", id).Error; err != nil {
		return err
	}
	if order.Status == model.BillingStatusRunning {
		return nil
	}
	now := time.Now()
	result := r.db.Model(&model.BillingOrder{}).
		Where("id = ? AND status = ?", id, model.BillingStatusReserved).
		Updates(map[string]any{"status": model.BillingStatusRunning, "started_at": &now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrBillingStateConflict
	}
	return nil
}

func (r *Repository) MarkBillingUncertain(id string, errorText string) error {
	result := r.db.Model(&model.BillingOrder{}).
		Where("id = ? AND status IN ?", id, []model.BillingStatus{model.BillingStatusReserved, model.BillingStatusRunning, model.BillingStatusUncertain}).
		Updates(map[string]any{"status": model.BillingStatusUncertain, "error": errorText, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var order model.BillingOrder
	if err := r.db.Select("status").First(&order, "id = ?", id).Error; err != nil {
		return err
	}
	if order.Status == model.BillingStatusUncertain {
		return nil
	}
	return ErrBillingStateConflict
}

func (r *Repository) SettleBillingOrder(id string, providerRequestID string) error {
	return r.settleBillingOrder(id, providerRequestID, true)
}

// 自动执行只能结算 reserved/running；uncertain 必须由人工核对或明确的上游任务恢复路径处理。
func (r *Repository) SettleBillingOrderFromExecution(id string, providerRequestID string) error {
	return r.settleBillingOrder(id, providerRequestID, false)
}

func (r *Repository) settleBillingOrder(id string, providerRequestID string, allowUncertain bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var order model.BillingOrder
		if err := tx.First(&order, "id = ?", id).Error; err != nil {
			return err
		}
		if order.Status == model.BillingStatusSettled {
			return nil
		}
		if order.Status == model.BillingStatusRefunded {
			return errors.New("billing order already refunded")
		}
		if order.Status == model.BillingStatusUncertain && !allowUncertain {
			return ErrBillingReviewRequired
		}
		now := time.Now()
		orderUpdates := map[string]any{"status": model.BillingStatusSettled, "settled_at": &now, "updated_at": now}
		if providerRequestID != "" {
			orderUpdates["provider_request_id"] = providerRequestID
		}
		// 先原子抢占订单终态，再动账户余额；并发结算只能有一个事务写入流水。
		allowedStatuses := []model.BillingStatus{model.BillingStatusReserved, model.BillingStatusRunning}
		if allowUncertain {
			allowedStatuses = append(allowedStatuses, model.BillingStatusUncertain)
		}
		transitioned := tx.Model(&model.BillingOrder{}).
			Where("id = ? AND status IN ?", id, allowedStatuses).
			Updates(orderUpdates)
		if transitioned.Error != nil {
			return transitioned.Error
		}
		if transitioned.RowsAffected != 1 {
			var current model.BillingOrder
			if err := tx.Select("status").First(&current, "id = ?", id).Error; err != nil {
				return err
			}
			if current.Status == model.BillingStatusSettled {
				return nil
			}
			if current.Status == model.BillingStatusRefunded {
				return errors.New("billing order already refunded")
			}
			if current.Status == model.BillingStatusUncertain && !allowUncertain {
				return ErrBillingReviewRequired
			}
			return ErrBillingStateConflict
		}
		updated := tx.Model(&model.CreditAccount{}).
			Where("user_id = ? AND reserved_microcredits >= ?", order.UserID, order.AmountMicrocredits).
			Updates(map[string]any{
				"reserved_microcredits": gorm.Expr("reserved_microcredits - ?", order.AmountMicrocredits),
				"version":               gorm.Expr("version + 1"),
				"updated_at":            now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("reserved credit balance is inconsistent")
		}
		var account model.CreditAccount
		if err := tx.First(&account, "user_id = ?", order.UserID).Error; err != nil {
			return err
		}
		return tx.Create(&model.CreditLedgerEntry{
			ID:                         newRepositoryID(),
			UserID:                     order.UserID,
			Type:                       model.CreditLedgerConsume,
			AmountMicrocredits:         -order.AmountMicrocredits,
			ReservedDeltaMicrocredits:  -order.AmountMicrocredits,
			AvailableAfterMicrocredits: account.AvailableMicrocredits,
			ReservedAfterMicrocredits:  account.ReservedMicrocredits,
			BillingOrderID:             order.ID,
			Model:                      order.Model,
			ChannelID:                  order.ChannelID,
			Scene:                      order.Scene,
		}).Error
	})
}

func (r *Repository) RefundBillingOrder(id string, errorText string) error {
	return r.refundBillingOrder(id, errorText, true)
}

// 自动失败处理不能把待核对订单直接退款；是否确实未计费必须由人工或明确恢复路径判断。
func (r *Repository) RefundBillingOrderFromExecution(id string, errorText string) error {
	return r.refundBillingOrder(id, errorText, false)
}

func (r *Repository) refundBillingOrder(id string, errorText string, allowUncertain bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var order model.BillingOrder
		if err := tx.First(&order, "id = ?", id).Error; err != nil {
			return err
		}
		if order.Status == model.BillingStatusRefunded {
			return nil
		}
		if order.Status == model.BillingStatusSettled {
			return errors.New("settled billing order requires a manual refund")
		}
		if order.Status == model.BillingStatusUncertain && !allowUncertain {
			return ErrBillingReviewRequired
		}
		now := time.Now()
		// 退款与结算共享同一终态抢占条件，避免重复归还余额或与结算交叉写账。
		allowedStatuses := []model.BillingStatus{model.BillingStatusReserved, model.BillingStatusRunning}
		if allowUncertain {
			allowedStatuses = append(allowedStatuses, model.BillingStatusUncertain)
		}
		transitioned := tx.Model(&model.BillingOrder{}).
			Where("id = ? AND status IN ?", id, allowedStatuses).
			Updates(map[string]any{"status": model.BillingStatusRefunded, "error": errorText, "refunded_at": &now, "updated_at": now})
		if transitioned.Error != nil {
			return transitioned.Error
		}
		if transitioned.RowsAffected != 1 {
			var current model.BillingOrder
			if err := tx.Select("status").First(&current, "id = ?", id).Error; err != nil {
				return err
			}
			if current.Status == model.BillingStatusRefunded {
				return nil
			}
			if current.Status == model.BillingStatusSettled {
				return errors.New("settled billing order requires a manual refund")
			}
			if current.Status == model.BillingStatusUncertain && !allowUncertain {
				return ErrBillingReviewRequired
			}
			return ErrBillingStateConflict
		}
		updated := tx.Model(&model.CreditAccount{}).
			Where("user_id = ? AND reserved_microcredits >= ?", order.UserID, order.AmountMicrocredits).
			Updates(map[string]any{
				"available_microcredits": gorm.Expr("available_microcredits + ?", order.AmountMicrocredits),
				"reserved_microcredits":  gorm.Expr("reserved_microcredits - ?", order.AmountMicrocredits),
				"version":                gorm.Expr("version + 1"),
				"updated_at":             now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("reserved credit balance is inconsistent")
		}
		var account model.CreditAccount
		if err := tx.First(&account, "user_id = ?", order.UserID).Error; err != nil {
			return err
		}
		return tx.Create(&model.CreditLedgerEntry{
			ID:                         newRepositoryID(),
			UserID:                     order.UserID,
			Type:                       model.CreditLedgerRefund,
			AmountMicrocredits:         order.AmountMicrocredits,
			AvailableDeltaMicrocredits: order.AmountMicrocredits,
			ReservedDeltaMicrocredits:  -order.AmountMicrocredits,
			AvailableAfterMicrocredits: account.AvailableMicrocredits,
			ReservedAfterMicrocredits:  account.ReservedMicrocredits,
			BillingOrderID:             order.ID,
			Model:                      order.Model,
			ChannelID:                  order.ChannelID,
			Scene:                      order.Scene,
			Note:                       errorText,
		}).Error
	})
}

func (r *Repository) AdjustCredits(userID string, actorUserID string, amount int64, note string) (*model.CreditAccount, error) {
	var account model.CreditAccount
	err := r.db.Transaction(func(tx *gorm.DB) error {
		account = model.CreditAccount{UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return err
		}
		updated := tx.Model(&model.CreditAccount{}).
			Where("user_id = ? AND available_microcredits + ? >= 0", userID, amount).
			Updates(map[string]any{
				"available_microcredits": gorm.Expr("available_microcredits + ?", amount),
				"version":                gorm.Expr("version + 1"),
				"updated_at":             time.Now(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrInsufficientCredits
		}
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		entryType := model.CreditLedgerAdminAdjust
		if amount > 0 {
			entryType = model.CreditLedgerAdminGrant
		}
		return tx.Create(&model.CreditLedgerEntry{
			ID:                         newRepositoryID(),
			UserID:                     userID,
			Type:                       entryType,
			AmountMicrocredits:         amount,
			AvailableDeltaMicrocredits: amount,
			AvailableAfterMicrocredits: account.AvailableMicrocredits,
			ReservedAfterMicrocredits:  account.ReservedMicrocredits,
			ActorUserID:                actorUserID,
			Note:                       note,
		}).Error
	})
	return &account, err
}

func (r *Repository) CreateRedeemBatch(batch *model.RedeemBatch, codes []model.RedeemCode) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		return tx.CreateInBatches(&codes, 200).Error
	})
}

func (r *Repository) AdminRedeemBatches(keyword string, validity string, limit int, offset int) ([]model.RedeemBatch, int64, error) {
	var items []model.RedeemBatch
	var total int64
	query := r.db.Model(&model.RedeemBatch{})
	if value := strings.TrimSpace(keyword); value != "" {
		pattern := "%" + strings.ToLower(value) + "%"
		query = query.Where("lower(note) LIKE ? OR CAST(amount_microcredits AS TEXT) LIKE ? OR CAST(count AS TEXT) LIKE ?", pattern, pattern, pattern)
	}
	if validity == "active" {
		query = query.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	} else if validity == "expired" {
		query = query.Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now())
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	now := time.Now()
	listQuery := query.Select(`redeem_batches.id, redeem_batches.amount_microcredits, redeem_batches.count,
		redeem_batches.note, redeem_batches.created_by, redeem_batches.expires_at, redeem_batches.created_at,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'unused' AND (rc.expires_at IS NULL OR rc.expires_at > ?)) AS available_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'redeemed') AS redeemed_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'disabled') AS disabled_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'unused' AND rc.expires_at IS NOT NULL AND rc.expires_at <= ?) AS expired_count`, now, now)
	if err := listQuery.Order("created_at desc").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *Repository) RedeemBatch(id string) (*model.RedeemBatch, error) {
	var batch model.RedeemBatch
	now := time.Now()
	query := r.db.Model(&model.RedeemBatch{}).Select(`redeem_batches.*,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'unused' AND (rc.expires_at IS NULL OR rc.expires_at > ?)) AS available_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'redeemed') AS redeemed_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'disabled') AS disabled_count,
		(SELECT COUNT(*) FROM redeem_codes rc WHERE rc.batch_id = redeem_batches.id AND rc.status = 'unused' AND rc.expires_at IS NOT NULL AND rc.expires_at <= ?) AS expired_count`, now, now)
	if err := query.First(&batch, "redeem_batches.id = ?", id).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *Repository) AdminRedeemCodes(batchID string, status string, limit int, offset int) ([]AdminRedeemCodeRow, int64, error) {
	var items []AdminRedeemCodeRow
	var total int64
	query := r.db.Model(&model.RedeemCode{}).Where("redeem_codes.batch_id = ?", batchID)
	now := time.Now()
	switch status {
	case "available":
		query = query.Where("redeem_codes.status = ? AND (redeem_codes.expires_at IS NULL OR redeem_codes.expires_at > ?)", model.RedeemCodeUnused, now)
	case "redeemed":
		query = query.Where("redeem_codes.status = ?", model.RedeemCodeRedeemed)
	case "disabled":
		query = query.Where("redeem_codes.status = ?", model.RedeemCodeDisabled)
	case "expired":
		query = query.Where("redeem_codes.status = ? AND redeem_codes.expires_at IS NOT NULL AND redeem_codes.expires_at <= ?", model.RedeemCodeUnused, now)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Select("redeem_codes.*, users.username AS redeemed_username, users.display_name AS redeemed_display_name").
		Joins("LEFT JOIN users ON users.id = redeem_codes.redeemed_by").
		Order("redeem_codes.created_at asc, redeem_codes.id asc").Limit(limit).Offset(offset).Scan(&items).Error
	return items, total, err
}

func (r *Repository) RedeemCode(userID string, codeHash string, redeemedIP string) (*model.CreditAccount, error) {
	var account model.CreditAccount
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var code model.RedeemCode
		if err := tx.First(&code, "code_hash = ?", codeHash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRedeemCodeInvalid
			}
			return err
		}
		now := time.Now()
		query := tx.Model(&model.RedeemCode{}).Where("id = ? AND status = ?", code.ID, model.RedeemCodeUnused)
		if code.ExpiresAt != nil {
			query = query.Where("expires_at > ?", now)
		}
		updated := query.Updates(map[string]any{"status": model.RedeemCodeRedeemed, "redeemed_by": userID, "redeemed_at": &now, "redeemed_ip": redeemedIP, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRedeemCodeInvalid
		}
		account = model.CreditAccount{UserID: userID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CreditAccount{}).Where("user_id = ?", userID).Updates(map[string]any{
			"available_microcredits": gorm.Expr("available_microcredits + ?", code.AmountMicrocredits),
			"version":                gorm.Expr("version + 1"),
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
			return err
		}
		return tx.Create(&model.CreditLedgerEntry{
			ID:                         newRepositoryID(),
			UserID:                     userID,
			Type:                       model.CreditLedgerRedeem,
			AmountMicrocredits:         code.AmountMicrocredits,
			AvailableDeltaMicrocredits: code.AmountMicrocredits,
			AvailableAfterMicrocredits: account.AvailableMicrocredits,
			ReservedAfterMicrocredits:  account.ReservedMicrocredits,
			RedeemCodeID:               code.ID,
			Note:                       "兑换码充值",
		}).Error
	})
	return &account, err
}

func newRepositoryID() string {
	return randomRepositorySuffix()
}

func randomRepositorySuffix() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(value[:])
}
