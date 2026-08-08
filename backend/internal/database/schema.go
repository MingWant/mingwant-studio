package database

import (
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// Models 是应用持久化表的唯一清单，服务启动和跨数据库迁移必须共用它。
func Models() []any {
	return []any{
		&model.User{},
		&model.AuthSession{},
		&model.UserIdentity{},
		&model.OAuthState{},
		&model.EmailVerificationCode{},
		&model.ModelChannel{},
		&model.ChannelModel{},
		&model.ApiCallLog{},
		&model.ModelPricing{},
		&model.CreditAccount{},
		&model.CreditLedgerEntry{},
		&model.BillingOrder{},
		&model.RedeemBatch{},
		&model.RedeemCode{},
		&model.AdminAuditEvent{},
		&model.UserDailyActivity{},
		&model.SystemSetting{},
		&model.UserOSSSetting{},
		&model.UserDailyUploadUsage{},
		&model.UserSkillState{},
		&model.Resource{},
		&model.Asset{},
		&model.ProjectAssetLink{},
		&model.ProjectAssetCandidate{},
		&model.AssetVersion{},
		&model.AssetRepresentation{},
		&model.VoiceProfile{},
		&model.CharacterVoiceBinding{},
		&model.Project{},
		&model.ProjectUnit{},
		&model.CanvasUnitLink{},
		&model.Shot{},
		&model.ShotAssetReference{},
		&model.WorkflowTemplateVersion{},
		&model.WorkflowInstance{},
		&model.WorkflowStepInstance{},
		&model.WorkflowStepTask{},
		&model.CanvasProject{},
		&model.CanvasShare{},
		&model.StoryboardPromptTemplate{},
		&model.Announcement{},
		&model.UserAnnouncementRead{},
		&model.ChannelProbeSubmission{},
		&model.Task{},
		&model.Session{},
		&model.Message{},
		&model.TaskLog{},
		&model.SessionFile{},
		&model.Result{},
	}
}

func MigrateSchema(db *gorm.DB) error {
	if err := db.AutoMigrate(Models()...); err != nil {
		return err
	}
	// 逻辑删除后的同名模型允许重新添加，旧唯一索引不能继续覆盖已删除记录。
	if err := db.Exec("DROP INDEX IF EXISTS idx_channel_model_key").Error; err != nil {
		return err
	}
	if err := db.Exec("DROP INDEX IF EXISTS idx_users_email").Error; err != nil {
		return err
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_nonempty ON users(lower(email)) WHERE email <> ''").Error; err != nil {
		return err
	}
	// 浏览器可在响应丢失后安全重发创建请求；同一用户的请求键永久指向原影视会话。
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_user_request_key ON sessions(user_id, request_key) WHERE request_key <> ''").Error; err != nil {
		return err
	}
	// 同一用户的同一画布操作在排队/运行阶段只能存在一条；终态历史继续保留同一请求键。
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_user_active_request_key ON tasks(user_id, request_key) WHERE request_key <> '' AND status IN ('queued', 'running')").Error; err != nil {
		return err
	}
	// 系统模型测活会产生真实供应商费用；文本测活和工具诊断共用同一请求键，
	// 兼容早期工具键前缀时也要纳入全局唯一约束，避免跨管理员并发调用供应商。
	if err := db.Exec("DROP INDEX IF EXISTS idx_tasks_active_system_probe_key").Error; err != nil {
		return err
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_active_system_probe_key ON tasks(request_key) WHERE type = 'channel_health_probe' AND (request_key LIKE 'probe-system-%' OR request_key LIKE 'probe-tool-system-%') AND status IN ('queued', 'running')").Error; err != nil {
		return err
	}
	// 同一次浏览器提交即使响应丢失后任务已终结，也只能取回原探针，不能变成第二次供应商调用。
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_user_probe_request_key ON tasks(user_id, probe_request_key) WHERE type = 'channel_health_probe' AND probe_request_key <> ''").Error; err != nil {
		return err
	}
	// 供应商成功后的媒体写入可能跨越 Worker 恢复；同一计费尝试、同一结果路径只能有一个可用资源。
	return db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_resources_task_output ON resources(user_id, source_task_id, source_attempt, source_path) WHERE source_task_id <> '' AND source_attempt <> '' AND status IN ('pending', 'ready')").Error
}
