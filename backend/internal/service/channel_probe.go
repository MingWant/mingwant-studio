package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/gorm"
)

const channelProbeTaskType = "channel_health_probe"
const channelProbeMaxAge = 7 * 24 * time.Hour
const channelProbeVerifierVersion = "sse-progress-v3"
const channelProbeMaxOutputTokens = 256
const systemChannelProbeRequestKeyPrefix = "probe-system-"
const userChannelProbeRequestKeyPrefix = "probe-user-"
const systemChannelProbeToolRequestKeyPrefix = "probe-tool-system-"
const userChannelProbeToolRequestKeyPrefix = "probe-tool-user-"

type ChannelProbeRequest struct {
	RequestKey    string `json:"requestKey"`
	Kind          string `json:"kind,omitempty"`
	ChannelID     string `json:"channelId"`
	BaseURL       string `json:"baseUrl"`
	APIKey        string `json:"apiKey"`
	APIFormat     string `json:"apiFormat"`
	InterfaceType string `json:"interfaceType"`
	Model         string `json:"model"`
}

type ChannelProbeResult struct {
	OK                 bool      `json:"ok"`
	Transport          string    `json:"transport"`
	DurationMs         int64     `json:"durationMs"`
	FirstByteMs        int64     `json:"firstByteMs,omitempty"`
	DeliverySpanMs     int64     `json:"deliverySpanMs,omitempty"`
	LongestChunkWaitMs int64     `json:"longestChunkWaitMs,omitempty"`
	TotalChunkWaitMs   int64     `json:"totalChunkWaitMs,omitempty"`
	StreamReadCount    int       `json:"streamReadCount,omitempty"`
	Progressive        bool      `json:"progressive,omitempty"`
	ResponsePreview    string    `json:"responsePreview,omitempty"`
	ToolCalling        string    `json:"toolCalling,omitempty"`
	ToolName           string    `json:"toolName,omitempty"`
	CheckedAt          time.Time `json:"checkedAt"`
	VerifierVersion    string    `json:"verifierVersion"`
}

type ChannelProbeStatus struct {
	ID          string              `json:"id"`
	Reused      bool                `json:"reused,omitempty"`
	Kind        string              `json:"kind"`
	Status      model.TaskStatus    `json:"status"`
	Stage       string              `json:"stage"`
	Progress    int                 `json:"progress"`
	Model       string              `json:"model"`
	Protocol    string              `json:"protocol"`
	Error       string              `json:"error,omitempty"`
	Result      *ChannelProbeResult `json:"result,omitempty"`
	StartedAt   *time.Time          `json:"startedAt,omitempty"`
	CompletedAt *time.Time          `json:"completedAt,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

type channelProbeTaskInput struct {
	Kind             string         `json:"kind"`
	Mode             string         `json:"mode"`
	Prompt           string         `json:"prompt"`
	VerificationCode string         `json:"verificationCode"`
	// 系统渠道任务不保存明文密钥，但要保存提交时的不可逆配置指纹；
	// 排队期间管理员换密钥/协议/模型后，旧任务不得替新配置写入测活绿灯。
	ConfigHash       string         `json:"configHash,omitempty"`
	Config           providerConfig `json:"config"`
}

type channelProbePayload struct {
	Site             string `json:"site"`
	Checked          int    `json:"checked"`
	Normal           int    `json:"normal"`
	Replace          int    `json:"replace"`
	VerificationCode string `json:"verificationCode"`
}

// CreateChannelProbe 只创建固定、低成本的信息抽取任务，禁止把测活入口变成任意提示词的免计费代理。
func (s *Service) CreateChannelProbe(ctx context.Context, actor *model.User, req ChannelProbeRequest) (*ChannelProbeStatus, error) {
	if actor == nil || strings.TrimSpace(actor.ID) == "" {
		return nil, Unauthorized("请先登录")
	}
	kind, err := normalizeChannelProbeKind(req.Kind)
	if err != nil {
		return nil, err
	}
	submissionKey, err := normalizeChannelProbeSubmissionKey(req.RequestKey)
	if err != nil {
		return nil, err
	}
	config, err := s.channelProbeConfig(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	requestKey := channelProbeRequestKeyForKind(config, kind)
	submitted, err := s.repo.ChannelProbeForSubmissionKey(actor.ID, submissionKey)
	if err != nil {
		return nil, err
	}
	if submitted != nil {
		if submitted.RequestKey != requestKey {
			return nil, BadAuthRequest("测活提交键已用于另一项渠道配置；请先核对原任务，再重新点击测活")
		}
		if kind == "text" && channelProbeTaskKind(*submitted) == "tool" {
			return nil, &AuthError{Status: 409, Message: "该提交键绑定的是工具诊断，不是文本测活；请重新点击文本测活，本次未调用供应商"}
		}
		s.logTaskEventBestEffort(submitted.UserID, submitted.ID, "info", "测活提交响应已安全重放", "同一提交键只返回原任务，本次未创建新的供应商调用")
		return reusedChannelProbeStatus(*submitted), nil
	}
	existing, err := s.activeChannelProbeTask(actor.ID, requestKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if kind == "text" && channelProbeTaskKind(*existing) == "tool" {
			// 文本与工具诊断共用活动互斥键，但工具结果不能冒充文本测活绿灯；
			// 否则另一标签页的“重新测活”会把 toolProbe 写成普通文本能力。
			return nil, &AuthError{Status: 409, Message: "当前渠道的工具诊断仍在执行；请等待诊断结束后再重新测活，本次未调用供应商"}
		}
		if !channelProbeTaskUsesConfig(*existing, config) {
			return nil, &AuthError{Status: 409, Message: "当前渠道已有使用旧配置的测活任务正在执行；请等待该任务结束后再使用当前配置测活"}
		}
		return s.bindAndReuseChannelProbeTask(actor.ID, submissionKey, existing.RequestKey, *existing, "重复测活请求已复用原任务", "相同系统模型或自定义渠道配置仍在排队或运行，本次未创建新的供应商调用")
	}
	verificationCode := strings.ReplaceAll(newID(), "-", "")
	if len(verificationCode) > 12 {
		verificationCode = verificationCode[:12]
	}
	taskConfig := config
	if taskConfig.ChannelID != "" {
		// 系统渠道密钥按执行时的最新配置读取，避免在排队任务中复制管理员密钥，也让删除渠道立即阻止未执行探针。
		taskConfig.BaseURL = ""
		taskConfig.APIKey = "system"
	}
	input := map[string]any{
		"kind":             kind,
		"mode":             "text",
		"prompt":           channelProbePrompt(verificationCode),
		"verificationCode": verificationCode,
		"configHash":       channelProbeConfigHash(config),
		"config": map[string]any{
			"channelId":     taskConfig.ChannelID,
			"apiFormat":     taskConfig.APIFormat,
			"interfaceType": taskConfig.InterfaceType,
			"baseUrl":       taskConfig.BaseURL,
			"apiKey":        taskConfig.APIKey,
			"model":         taskConfig.Model,
		},
	}
	if kind == "tool" {
		input["prompt"] = channelProbeToolPrompt(verificationCode)
	}
	if err := s.protectTaskSecrets(input); err != nil {
		return nil, errors.Join(&AuthError{Status: 500, Message: "测活任务无法安全保存，本次未调用供应商"}, fmt.Errorf("protect channel probe secrets: %w", err))
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, errors.Join(&AuthError{Status: 500, Message: "测活任务无法安全保存，本次未调用供应商"}, fmt.Errorf("serialize channel probe input: %w", err))
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	task := model.Task{
		ID:              newID(),
		UserID:          actor.ID,
		Type:            channelProbeTaskType,
		Status:          model.TaskStatusQueued,
		Stage:           "等待测活任务调度",
		Progress:        5,
		Prompt:          "渠道测活：固定运维记录信息抽取",
		Operation:       "channel_health_check",
		Provider:        config.InterfaceType,
		Model:           config.Model,
		RequestKey:      requestKey,
		ProbeRequestKey: submissionKey,
		InputJSON:       string(encoded),
	}
	if kind == "tool" {
		task.Prompt = "渠道测活：固定工具调用诊断"
		task.Operation = "channel_tool_check"
	}
	if err := s.createTaskWithinStorageQuota(&task, nil, policy); err != nil {
		if errors.Is(err, repository.ErrDuplicateProbeRequest) {
			submitted, lookupErr := s.repo.ChannelProbeForSubmissionKey(actor.ID, submissionKey)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if submitted != nil && submitted.RequestKey == requestKey {
				s.logTaskEventBestEffort(submitted.UserID, submitted.ID, "info", "并发测活提交已安全重放", "数据库永久唯一约束返回原任务，本次未创建新的供应商调用")
				return reusedChannelProbeStatus(*submitted), nil
			}
			return nil, &AuthError{Status: 409, Message: "测活提交键已被另一项请求占用，本次未创建新的供应商调用；请先核对原任务"}
		}
		// 活动上限与唯一索引都可能在并发窗口先返回；重新读取可以安全接管刚创建的原任务。
		if errors.Is(err, repository.ErrDuplicateActiveTask) || errors.Is(err, repository.ErrActiveTaskLimit) {
			existing, lookupErr := s.activeChannelProbeTask(actor.ID, requestKey)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if existing != nil {
				if kind == "text" && channelProbeTaskKind(*existing) == "tool" {
					return nil, &AuthError{Status: 409, Message: "当前渠道的工具诊断仍在执行；请等待诊断结束后再重新测活，本次未调用供应商"}
				}
				if !channelProbeTaskUsesConfig(*existing, config) {
					return nil, &AuthError{Status: 409, Message: "当前渠道已有使用旧配置的测活任务正在执行；请等待该任务结束后再使用当前配置测活"}
				}
				return s.bindAndReuseChannelProbeTask(actor.ID, submissionKey, requestKey, *existing, "并发测活请求已复用原任务", "数据库唯一约束阻止了第二个任务和供应商调用")
			}
		}
		if errors.Is(err, repository.ErrDuplicateActiveTask) {
			return nil, &AuthError{Status: 409, Message: "相同配置的测活任务刚刚发生状态变化，本次未创建新的供应商调用；请先刷新任务状态"}
		}
		if errors.Is(err, repository.ErrActiveTaskLimit) {
			return nil, BadAuthRequest(fmt.Sprintf("同时排队或运行的任务最多 %d 个，请等待已有任务完成", policy.Task.ActiveTaskLimit))
		}
		return nil, err
	}
	queueDetail := "固定信息抽取，不扣平台积分"
	if kind == "tool" {
		queueDetail = "固定无副作用工具调用诊断，不扣平台积分"
	}
	s.logTaskEventBestEffort(actor.ID, task.ID, "info", "渠道测活已进入后台队列", queueDetail)
	return channelProbeStatusFromTask(task), nil
}

func (s *Service) ChannelProbe(actor *model.User, id string) (*ChannelProbeStatus, error) {
	if actor == nil || strings.TrimSpace(actor.ID) == "" {
		return nil, Unauthorized("请先登录")
	}
	task, err := s.repo.TaskForUser(actor.ID, id)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if actor.Role != model.UserRoleAdmin {
			return nil, BadAuthRequest("测活任务不存在")
		}
		// 系统模型探针跨管理员全局去重；接管者只能读取固定系统探针，不能借任务 ID 读取其他人的自定义密钥任务。
		task, err = s.repo.Task(id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, BadAuthRequest("测活任务不存在")
			}
			return nil, err
		}
		if !isSystemChannelProbeTask(*task) {
			return nil, BadAuthRequest("测活任务不存在")
		}
	}
	if task.Type != channelProbeTaskType {
		return nil, BadAuthRequest("该任务不是渠道测活任务")
	}
	return channelProbeStatusFromTask(*task), nil
}

func (s *Service) activeChannelProbeTask(userID string, requestKey string) (*model.Task, error) {
	if isSystemChannelProbeRequestKey(requestKey) {
		return s.repo.ActiveTaskForGlobalRequestKey(channelProbeTaskType, requestKey)
	}
	return s.repo.ActiveTaskForRequestKey(userID, requestKey)
}

func channelProbeTaskUsesConfig(task model.Task, config providerConfig) bool {
	var input channelProbeTaskInput
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return false
	}
	// 活动探针不能只按渠道/模型复用；管理员修改端点、协议或密钥后，旧任务
	// 仍可能在后台执行，必须让当前请求等待旧任务结束而不能把旧结论冒充新配置。
	return strings.TrimSpace(input.ConfigHash) != "" && input.ConfigHash == channelProbeConfigHash(config)
}

func normalizeChannelProbeKind(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "text":
		return "text", nil
	case "tool":
		return "tool", nil
	default:
		return "", BadAuthRequest("测活类型无效，请重新打开渠道测活")
	}
}

func isSystemChannelProbeRequestKey(requestKey string) bool {
	return strings.HasPrefix(requestKey, systemChannelProbeRequestKeyPrefix) || strings.HasPrefix(requestKey, systemChannelProbeToolRequestKeyPrefix)
}

func normalizeChannelProbeSubmissionKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 64 || !sessionRequestKeyPattern.MatchString(value) {
		return "", BadAuthRequest("测活提交键格式无效，请刷新页面后重试")
	}
	return value, nil
}

func reusedChannelProbeStatus(task model.Task) *ChannelProbeStatus {
	status := channelProbeStatusFromTask(task)
	status.Reused = true
	return status
}

// 活动探针可能属于另一个标签页或管理员；先绑定当前提交键，响应丢失后的重发才能永久回到同一任务。
func (s *Service) bindAndReuseChannelProbeTask(userID string, submissionKey string, requestKey string, task model.Task, event string, detail string) (*ChannelProbeStatus, error) {
	if err := s.repo.BindChannelProbeSubmission(userID, submissionKey, requestKey, task.ID); err != nil {
		if !errors.Is(err, repository.ErrDuplicateProbeRequest) {
			return nil, err
		}
		submitted, lookupErr := s.repo.ChannelProbeForSubmissionKey(userID, submissionKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if submitted == nil || submitted.RequestKey != requestKey {
			return nil, &AuthError{Status: 409, Message: "测活提交键已被另一项请求占用，本次未创建新的供应商调用；请先核对原任务"}
		}
		task = *submitted
	}
	s.logTaskEventBestEffort(task.UserID, task.ID, "info", event, detail)
	return reusedChannelProbeStatus(task), nil
}

func isSystemChannelProbeTask(task model.Task) bool {
	if task.Type != channelProbeTaskType || !isSystemChannelProbeRequestKey(task.RequestKey) {
		return false
	}
	var input channelProbeTaskInput
	return json.Unmarshal([]byte(task.InputJSON), &input) == nil && strings.TrimSpace(input.Config.ChannelID) != ""
}

func channelProbeStatusFromTask(task model.Task) *ChannelProbeStatus {
	redactSystemChannelProbeOutput(&task)
	kind := channelProbeTaskKind(task)
	status := &ChannelProbeStatus{
		ID: task.ID, Status: task.Status, Stage: task.Stage, Progress: task.Progress,
		Kind: kind, Model: task.Model, Protocol: task.Provider, Error: truncateRunes(task.Error, 800),
		StartedAt: task.StartedAt, CompletedAt: task.CompletedAt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
	if task.Status != model.TaskStatusSucceeded || strings.TrimSpace(task.ResultJSON) == "" {
		return status
	}
	var payload struct {
		Probe     *ChannelProbeResult `json:"probe"`
		ToolProbe *ChannelProbeResult `json:"toolProbe"`
	}
	if json.Unmarshal([]byte(task.ResultJSON), &payload) == nil {
		if payload.Probe != nil && payload.Probe.OK {
			status.Result = payload.Probe
		}
		if payload.ToolProbe != nil && payload.ToolProbe.OK {
			status.Result = payload.ToolProbe
		}
	}
	return status
}

func channelProbeTaskKind(task model.Task) string {
	var input channelProbeTaskInput
	if json.Unmarshal([]byte(task.InputJSON), &input) == nil {
		if kind, err := normalizeChannelProbeKind(input.Kind); err == nil {
			return kind
		}
	}
	return "text"
}

func (s *Service) channelProbeConfig(ctx context.Context, actor *model.User, req ChannelProbeRequest) (providerConfig, error) {
	modelName := strings.TrimPrefix(strings.TrimSpace(req.Model), "models/")
	if modelName == "" {
		return providerConfig{}, BadAuthRequest("请选择要测活的文本模型")
	}
	if channelID := strings.TrimSpace(req.ChannelID); channelID != "" {
		if err := s.RequireAdmin(actor); err != nil {
			return providerConfig{}, err
		}
		return s.systemChannelProbeConfig(ctx, channelID, modelName)
	}

	protocol := model.ChannelInterfaceType(strings.TrimSpace(req.InterfaceType))
	if capabilityForProtocol(protocol) != "text" {
		return providerConfig{}, BadAuthRequest("请选择 Chat Completions、OpenAI Responses 或 Gemini GenerateContent 文本协议")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(req.APIKey) == "" {
		return providerConfig{}, BadAuthRequest("自定义渠道缺少 Base URL 或 API Key")
	}
	expectedFormat := apiFormatForProtocol(protocol, "")
	if format := strings.ToLower(strings.TrimSpace(req.APIFormat)); format != "" && format != expectedFormat {
		return providerConfig{}, BadAuthRequest("文本协议与渠道调用格式不匹配，请重新选择模型协议")
	}
	// 自定义测活最终可能被在线 Agent 送入 /api/ai/custom 中转；该入口只允许
	// HTTPS。这里必须使用同一校验，避免测活先按通用出站规则放行 HTTP，随后
	// 创作台才在真正请求前拒绝，给用户造成“测活成功但创作失败”的假绿灯。
	if _, err := ValidateCustomRelayURLContext(ctx, baseURL); err != nil {
		return providerConfig{}, err
	}
	return providerConfig{APIFormat: expectedFormat, InterfaceType: string(protocol), BaseURL: baseURL, APIKey: strings.TrimSpace(req.APIKey), Model: modelName}, nil
}

func (s *Service) systemChannelProbeConfig(ctx context.Context, channelID string, modelName string) (providerConfig, error) {
	channel, err := s.repo.AdminSystemChannel(strings.TrimSpace(channelID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return providerConfig{}, BadAuthRequest("系统渠道不存在或已删除")
		}
		return providerConfig{}, err
	}
	item, err := s.repo.ChannelModelByKeyIncludingDisabled(channel.ID, modelName)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return providerConfig{}, BadAuthRequest("系统渠道中不存在该模型")
		}
		return providerConfig{}, err
	}
	protocol, protocolErr := resolveSystemTextModelProtocol(*channel, *item)
	if protocolErr != nil {
		return providerConfig{}, BadAuthRequest("测活只支持文本模型及其对应协议")
	}
	if strings.TrimSpace(channel.BaseURL) == "" || strings.TrimSpace(channel.APIKey) == "" {
		return providerConfig{}, BadAuthRequest("系统渠道缺少 Base URL 或 API Key")
	}
	if _, err := ValidateOutboundURLContext(ctx, channel.BaseURL); err != nil {
		return providerConfig{}, err
	}
	return providerConfig{ChannelID: channel.ID, APIFormat: apiFormatForProtocol(protocol, channel.APIFormat), InterfaceType: string(protocol), BaseURL: channel.BaseURL, APIKey: channel.APIKey, Model: item.ModelKey}, nil
}

func (s *Service) processChannelProbeTask(ctx context.Context, task model.Task) (map[string]interface{}, error) {
	var probe channelProbeTaskInput
	if err := json.Unmarshal([]byte(task.InputJSON), &probe); err != nil {
		return nil, fmt.Errorf("测活任务输入解析失败：%w", err)
	}
	kind, err := normalizeChannelProbeKind(probe.Kind)
	if err != nil {
		return nil, err
	}
	if probe.Mode != "text" || strings.TrimSpace(probe.Prompt) == "" || strings.TrimSpace(probe.VerificationCode) == "" {
		return nil, errors.New("测活任务输入不完整")
	}
	if probe.Config.ChannelID != "" {
		resolved, err := s.systemChannelProbeConfig(ctx, probe.Config.ChannelID, probe.Config.Model)
		if err != nil {
			return nil, markProviderPreparationFailure(err)
		}
		probe.Config = resolved
		if strings.TrimSpace(probe.ConfigHash) == "" || probe.ConfigHash != channelProbeConfigHash(probe.Config) {
			return nil, markProviderPreparationFailure(errors.New("系统渠道配置在测活排队期间已变化，请重新测活"))
		}
	}
	if strings.TrimSpace(probe.Config.BaseURL) == "" || strings.TrimSpace(probe.Config.APIKey) == "" || strings.TrimSpace(probe.Config.Model) == "" {
		return nil, markProviderPreparationFailure(errors.New("测活任务缺少 Base URL、API Key 或模型名"))
	}
	if err := validateGenerationInterface("text", probe.Config.InterfaceType); err != nil {
		return nil, markProviderPreparationFailure(err)
	}
	if probe.Config.ChannelID == "" {
		if _, err := ValidateCustomRelayURLContext(ctx, probe.Config.BaseURL); err != nil {
			return nil, markProviderPreparationFailure(err)
		}
	} else if _, err := ValidateOutboundURLContext(ctx, probe.Config.BaseURL); err != nil {
		return nil, markProviderPreparationFailure(err)
	}
	startedAt := time.Now()
	configHash := channelProbeConfigHash(probe.Config)
	ctx = withProviderRequestKind(ctx, "health_check")
	if kind == "tool" {
		result, err := runChannelToolProbeTask(ctx, canvasGenerationInput{Mode: "text", Prompt: probe.Prompt, Config: probe.Config, MaxOutputTokens: channelProbeToolMaxOutputTokens})
		if err != nil {
			return nil, s.recordChannelProbeFailureForKindAt(probe.Config, kind, time.Since(startedAt).Milliseconds(), err, startedAt, configHash)
		}
		toolProbe, ok := result["toolProbe"].(ChannelProbeResult)
		if !ok || !toolProbe.OK {
			return nil, s.recordChannelProbeFailureForKindAt(probe.Config, kind, time.Since(startedAt).Milliseconds(), errors.New("工具诊断没有返回可校验结果"), startedAt, configHash)
		}
		toolProbe.DurationMs = time.Since(startedAt).Milliseconds()
		toolProbe.CheckedAt = time.Now()
		if err := s.recordSystemChannelToolProbeAt(probe.Config, "succeeded", toolProbe.ToolCalling, toolProbe.DurationMs, startedAt, configHash); err != nil {
			return nil, fmt.Errorf("工具诊断已完成，但保存系统渠道能力状态失败：%w", err)
		}
		return map[string]interface{}{"mode": "text", "toolProbe": toolProbe}, nil
	}
	// 测活只验证一次固定信息抽取与真实传输方式；硬上限防止推理模型把探针扩写成数千 token 的长回答。
	result, err := runTextTask(ctx, canvasGenerationInput{Mode: "text", Prompt: probe.Prompt, Config: probe.Config, MaxOutputTokens: channelProbeMaxOutputTokens})
	if err != nil {
		return nil, s.recordChannelProbeFailureAt(probe.Config, time.Since(startedAt).Milliseconds(), err, startedAt, configHash)
	}
	text, _ := result["text"].(string)
	preview := truncateRunes(strings.TrimSpace(text), 320)
	if err := validateChannelProbeResponse(text, probe.VerificationCode); err != nil {
		failureMessage := fmt.Sprintf("上游已响应，但测活校验未通过：%v", err)
		if probe.Config.ChannelID == "" {
			failureMessage += "；响应摘要：" + defaultString(preview, "空响应")
		}
		failure := errors.New(failureMessage)
		return nil, s.recordChannelProbeFailureAt(probe.Config, time.Since(startedAt).Milliseconds(), failure, startedAt, configHash)
	}
	transport, _ := result["transport"].(string)
	delivery, _ := result["streamDelivery"].(providerStreamDelivery)
	durationMs := time.Since(startedAt).Milliseconds()
	publicPreview := preview
	if probe.Config.ChannelID != "" {
		// 系统渠道只公开能力结论；固定探针正文也不进入持久任务结果。
		publicPreview = ""
	}
	if err := s.recordSystemChannelProbeAt(probe.Config, "succeeded", defaultString(transport, "non-stream"), durationMs, startedAt, configHash); err != nil {
		return nil, fmt.Errorf("模型测活成功，但保存流式能力状态失败：%w", err)
	}
	return map[string]interface{}{
		"mode": "text",
		"probe": ChannelProbeResult{
			OK: true, Transport: defaultString(transport, "non-stream"), DurationMs: durationMs,
			FirstByteMs: delivery.FirstByteMs, DeliverySpanMs: delivery.DeliverySpanMs, LongestChunkWaitMs: delivery.LongestReadWaitMs, TotalChunkWaitMs: delivery.TotalFollowupWaitMs,
			StreamReadCount: delivery.ReadCount, Progressive: delivery.Progressive,
			ResponsePreview: publicPreview, CheckedAt: time.Now(), VerifierVersion: channelProbeVerifierVersion,
		},
	}, nil
}

// 升级前任务可能已经保存系统探针摘要；所有公开任务与测活响应都在返回前再次剥离。
func redactSystemChannelProbeOutput(task *model.Task) {
	if task == nil || !isSystemChannelProbeTask(*task) {
		return
	}
	task.Error = redactSystemChannelProbeText(task.Error)
	if strings.TrimSpace(task.ResultJSON) == "" {
		return
	}
	var payload map[string]any
	if json.Unmarshal([]byte(task.ResultJSON), &payload) != nil {
		task.ResultJSON = ""
		return
	}
	probe, ok := payload["probe"].(map[string]any)
	if !ok {
		probe, ok = payload["toolProbe"].(map[string]any)
	}
	if !ok {
		task.ResultJSON = ""
		return
	}
	delete(probe, "responsePreview")
	encoded, err := json.Marshal(payload)
	if err != nil {
		task.ResultJSON = ""
		return
	}
	task.ResultJSON = string(encoded)
}

func redactSystemChannelProbeText(value string) string {
	if marker := strings.Index(value, "；响应摘要："); marker >= 0 {
		return value[:marker]
	}
	return value
}

func (s *Service) recordChannelProbeFailure(config providerConfig, durationMs int64, cause error) error {
	if err := s.recordSystemChannelProbe(config, "failed", "", durationMs); err != nil {
		return fmt.Errorf("%v；保存测活失败状态时出错：%w", cause, err)
	}
	return cause
}

func (s *Service) recordChannelProbeFailureAt(config providerConfig, durationMs int64, cause error, startedAt time.Time, configHash string) error {
	if err := s.recordSystemChannelProbeAt(config, "failed", "", durationMs, startedAt, configHash); err != nil {
		return fmt.Errorf("%v；保存测活失败状态时出错：%w", cause, err)
	}
	return cause
}

func (s *Service) recordChannelProbeFailureForKind(config providerConfig, kind string, durationMs int64, cause error) error {
	if kind == "tool" {
		// 工具诊断是能力分支，不得把失败误写成系统文本链路失败；
		// 但系统渠道仍要把失败状态共享给普通用户，避免他们看到文本绿灯后再次踩同一个工具错误。
		if err := s.recordSystemChannelToolProbe(config, "failed", "", durationMs); err != nil {
			return fmt.Errorf("%v；保存工具诊断失败状态时出错：%w", cause, err)
		}
		return cause
	}
	return s.recordChannelProbeFailure(config, durationMs, cause)
}

func (s *Service) recordChannelProbeFailureForKindAt(config providerConfig, kind string, durationMs int64, cause error, startedAt time.Time, configHash string) error {
	if kind == "tool" {
		if err := s.recordSystemChannelToolProbeAt(config, "failed", "", durationMs, startedAt, configHash); err != nil {
			return fmt.Errorf("%v；保存工具诊断失败状态时出错：%w", cause, err)
		}
		return cause
	}
	return s.recordChannelProbeFailureAt(config, durationMs, cause, startedAt, configHash)
}

func (s *Service) recordSystemChannelProbe(config providerConfig, status string, transport string, durationMs int64) error {
	if strings.TrimSpace(config.ChannelID) == "" {
		return nil
	}
	if durationMs < 0 {
		durationMs = 0
	}
	now := time.Now()
	startedAt := now.Add(-time.Duration(durationMs) * time.Millisecond)
	return s.repo.RecordChannelModelProbe(config.ChannelID, strings.TrimPrefix(config.Model, "models/"), status, transport, durationMs, channelProbeConfigHash(config), startedAt, now)
}

func (s *Service) recordSystemChannelProbeAt(config providerConfig, status string, transport string, durationMs int64, startedAt time.Time, configHash string) error {
	if strings.TrimSpace(config.ChannelID) == "" {
		return nil
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if startedAt.IsZero() {
		startedAt = time.Now().Add(-time.Duration(durationMs) * time.Millisecond)
	}
	if strings.TrimSpace(configHash) == "" {
		configHash = channelProbeConfigHash(config)
	}
	if currentHash, err := s.SystemTextTransportObservationKey(config.ChannelID, config.Model); err != nil {
		return err
	} else if currentHash != configHash {
		return errors.New("系统渠道配置在测活请求执行期间已变化，结果未登记；请重新测活")
	}
	return s.repo.RecordChannelModelProbe(config.ChannelID, strings.TrimPrefix(config.Model, "models/"), status, transport, durationMs, configHash, startedAt, time.Now())
}

func (s *Service) recordSystemChannelToolProbe(config providerConfig, status string, toolCalling string, durationMs int64) error {
	if strings.TrimSpace(config.ChannelID) == "" {
		return nil
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if status != "succeeded" {
		toolCalling = ""
	}
	if status == "succeeded" && toolCalling != "supported" {
		status = "failed"
	}
	now := time.Now()
	startedAt := now.Add(-time.Duration(durationMs) * time.Millisecond)
	return s.repo.RecordChannelModelToolProbe(config.ChannelID, strings.TrimPrefix(config.Model, "models/"), status, channelProbeVerifierVersion, channelProbeConfigHash(config), startedAt, now)
}

func (s *Service) recordSystemChannelToolProbeAt(config providerConfig, status string, toolCalling string, durationMs int64, startedAt time.Time, configHash string) error {
	if strings.TrimSpace(config.ChannelID) == "" {
		return nil
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if status != "succeeded" {
		toolCalling = ""
	}
	if status == "succeeded" && toolCalling != "supported" {
		status = "failed"
	}
	if startedAt.IsZero() {
		startedAt = time.Now().Add(-time.Duration(durationMs) * time.Millisecond)
	}
	if strings.TrimSpace(configHash) == "" {
		configHash = channelProbeConfigHash(config)
	}
	if currentHash, err := s.SystemTextTransportObservationKey(config.ChannelID, config.Model); err != nil {
		return err
	} else if currentHash != configHash {
		return errors.New("系统渠道配置在工具诊断执行期间已变化，结果未登记；请重新测活")
	}
	return s.repo.RecordChannelModelToolProbe(config.ChannelID, strings.TrimPrefix(config.Model, "models/"), status, channelProbeVerifierVersion, configHash, startedAt, time.Now())
}

// 请求开始时冻结配置指纹；迟到结果即使遇到管理员换密钥，也只能保存为与旧配置绑定的结论。
func (s *Service) SystemTextTransportObservationKey(channelID string, modelName string) (string, error) {
	channelID = strings.TrimSpace(channelID)
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if channelID == "" || modelName == "" {
		return "", errors.New("记录系统模型流式状态缺少渠道或模型")
	}
	channel, err := s.repo.SystemChannel(channelID)
	if err != nil {
		return "", err
	}
	item, err := s.repo.ChannelModelByKeyIncludingDisabled(channelID, modelName)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(item.Capability), "text") {
		return "", errors.New("只有文本模型可以记录流式状态")
	}
	protocol, err := resolveSystemTextModelProtocol(*channel, *item)
	if err != nil {
		return "", err
	}
	config := providerConfig{
		ChannelID:     channel.ID,
		BaseURL:       channel.BaseURL,
		APIKey:        channel.APIKey,
		APIFormat:     apiFormatForProtocol(protocol, channel.APIFormat),
		InterfaceType: string(protocol),
		Model:         item.ModelKey,
	}
	return channelProbeConfigHash(config), nil
}

// 真实在线 Agent 请求也能证明模型的流式能力；只保存协议结论，不保存提示词、响应正文或密钥。
func (s *Service) RecordSystemTextTransportObservation(channelID string, modelName string, configHash string, status string, transport string, startedAt time.Time, durationMs int64) error {
	channelID = strings.TrimSpace(channelID)
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	decodedHash, hashErr := hex.DecodeString(strings.TrimSpace(configHash))
	if channelID == "" || modelName == "" || hashErr != nil || len(decodedHash) != sha256.Size {
		return errors.New("系统模型流式观察标识无效")
	}
	if startedAt.IsZero() {
		return errors.New("系统模型流式观察缺少开始时间")
	}
	if status != "succeeded" && status != "failed" {
		return errors.New("系统模型流式状态无效")
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if status == "succeeded" && strings.TrimSpace(transport) == "" {
		transport = "non-stream-compatible"
	}
	if currentHash, err := s.SystemTextTransportObservationKey(channelID, modelName); err != nil {
		return err
	} else if currentHash != strings.TrimSpace(configHash) {
		return errors.New("系统渠道配置在流式请求执行期间已变化，结果未登记；请重新测活")
	}
	now := time.Now()
	return s.repo.RecordChannelModelProbe(channelID, modelName, status, transport, durationMs, strings.TrimSpace(configHash), startedAt, now)
}

func channelProbeConfigHash(config providerConfig) string {
	apiFormat := strings.ToLower(strings.TrimSpace(config.APIFormat))
	if apiFormat == "" {
		apiFormat = "openai"
	}
	value := strings.Join([]string{
		channelProbeVerifierVersion,
		strings.TrimSpace(config.ChannelID),
		strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		strings.TrimSpace(config.APIKey),
		apiFormat,
		strings.ToLower(strings.TrimSpace(config.InterfaceType)),
		strings.TrimPrefix(strings.TrimSpace(config.Model), "models/"),
	}, "\n")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// 请求键只保存不可逆摘要；文本测活和工具诊断共用活动键，系统模型按渠道与模型全局互斥，
// 自定义渠道按用户和完整请求配置互斥；两种终态的 kind 保存在任务输入而不是键里。
func channelProbeRequestKey(config providerConfig) string {
	return channelProbeRequestKeyForKind(config, "text")
}

func channelProbeRequestKeyForKind(config providerConfig, _ string) string {
	prefix := userChannelProbeRequestKeyPrefix
	parts := []string{
		"custom",
		strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		strings.TrimSpace(config.APIKey),
		strings.ToLower(strings.TrimSpace(config.APIFormat)),
		strings.ToLower(strings.TrimSpace(config.InterfaceType)),
		strings.TrimPrefix(strings.TrimSpace(config.Model), "models/"),
	}
	if strings.TrimSpace(config.ChannelID) != "" {
		prefix = systemChannelProbeRequestKeyPrefix
		parts = []string{"system", strings.TrimSpace(config.ChannelID), strings.TrimPrefix(strings.TrimSpace(config.Model), "models/")}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	digest := hex.EncodeToString(sum[:])
	return prefix + digest[:64-len(prefix)]
}

func channelModelProbeMatches(channel model.ModelChannel, item model.ChannelModel) bool {
	protocol, err := resolveSystemTextModelProtocol(channel, item)
	if err != nil {
		return false
	}
	config := providerConfig{ChannelID: channel.ID, BaseURL: channel.BaseURL, APIKey: channel.APIKey, APIFormat: apiFormatForProtocol(protocol, channel.APIFormat), InterfaceType: string(protocol), Model: item.ModelKey}
	return item.ProbeConfigHash != "" && item.ProbeConfigHash == channelProbeConfigHash(config)
}

// 系统渠道的工具诊断与文本测活绑定同一份服务端配置指纹，普通用户才能读取管理员已经验证的 Function Calling 结论。
func channelModelToolProbeMatches(channel model.ModelChannel, item model.ChannelModel) bool {
	protocol, err := resolveSystemTextModelProtocol(channel, item)
	if err != nil {
		return false
	}
	config := providerConfig{ChannelID: channel.ID, BaseURL: channel.BaseURL, APIKey: channel.APIKey, APIFormat: apiFormatForProtocol(protocol, channel.APIFormat), InterfaceType: string(protocol), Model: item.ModelKey}
	return item.ToolProbeConfigHash != "" && item.ToolProbeConfigHash == channelProbeConfigHash(config)
}

func clearChannelModelProbeState(item *model.ChannelModel) {
	item.ProbeStatus = ""
	item.ProbeTransport = ""
	item.ProbeDurationMs = 0
	item.ProbeCheckedAt = nil
	item.ProbeConfigHash = ""
	item.ToolProbeStatus = ""
	item.ToolProbeCheckedAt = nil
	item.ToolProbeVerifierVersion = ""
	item.ToolProbeConfigHash = ""
}

// 测活是管理员的渠道诊断，不是普通用户的调用授权。长分镜仍需校验模型配置，
// 但不得因为测活失败、过期或尚未测活就阻止用户创建任务；真实调用结果由 Worker
// 按供应商响应、超时和费用状态处理，用户可以在明确确认后重试。
func validateLongTextTaskConfig(taskType string, input map[string]any) error {
	if taskType != "agent_storyboard" && taskType != "agent_storyboard_rows" {
		return nil
	}
	config, ok := input["config"].(map[string]any)
	if !ok {
		return BadAuthRequest("长分镜缺少文本模型配置，系统未创建任务或计费订单")
	}
	modelName, _ := config["model"].(string)
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return BadAuthRequest("长分镜缺少文本模型名称，系统未创建任务或计费订单")
	}
	// channelProbeTaskId/toolProbeTaskId 仍随请求保存，便于管理员审计“调用前是否有诊断”，
	// 但它们不再作为用户调用的必要证明；没有探针、探针失败或探针过期都继续进入正常计费与供应商请求路径。
	return nil
}

func channelProbePrompt(code string) string {
	return fmt.Sprintf(`请完成一次真实、低风险的运维记录信息抽取。这不是闲聊，不要解释过程。

记录：蓝桥社区服务站检查了 3 台应急灯，其中 2 台状态正常，1 台需要更换。

只返回一个 JSON 对象，不要使用 Markdown。字段必须为：
{"site":"蓝桥社区服务站","checked":3,"normal":2,"replace":1,"verificationCode":"%s"}

verificationCode 只用于本次请求校验，必须原样复制。`, code)
}

func channelProbeToolPrompt(code string) string {
	return fmt.Sprintf(`请完成一次真实、低风险的运维记录信息抽取。这不是闲聊，不要输出解释文字。

只调用名为 probe_extract_record 的工具，把下面记录填写为工具参数；该工具没有外部副作用，只用于校验模型的 Function Calling 能力。
记录：蓝桥社区服务站检查了 3 台应急灯，其中 2 台状态正常，1 台需要更换。

verificationCode 必须原样填写为 %s。`, code)
}

func validateChannelProbeResponse(raw string, code string) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(raw[index:]))
		decoder.UseNumber()
		var payload map[string]any
		if decoder.Decode(&payload) != nil {
			continue
		}
		candidate, err := normalizeChannelProbePayload(payload)
		if err != nil {
			continue
		}
		if candidate.VerificationCode != code {
			return errors.New("随机校验码不匹配")
		}
		if candidate.Site != "蓝桥社区服务站" || candidate.Checked != 3 || candidate.Normal != 2 || candidate.Replace != 1 {
			return errors.New("信息抽取结果与记录不一致")
		}
		return nil
	}
	return errors.New("没有找到可校验的 JSON 对象")
}

func normalizeChannelProbePayload(payload map[string]any) (channelProbePayload, error) {
	checked, err := channelProbeInteger(payload["checked"])
	if err != nil {
		return channelProbePayload{}, err
	}
	normal, err := channelProbeInteger(payload["normal"])
	if err != nil {
		return channelProbePayload{}, err
	}
	replace, err := channelProbeInteger(payload["replace"])
	if err != nil {
		return channelProbePayload{}, err
	}
	return channelProbePayload{Site: strings.TrimSpace(fmt.Sprint(payload["site"])), Checked: checked, Normal: normal, Replace: replace, VerificationCode: strings.TrimSpace(fmt.Sprint(payload["verificationCode"]))}, nil
}

func channelProbeInteger(value any) (int, error) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(item.String())
		return parsed, err
	case float64:
		if item != float64(int(item)) {
			return 0, errors.New("数量不是整数")
		}
		return int(item), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(item))
	default:
		return 0, errors.New("缺少数量字段")
	}
}
