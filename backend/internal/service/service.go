package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/gorm"
)

type Service struct {
	repo                 *repository.Repository
	dataDir              string
	cancelMu             sync.Mutex
	registrationMu       sync.Mutex
	emailCodeMu          sync.Mutex
	redeemBatchMu        sync.Mutex
	storageMu            sync.Mutex
	storageLocks         map[string]*userStorageLock
	characterTaskMu      sync.Mutex
	activeCancels        map[string]context.CancelFunc
	pendingStorage       map[string]int64
	coordinator          *runtimeCoordinator
	runtimeErr           error
	shutdownDrainTimeout time.Duration
	workerID             string
	workerHealthMu    sync.RWMutex
	workerHeartbeat   time.Time
	workerError       string
	workerLifecycleMu sync.Mutex
	workerStarted     bool
	workerStopping    bool
	workerStop        chan struct{}
	workerDone        chan struct{}
	workerTasks       sync.WaitGroup
	workerSlotMu      sync.RWMutex
	workerSlotIssue   map[string]struct{}
	runtimeHealthMu   sync.Mutex
	runtimeHealthAt   time.Time
	runtimeHealth     RuntimeHealth
}

const taskLogPayloadLimit = 4000

var sessionRequestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type CreateSessionRequest struct {
	RequestKey               string            `json:"requestKey"`
	ProjectID                string            `json:"projectId"`
	Prompt                   string            `json:"prompt"`
	CanvasSnapshot           map[string]any    `json:"canvasSnapshot"`
	References               []string          `json:"references"`
	Requirements             string            `json:"requirements"`
	CanvasAssets             []storyboardAsset `json:"canvasAssets"`
	Config                   providerConfig    `json:"config"`
	ChannelProbeTaskID       string            `json:"channelProbeTaskId"`
	ToolProbeTaskID          string            `json:"toolProbeTaskId"`
	AllowPaidStructureRepair bool              `json:"allowPaidStructureRepair"`
}

type CreateTaskRequest struct {
	SessionID                 string         `json:"sessionId"`
	ProjectID                 string         `json:"projectId"`
	Type                      string         `json:"type"`
	Operation                 string         `json:"operation"`
	Prompt                    string         `json:"prompt"`
	Provider                  string         `json:"provider"`
	Model                     string         `json:"model"`
	SourceTaskID              string         `json:"sourceTaskId"`
	ConfirmNewProviderRequest bool           `json:"confirmNewProviderRequest"`
	Input                     map[string]any `json:"input"`
}

type SessionDetail struct {
	Session  model.Session   `json:"session"`
	Messages []model.Message `json:"messages"`
	Tasks    []TaskSummary   `json:"tasks"`
	Results  []model.Result  `json:"results"`
}

type TaskSummary struct {
	ID                  string              `json:"id"`
	SessionID           string              `json:"sessionId,omitempty"`
	ProjectID           string              `json:"projectId,omitempty"`
	Type                string              `json:"type"`
	Status              model.TaskStatus    `json:"status"`
	Stage               string              `json:"stage"`
	Progress            int                 `json:"progress"`
	Prompt              string              `json:"prompt"`
	Operation           string              `json:"operation,omitempty"`
	Provider            string              `json:"provider,omitempty"`
	Model               string              `json:"model,omitempty"`
	ProviderRequestID   string              `json:"providerRequestId,omitempty"`
	Error               string              `json:"error,omitempty"`
	ErrorCode           string              `json:"errorCode,omitempty"`
	PreviewURL          string              `json:"previewUrl,omitempty"`
	PreviewKind         string              `json:"previewKind,omitempty"`
	DeliveryRecoverable bool                `json:"deliveryRecoverable,omitempty"`
	Attempts            int                 `json:"attempts"`
	NextPollAt          *time.Time          `json:"nextPollAt,omitempty"`
	StartedAt           *time.Time          `json:"startedAt"`
	CompletedAt         *time.Time          `json:"completedAt"`
	CreatedAt           time.Time           `json:"createdAt"`
	UpdatedAt           time.Time           `json:"updatedAt"`
	Billing             *TaskBillingSummary `json:"billing,omitempty"`
}

type TaskBillingSummary struct {
	AmountMicrocredits int64               `json:"amountMicrocredits"`
	Status             model.BillingStatus `json:"status"`
}

type TaskDetail struct {
	model.Task
	Billing *TaskBillingSummary `json:"billing,omitempty"`
}

type TaskListOptions struct {
	Limit      int
	ProjectID  string
	ActiveOnly bool
}

type agentStoryboardInput struct {
	References               []string          `json:"references"`
	CanvasSnapshot           map[string]any    `json:"canvasSnapshot"`
	Requirements             string            `json:"requirements"`
	CanvasAssets             []storyboardAsset `json:"canvasAssets"`
	Config                   providerConfig    `json:"config"`
	ChannelProbeTaskID       string            `json:"channelProbeTaskId"`
	ToolProbeTaskID          string            `json:"toolProbeTaskId"`
	ShotDuration             int               `json:"shotDurationSeconds"`
	ShotCount                int               `json:"shotCount"`
	ManualDelivery           bool              `json:"manualDelivery"`
	AllowPaidStructureRepair bool              `json:"allowPaidStructureRepair"`
}

type storyboardAsset struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Type   string   `json:"type"`
	Tags   []string `json:"tags"`
	Prompt string   `json:"prompt"`
}

type agentStoryboardPlan struct {
	Title      string                 `json:"title"`
	Logline    string                 `json:"logline"`
	StyleGuide string                 `json:"styleGuide"`
	Characters []string               `json:"characters"`
	Locations  []string               `json:"locations"`
	Shots      []agentStoryboardShot  `json:"shots"`
	Raw        map[string]interface{} `json:"-"`
}

type agentStoryboardShot struct {
	Title             string               `json:"title"`
	Description       string               `json:"description"`
	Purpose           string               `json:"purpose"`
	InformationChange string               `json:"informationChange"`
	StartBoundary     *projectShotBoundary `json:"startBoundary"`
	EndBoundary       *projectShotBoundary `json:"endBoundary"`
	Duration          int                  `json:"durationSeconds"`
	Dialogue          string               `json:"dialogue"`
	ShotSize          string               `json:"shotSize"`
	Emotion           string               `json:"emotion"`
	Lighting          string               `json:"lightingAndAtmosphere"`
	AudioEffects      string               `json:"audioEffects"`
	VisualPrompt      string               `json:"visualPrompt"`
	VideoPrompt       string               `json:"videoPrompt"`
	Camera            string               `json:"camera"`
	Motion            string               `json:"motion"`
	TimeBeats         string               `json:"timeBeats"`
	Negative          string               `json:"negativePrompt"`
	AssetTags         []string             `json:"assetTags"`
}

func New(repo *repository.Repository, dataDir string) *Service {
	coordinator, err := newRuntimeCoordinator(repo.Dialect())
	return &Service{
		repo: repo, dataDir: dataDir, activeCancels: make(map[string]context.CancelFunc), storageLocks: make(map[string]*userStorageLock),
		coordinator: coordinator, runtimeErr: err, workerID: newID(), workerStop: make(chan struct{}), workerDone: make(chan struct{}),
		workerSlotIssue: make(map[string]struct{}),
	}
}

func (s *Service) CreateSession(userID string, req CreateSessionRequest) (*SessionDetail, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, BadAuthRequest("请输入影视分镜需求")
	}
	requestKey, err := normalizeSessionRequestKey(req.RequestKey)
	if err != nil {
		return nil, err
	}
	if requestKey != "" {
		existing, err := s.repo.SessionForRequestKey(userID, requestKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.ProjectID != strings.TrimSpace(req.ProjectID) || existing.Prompt != prompt {
				return nil, BadAuthRequest("影视会话请求键已用于另一项创建请求")
			}
			return s.resumeOrReadStoryboardSession(userID, *existing, req, prompt, compactPersistedValue(req.CanvasSnapshot))
		}
	}
	compactedSnapshot := compactPersistedValue(req.CanvasSnapshot)
	snapshotJSON, err := json.Marshal(compactedSnapshot)
	if err != nil {
		return nil, errors.Join(&AuthError{Status: 500, Message: "会话快照无法安全保存，本次未创建会话或任务"}, fmt.Errorf("serialize session snapshot: %w", err))
	}
	session := model.Session{ID: newID(), UserID: userID, ProjectID: strings.TrimSpace(req.ProjectID), RequestKey: requestKey, Status: model.SessionStatusActive, Prompt: prompt, CanvasSnapshotJSON: string(snapshotJSON)}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	unlockStorage := s.lockUserStorage(userID)
	usage, err := s.repo.UserStorageUsage(userID)
	if err != nil {
		unlockStorage()
		return nil, err
	}
	incomingBytes := int64(len([]byte(prompt))*2 + len(snapshotJSON))
	if err := validateStructuredStorageQuotaWithPolicy(usage, "session", true, incomingBytes, policy.Resource); err != nil {
		unlockStorage()
		return nil, err
	}
	if err := s.repo.CreateSessionDraft(&session); err != nil {
		unlockStorage()
		if errors.Is(err, repository.ErrDuplicateSessionRequest) {
			existing, lookupErr := s.repo.SessionForRequestKey(userID, requestKey)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if existing != nil && existing.ProjectID == session.ProjectID && existing.Prompt == prompt {
				return s.resumeOrReadStoryboardSession(userID, *existing, req, prompt, compactedSnapshot)
			}
			return nil, BadAuthRequest("影视会话创建请求正在处理中，请继续跟踪原请求")
		}
		return nil, err
	}
	if err := s.repo.EnsureInitialSessionMessage(&model.Message{ID: session.ID, UserID: userID, SessionID: session.ID, Role: "user", Content: prompt}); err != nil {
		cleanupErr := s.repo.DeleteSessionDraft(userID, session.ID)
		unlockStorage()
		if cleanupErr != nil {
			return nil, fmt.Errorf("创建会话消息失败：%v；清理会话失败：%w", err, cleanupErr)
		}
		return nil, err
	}
	unlockStorage()
	taskReq := storyboardSessionTaskRequest(session, req, prompt, compactedSnapshot)
	if _, err := s.CreateTask(userID, taskReq); err != nil {
		unlockCleanup := s.lockUserStorage(userID)
		cleanupErr := s.repo.DeleteSessionDraft(userID, session.ID)
		unlockCleanup()
		if cleanupErr != nil {
			return nil, fmt.Errorf("创建会话任务失败：%v；清理会话失败：%w", err, cleanupErr)
		}
		return nil, err
	}
	s.recordActivity(userID, "agent_message", 1)
	return s.SessionDetail(userID, session.ID)
}

func (s *Service) resumeOrReadStoryboardSession(userID string, session model.Session, req CreateSessionRequest, prompt string, compactedSnapshot any) (*SessionDetail, error) {
	detail, err := s.SessionDetail(userID, session.ID)
	if err != nil {
		return nil, err
	}
	if session.Status != model.SessionStatusActive || len(detail.Tasks) > 0 {
		return detail, nil
	}
	// 进程可能在会话落库后、任务创建前中断；固定消息 ID 与任务请求键让多实例恢复仍只产生一条消息和一张订单。
	if err := s.repo.EnsureInitialSessionMessage(&model.Message{ID: session.ID, UserID: userID, SessionID: session.ID, Role: "user", Content: prompt}); err != nil {
		return nil, err
	}
	if _, err := s.CreateTask(userID, storyboardSessionTaskRequest(session, req, prompt, compactedSnapshot)); err != nil {
		return nil, err
	}
	return s.SessionDetail(userID, session.ID)
}

func storyboardSessionTaskRequest(session model.Session, req CreateSessionRequest, prompt string, compactedSnapshot any) CreateTaskRequest {
	return CreateTaskRequest{SessionID: session.ID, ProjectID: session.ProjectID, Type: "agent_storyboard", Operation: "storyboard", Prompt: prompt, Provider: "openai-compatible", Model: req.Config.Model, Input: map[string]any{"references": req.References, "canvasSnapshot": compactedSnapshot, "requirements": req.Requirements, "canvasAssets": req.CanvasAssets, "config": req.Config, "channelProbeTaskId": req.ChannelProbeTaskID, "toolProbeTaskId": req.ToolProbeTaskID, "allowPaidStructureRepair": req.AllowPaidStructureRepair}}
}

func normalizeSessionRequestKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) < 8 || len(value) > 64 || !sessionRequestKeyPattern.MatchString(value) {
		return "", BadAuthRequest("影视会话请求键格式无效")
	}
	return value, nil
}

func channelModelNames(channel model.ModelChannel) []string {
	models := []string{}
	_ = json.Unmarshal([]byte(channel.ModelsJSON), &models)
	return normalizedChannelModelNames(models)
}

func normalizedChannelModelNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "models/"))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Service) SessionDetail(userID string, id string) (*SessionDetail, error) {
	session, err := s.repo.SessionForUser(userID, id)
	if err != nil {
		return nil, err
	}
	tasks, err := s.repo.SessionTasks(userID, id)
	if err != nil {
		return nil, err
	}
	if session.Status == model.SessionStatusActive && len(tasks) > 0 {
		latest := tasks[len(tasks)-1]
		if latest.Status == model.TaskStatusFailed || latest.Status == model.TaskStatusCancelled {
			// 兼容 Worker 在任务终态提交后、旧版会话状态写入前退出的窗口；条件更新会避开并发重试。
			if err := s.repo.MarkSessionFailedForTask(latest, defaultString(latest.Error, "会话任务已结束，但没有保存失败说明。")); err != nil {
				return nil, err
			}
			session, err = s.repo.SessionForUser(userID, id)
			if err != nil {
				return nil, err
			}
		}
	}
	messages, err := s.repo.SessionMessages(userID, id)
	if err != nil {
		return nil, err
	}
	taskSummaries := taskSummariesForOutput(tasks)
	results, err := s.repo.SessionResults(userID, id)
	if err != nil {
		return nil, err
	}
	return &SessionDetail{Session: *session, Messages: messages, Tasks: taskSummaries, Results: results}, nil
}

func (s *Service) CreateTask(userID string, req CreateTaskRequest) (*model.Task, error) {
	taskType := strings.TrimSpace(req.Type)
	if taskType == "" {
		taskType = "video_image_to_video"
	}
	if taskType == channelProbeTaskType {
		return nil, BadAuthRequest("渠道测活必须使用专用入口")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, BadAuthRequest("请输入生成提示词")
	}
	sourceTask, err := s.validateSourceTaskForNewRequest(userID, prompt, req)
	if err != nil {
		return nil, err
	}
	normalizedInput, err := normalizeTaskInput(req.Input)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(taskType, "video_") {
		if mode, _ := normalizedInput["mode"].(string); strings.TrimSpace(mode) == "" {
			normalizedInput["mode"] = "video"
		}
	}
	if containsInlineMediaDataURL(normalizedInput) {
		return nil, BadAuthRequest("任务输入不能包含内嵌媒体，请先上传到资源存储")
	}
	requestKey, err := generationTaskRequestKey(taskType, req.SessionID, req.ProjectID, req.Operation, normalizedInput)
	if err != nil {
		return nil, err
	}
	if sourceTask != nil {
		// 新任务不能覆盖旧任务；保留来源和确认事实，便于费用争议时还原用户操作链路。
		normalizedInput["sourceTaskId"] = sourceTask.ID
		normalizedInput["confirmNewProviderRequest"] = true
	}
	if requestKey != "" {
		existing, err := s.repo.ActiveTaskForRequestKey(userID, requestKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			s.logTaskEventBestEffort(userID, existing.ID, "info", "重复提交已复用原任务", "同一画布节点与生成操作仍有排队或运行中的任务，未创建新订单")
			return taskForOutput(*existing), nil
		}
	}
	if err := validateLongTextTaskConfig(taskType, normalizedInput); err != nil {
		return nil, err
	}
	if err := s.ValidateTaskCapability(userID, normalizedInput); err != nil {
		return nil, err
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	activeTasks, err := s.repo.ActiveTaskCountForUser(userID)
	if err != nil {
		return nil, err
	}
	if activeTasks >= int64(policy.Task.ActiveTaskLimit) {
		return nil, BadAuthRequest(fmt.Sprintf("同时排队或运行的任务最多 %d 个，请等待已有任务完成", policy.Task.ActiveTaskLimit))
	}
	task := model.Task{ID: newID(), UserID: userID, SessionID: req.SessionID, ProjectID: req.ProjectID, Type: taskType, Status: model.TaskStatusQueued, Stage: "等待队列调度", Progress: 5, Prompt: prompt, Operation: req.Operation, Provider: req.Provider, Model: req.Model, RequestKey: requestKey}
	if err := s.ensureTaskProjectActive(userID, req.ProjectID); err != nil {
		return nil, err
	}
	// 先保护并序列化任务输入，再构造计费订单；密钥加密或 JSON 持久化失败时不能留下可预留积分的订单对象。
	if err := s.protectTaskSecrets(normalizedInput); err != nil {
		return nil, errors.Join(&AuthError{Status: 500, Message: "任务输入无法安全保存，本次未创建任务或计费订单"}, fmt.Errorf("protect task input secrets: %w", err))
	}
	inputJSON, err := json.Marshal(normalizedInput)
	if err != nil {
		return nil, errors.Join(&AuthError{Status: 500, Message: "任务输入无法安全保存，本次未创建任务或计费订单"}, fmt.Errorf("serialize task input: %w", err))
	}
	billingOrder, err := s.taskBillingOrder(userID, &task, normalizedInput)
	if err != nil {
		return nil, err
	}
	task.InputJSON = string(inputJSON)
	if billingOrder != nil {
		task.BillingOrderID = billingOrder.ID
	}
	err = s.createTaskWithinStorageQuota(&task, billingOrder, policy)
	if errors.Is(err, repository.ErrDuplicateActiveTask) {
		existing, lookupErr := s.repo.ActiveTaskForRequestKey(userID, requestKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing != nil {
			s.logTaskEventBestEffort(userID, existing.ID, "info", "并发重复提交已复用原任务", "数据库唯一约束阻止了第二张任务与计费订单")
			return taskForOutput(*existing), nil
		}
		return nil, BadAuthRequest("同一画布节点与生成操作已有活动任务，请从任务中心继续跟踪原任务")
	}
	if errors.Is(err, repository.ErrActiveTaskLimit) {
		return nil, BadAuthRequest(fmt.Sprintf("同时排队或运行的任务最多 %d 个，请等待已有任务完成", policy.Task.ActiveTaskLimit))
	}
	if errors.Is(err, repository.ErrInsufficientCredits) {
		return nil, BadAuthRequest("积分不足，请先使用兑换码充值")
	}
	if err != nil {
		return nil, err
	}
	s.recordActivity(userID, "task", 1)
	logPayload := ""
	if sourceTask != nil {
		logPayload = "来源任务：" + sourceTask.ID + "；用户已确认创建新的供应商请求"
	}
	s.logTaskEventBestEffort(userID, task.ID, "info", "任务已进入队列", logPayload)
	return taskForOutput(task), nil
}

// 画布“重试”实际会创建新任务；服务端必须验证旧任务终态和计费状态，不能只依赖可绕过的前端弹窗。
func (s *Service) validateSourceTaskForNewRequest(userID string, prompt string, req CreateTaskRequest) (*model.Task, error) {
	sourceTaskID := strings.TrimSpace(req.SourceTaskID)
	if sourceTaskID == "" {
		return nil, nil
	}
	if !req.ConfirmNewProviderRequest {
		return nil, BadAuthRequest("重新生成会创建新的供应商请求并可能产生费用，请先核对原任务和账单，再明确确认")
	}
	source, err := s.repo.TaskForUser(userID, sourceTaskID)
	if err != nil {
		return nil, err
	}
	if source.Type == channelProbeTaskType {
		return nil, BadAuthRequest("渠道测活不能作为普通生成任务的重试来源")
	}
	switch source.Status {
	case model.TaskStatusSucceeded:
		return nil, BadAuthRequest("原后台任务已经成功，请先从任务中心恢复结果，不要重复调用供应商")
	case model.TaskStatusQueued, model.TaskStatusRunning:
		return nil, BadAuthRequest("原后台任务仍在排队或运行，请继续跟踪原任务，不要重复提交")
	case model.TaskStatusFailed, model.TaskStatusCancelled:
	default:
		return nil, BadAuthRequest("原任务状态不允许重新生成")
	}
	if source.ProjectID != "" && req.ProjectID != "" && source.ProjectID != req.ProjectID {
		return nil, BadAuthRequest("重试来源任务与当前画布不一致")
	}
	if requestedType := strings.TrimSpace(req.Type); requestedType != "" && source.Type != requestedType {
		return nil, BadAuthRequest("重试来源任务与当前生成类型不一致")
	}
	if source.Operation != "" && req.Operation != "" && source.Operation != req.Operation {
		return nil, BadAuthRequest("重试来源任务与当前生成操作不一致")
	}
	if isContentModerationFailure(source.Error) && strings.TrimSpace(source.Prompt) == prompt {
		return nil, BadAuthRequest(contentModerationRetryMessage)
	}
	s.hydrateTaskProviderRequestID(source)
	if recoverableProviderPollingTask(source) && strings.TrimSpace(source.BillingOrderID) == "" {
		return nil, BadAuthRequest("原任务已有可查询的上游结果，请先使用“手动查询任务”；无法恢复时再联系管理员核对")
	}
	// 自定义渠道可能没有平台计费订单，但调用日志仍能证明供应商已经成功收到一次付费请求；
	// 这种来源不能被“没有订单”误判为安全重试，先恢复或人工核对原调用。
	hasSuccessfulCall, err := s.repo.TaskHasSuccessfulBillableCall(source.ID)
	if err != nil {
		return nil, err
	}
	if hasSuccessfulCall {
		return nil, BadAuthRequest("原任务已有成功的供应商调用记录（即使没有平台计费订单也可能已经产生费用），请先恢复结果或核对供应商账单；本次未创建新的供应商请求")
	}
	// 这是用户明确确认后的新任务入口：保留旧任务的费用状态，不再把 uncertain
	// 变成只能找管理员的死循环；没有确认时仍在服务端拒绝，不能只靠前端弹窗。
	billingReviewRisk := taskErrorRequiresBillingReview(source.Error)
	if billingReviewRisk && !req.ConfirmNewProviderRequest {
		return nil, BadAuthRequest("原任务费用状态可能待核对；请在确认可能重复计费后再创建新的供应商请求")
	}
	if source.BillingOrderID != "" {
		order, err := s.repo.BillingOrder(source.BillingOrderID)
		if err != nil {
			return nil, err
		}
		if order.UserID != userID || order.TaskID != source.ID {
			return nil, BadAuthRequest("来源任务与计费订单归属不一致")
		}
		if order.Status == model.BillingStatusReserved || order.Status == model.BillingStatusRunning || order.Status == model.BillingStatusUncertain {
			if recoverableProviderPollingTask(source) {
				return nil, BadAuthRequest("原任务已有可查询的上游结果，请先使用“手动查询任务”；无法恢复时再联系管理员核对")
			}
			if !req.ConfirmNewProviderRequest {
				return nil, BadAuthRequest("原任务费用尚待核对；请在确认可能重复计费后再创建新的供应商请求")
			}
		}
	}
	return source, nil
}

// 所有任务输入先收敛为 JSON 对象，确保计费与密钥保护不会因 Go 结构体类型不同而被绕过。
func normalizeTaskInput(input map[string]any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, BadAuthRequest("任务输入格式无效")
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, BadAuthRequest("任务输入格式无效")
	}
	if snapshot, ok := normalized["canvasSnapshot"]; ok {
		normalized["canvasSnapshot"] = compactPersistedValue(snapshot)
	}
	return normalized, nil
}

// 画布任务键由服务端从业务身份计算，不能依赖容易被漏传或改写的浏览器运行状态。
func generationTaskRequestKey(taskType string, sessionID string, projectID string, operation string, input map[string]any) (string, error) {
	if taskType == "agent_storyboard" {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return "", BadAuthRequest("影视分镜任务缺少会话 ID")
		}
		identity := strings.Join([]string{taskType, sessionID, strings.TrimSpace(operation)}, "\x00")
		digest := sha256.Sum256([]byte(identity))
		return hex.EncodeToString(digest[:]), nil
	}
	if !strings.HasPrefix(taskType, "canvas_") && taskType != "agent_storyboard_rows" {
		return "", nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", BadAuthRequest("画布生成任务缺少画布 ID")
	}
	metadata, ok := input["metadata"].(map[string]any)
	if !ok {
		return "", BadAuthRequest("画布生成任务缺少节点身份")
	}
	nodeID, _ := metadata["nodeId"].(string)
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", BadAuthRequest("画布生成任务缺少节点 ID")
	}
	identity := strings.Join([]string{taskType, projectID, strings.TrimSpace(operation), nodeID}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:]), nil
}

func compactPersistedValue(value interface{}) interface{} {
	switch item := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			if text, ok := child.(string); ok && strings.HasPrefix(text, "data:") {
				result[key] = ""
				continue
			}
			result[key] = compactPersistedValue(child)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(item))
		for index, child := range item {
			result[index] = compactPersistedValue(child)
		}
		return result
	default:
		return value
	}
}

func (s *Service) Tasks(userID string, limit int) ([]TaskSummary, error) {
	return s.TasksWithOptions(userID, TaskListOptions{Limit: limit})
}

func (s *Service) TasksWithOptions(userID string, options TaskListOptions) ([]TaskSummary, error) {
	tasks, err := s.repo.Tasks(userID, options.Limit, options.ProjectID, options.ActiveOnly)
	if err != nil {
		return nil, err
	}
	orders, err := s.repo.BillingOrdersByTaskIDs(userID, taskBillingTaskIDs(tasks))
	if err != nil {
		return nil, err
	}
	return taskSummariesForOutputWithBilling(tasks, orders), nil
}

func (s *Service) Task(userID string, id string) (*model.Task, error) {
	task, err := s.repo.TaskForUser(userID, id)
	if err != nil {
		return nil, err
	}
	s.hydrateTaskProviderRequestID(task)
	return taskForOutput(*task), nil
}

func (s *Service) TaskWithBilling(userID string, id string) (*TaskDetail, error) {
	task, err := s.Task(userID, id)
	if err != nil {
		return nil, err
	}
	detail := &TaskDetail{Task: *task}
	if task.BillingOrderID == "" {
		return detail, nil
	}
	order, err := s.repo.BillingOrder(task.BillingOrderID)
	if err != nil {
		return nil, err
	}
	if order.UserID != userID || order.TaskID != task.ID {
		return nil, BadAuthRequest("任务与计费订单归属不一致")
	}
	detail.Billing = &TaskBillingSummary{AmountMicrocredits: order.AmountMicrocredits, Status: order.Status}
	return detail, nil
}

func (s *Service) hydrateTaskProviderRequestID(task *model.Task) {
	if task == nil || task.ProviderRequestID != "" {
		return
	}
	if task.BillingOrderID != "" {
		if order, err := s.repo.BillingOrder(task.BillingOrderID); err == nil {
			task.ProviderRequestID = strings.TrimSpace(order.ProviderRequestID)
		}
	}
	if task.ProviderRequestID == "" {
		if providerRequestID, err := s.repo.LatestProviderRequestIDForTaskAttempt(task.ID, task.BillingOrderID, task.StartedAt); err == nil {
			task.ProviderRequestID = providerRequestID
		}
	}
}

// Worker 恢复任务时必须在任何上游调用前严格找回当前尝试的任务 ID，避免数据库短暂失败后重复创建计费任务。
func (s *Service) hydrateClaimedTaskProviderRequestID(task *model.Task) error {
	if task == nil || task.ID == "" || task.ProviderRequestID != "" {
		return nil
	}
	if task.BillingOrderID != "" {
		order, err := s.repo.BillingOrder(task.BillingOrderID)
		if err != nil {
			return fmt.Errorf("读取任务计费订单中的上游任务 ID 失败：%w", err)
		}
		if order.TaskID != task.ID || order.UserID != task.UserID {
			return errors.New("任务与计费订单归属不一致")
		}
		task.ProviderRequestID = strings.TrimSpace(order.ProviderRequestID)
	}
	if task.ProviderRequestID == "" {
		providerRequestID, err := s.repo.LatestProviderRequestIDForTaskAttempt(task.ID, task.BillingOrderID, task.StartedAt)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取当前任务尝试的上游任务 ID 失败：%w", err)
		}
		task.ProviderRequestID = providerRequestID
	}
	if task.ProviderRequestID == "" {
		return nil
	}
	if err := s.repo.UpdateClaimedTaskProviderState(task.ID, task.LeaseOwner, task.ProviderRequestID, task.PollStage, task.NextPollAt); err != nil {
		return fmt.Errorf("保存恢复的上游任务 ID 失败：%w", err)
	}
	return nil
}

// 上游请求日志会在任务执行期间更新 provider 状态，终态保存前必须重新合并，避免旧任务对象覆盖可恢复 ID。
func (s *Service) refreshTaskProviderState(task *model.Task) error {
	if task == nil || task.ID == "" {
		return errors.New("任务状态无效")
	}
	latest, err := s.repo.Task(task.ID)
	if err != nil {
		return fmt.Errorf("刷新任务上游状态失败：%w", err)
	}
	if latest.ProviderRequestID != "" {
		task.ProviderRequestID = latest.ProviderRequestID
	}
	task.ProviderCallState = latest.ProviderCallState
	task.ProviderStateJSON = latest.ProviderStateJSON
	task.PollStage = latest.PollStage
	task.NextPollAt = latest.NextPollAt
	return nil
}

func (s *Service) RetryTask(userID string, id string, confirmNewProviderRequest bool) (*model.Task, error) {
	task, err := s.repo.TaskForUser(userID, id)
	if err != nil {
		return nil, err
	}
	if task.Type == channelProbeTaskType {
		return nil, BadAuthRequest("请从渠道设置重新发起测活，不要重试旧探针")
	}
	if task.Status != model.TaskStatusFailed && task.Status != model.TaskStatusCancelled {
		return nil, BadAuthRequest("只有失败或已取消的任务才能重新生成")
	}
	if taskHasDeliveryCheckpoint(*task) {
		return nil, BadAuthRequest("该任务已有可恢复的本地结果检查点，请先恢复已保存结果；本次没有创建新的供应商请求")
	}
	if isContentModerationFailure(task.Error) {
		return nil, BadAuthRequest(contentModerationRetryMessage)
	}
	s.hydrateTaskProviderRequestID(task)
	if recoverableProviderPollingTask(task) && strings.TrimSpace(task.BillingOrderID) == "" {
		return nil, BadAuthRequest("该任务已有可查询的上游结果，请先使用“手动查询任务”；仍无法恢复时再联系管理员核对")
	}
	// 不能只看当前任务是否绑定平台订单：自定义渠道可能没有订单，但成功调用日志仍证明原请求已经发出。
	hasSuccessfulCall, err := s.repo.TaskHasSuccessfulBillableCall(task.ID)
	if err != nil {
		return nil, err
	}
	if hasSuccessfulCall {
		return nil, BadAuthRequest("原任务已有成功的供应商调用记录（即使没有平台计费订单也可能已经产生费用），请先恢复结果或核对供应商账单；本次未创建新的供应商请求")
	}
	billingReviewRisk := taskErrorRequiresBillingReview(task.Error)
	if billingReviewRisk && !confirmNewProviderRequest {
		return nil, BadAuthRequest("原任务的错误说明表示请求状态或费用尚未核对；如果你已确认供应商不会再执行，请点击重试并明确确认可能重复计费，本次未创建新的供应商请求")
	}
	if task.BillingOrderID != "" {
		order, err := s.repo.BillingOrder(task.BillingOrderID)
		if err != nil {
			return nil, err
		}
		if order.UserID != task.UserID || order.TaskID != task.ID {
			return nil, BadAuthRequest("任务与计费订单归属不一致")
		}
		if order.Status == model.BillingStatusReserved || order.Status == model.BillingStatusRunning || order.Status == model.BillingStatusUncertain {
			if recoverableProviderPollingTask(task) {
				return nil, BadAuthRequest("该任务已有可查询的上游结果且上一笔计费尚未结清，请先使用“手动查询任务”；仍无法恢复时请联系管理员核对")
			}
			if !confirmNewProviderRequest {
				return nil, BadAuthRequest("该任务上一笔计费尚未结清；点击重试并明确确认可能重复计费即可创建新的尝试，本次未创建新的供应商请求")
			}
			billingReviewRisk = true
		}
	}
	// 每次重试都会创建新的供应商请求；确认必须由调用方显式提交，不能依赖可绕过的页面提示。
	if !confirmNewProviderRequest {
		return nil, BadAuthRequest("重试将创建新的供应商请求；部分任务可能包含一次已授权的结构修复并产生额外费用，请先核对原任务和供应商账单，再明确确认重新生成")
	}
	decryptedInput, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return nil, err
	}
	var billingInput map[string]any
	if err := json.Unmarshal([]byte(decryptedInput), &billingInput); err != nil {
		return nil, err
	}
	if err := validateLongTextTaskConfig(task.Type, billingInput); err != nil {
		return nil, err
	}
	billingOrder, err := s.taskBillingOrder(userID, task, billingInput)
	if err != nil {
		return nil, err
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	if err := s.ensureTaskProjectActive(userID, task.ProjectID); err != nil {
		return nil, err
	}
	retryLog := &model.TaskLog{
		ID:      newID(),
		UserID:  userID,
		TaskID:  task.ID,
		Level:   "info",
		Message: "任务已重新入队",
		Payload: func() string {
			if billingReviewRisk {
				return "用户已确认原任务费用可能仍待核对，并明确同意创建新的生成尝试；旧订单保留给管理员核账，不自动退款或结算"
			}
			return "用户已确认新的生成尝试及其可能包含的已授权结构修复会产生供应商费用"
		}(),
	}
	// 走到这里代表用户已经显式确认；允许旧订单保持 uncertain/reserved，
	// 但新尝试必须单独预留新订单，不能把旧订单的费用状态悄悄改掉。
	task, err = s.repo.RetryTaskWithBillingConfirmed(userID, task.ID, billingOrder, policy.Task.ActiveTaskLimit, retryLog)
	if errors.Is(err, repository.ErrDuplicateActiveTask) {
		return nil, BadAuthRequest("同一画布节点与生成操作已有排队或运行中的任务，请继续跟踪原任务")
	}
	if errors.Is(err, repository.ErrInsufficientCredits) {
		return nil, BadAuthRequest("积分不足，请先使用兑换码充值")
	}
	if errors.Is(err, repository.ErrActiveTaskLimit) {
		return nil, BadAuthRequest(fmt.Sprintf("同时排队或运行的任务最多 %d 个，请等待已有任务完成", policy.Task.ActiveTaskLimit))
	}
	if errors.Is(err, repository.ErrTaskNotRetryable) {
		return nil, BadAuthRequest("任务已被其他请求重新入队，请勿重复重试")
	}
	if errors.Is(err, repository.ErrTaskSessionUnavailable) {
		return nil, BadAuthRequest("任务关联的 Agent 会话不存在或已被删除，无法安全重试")
	}
	if errors.Is(err, repository.ErrTaskBillingPending) {
		return nil, BadAuthRequest("该任务上一笔计费尚未结清，请先查询上游任务或联系管理员核对")
	}
	if errors.Is(err, repository.ErrTaskProviderRecoveryConflict) {
		return nil, &AuthError{Status: 409, Message: "该任务正在查询上游状态，请等待查询完成后再重试"}
	}
	if err != nil {
		return nil, err
	}
	return taskForOutput(*task), nil
}

func (s *Service) CancelTask(userID string, id string) (*model.Task, error) {
	task, err := s.repo.TaskForUser(userID, id)
	if err != nil {
		return nil, err
	}
	if task.Status == model.TaskStatusSucceeded {
		return nil, BadAuthRequest("已完成任务不能取消")
	}
	now := time.Now()
	cancelNote := ""
	if task.Status == model.TaskStatusQueued {
		cancelled, err := s.repo.CancelTaskIfStatus(userID, task.ID, model.TaskStatusQueued, now)
		if err != nil {
			return nil, err
		}
		if cancelled {
			cancelNote = s.refundCancelledTaskBeforeProvider(*task)
			task, err = s.repo.TaskForUser(userID, id)
			if err != nil {
				return nil, err
			}
		} else {
			task, err = s.repo.TaskForUser(userID, id)
			if err != nil {
				return nil, err
			}
		}
	}
	if task.Status == model.TaskStatusRunning {
		// preflight 与真正发送请求争抢同一任务行：取消先赢才能安全退款，发送检查点先赢则必须按费用待核对处理。
		cancelledBeforeDispatch, err := s.repo.CancelTaskIfStatusAndProviderStates(
			userID, task.ID, model.TaskStatusRunning,
			[]model.TaskProviderCallState{model.TaskProviderCallPending, model.TaskProviderCallPreflight}, now,
		)
		if err != nil {
			return nil, err
		}
		if cancelledBeforeDispatch {
			s.cancelActiveTask(task.ID)
			cancelNote = s.refundCancelledTaskBeforeProvider(*task)
			task, err = s.repo.TaskForUser(userID, id)
			if err != nil {
				return nil, err
			}
		} else {
			s.cancelActiveTask(task.ID)
			cancelled, cancelErr := s.repo.CancelTaskIfStatus(userID, task.ID, model.TaskStatusRunning, now)
			if cancelErr != nil {
				return nil, cancelErr
			}
			latest, latestErr := s.repo.TaskForUser(userID, id)
			if latestErr != nil {
				return nil, latestErr
			}
			if latest.Status == model.TaskStatusSucceeded {
				return nil, BadAuthRequest("任务已经完成，不能取消")
			}
			task = latest
			if cancelled || task.Status == model.TaskStatusCancelled {
				if providerDispatchDefinitelyNotStarted(task.ProviderCallState) {
					cancelNote = s.refundCancelledTaskBeforeProvider(*task)
				} else {
					cancelNote = providerRequestCancellationRiskMessage
					if err := s.MarkBillingUncertain(task.BillingOrderID, "运行中的上游请求被用户取消，费用状态待核对"); err != nil {
						cancelNote = "任务已取消；上游供应商可能仍在执行并产生费用，且计费状态更新失败，需管理员核对，请勿立即重试。"
						s.logTaskInternalErrorBestEffort(userID, task.ID, "任务已取消但计费状态更新失败", err)
					}
				}
				task, err = s.repo.TaskForUser(userID, id)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if task.Status != model.TaskStatusCancelled {
		return nil, BadAuthRequest("当前任务状态不能取消")
	}
	if cancelNote != "" {
		// 即使风险说明落库失败，本次响应也必须把真实取消边界告诉用户。
		task.Error = cancelNote
		if err := s.repo.UpdateCancelledTaskError(userID, task.ID, cancelNote); err != nil {
			// 取消已经生效，不能把附加说明写入失败伪装成“取消失败”并诱导用户重复操作。
			s.logTaskInternalErrorBestEffort(userID, task.ID, "任务已取消但风险说明保存失败", err)
		} else if latest, err := s.repo.TaskForUser(userID, id); err == nil {
			task = latest
		}
	}
	if task.SessionID != "" {
		if err := s.markSessionFailed(*task, defaultString(cancelNote, "会话任务已取消。")); err != nil {
			s.logTaskInternalErrorBestEffort(userID, task.ID, "任务已取消但 Agent 会话终态保存失败", err)
		}
	}
	s.logTaskEventBestEffort(userID, task.ID, "warn", "任务已取消", cancelNote)
	return taskForOutput(*task), nil
}

func (s *Service) refundCancelledTaskBeforeProvider(task model.Task) string {
	if err := s.RefundBillingFromExecution(task.BillingOrderID, "任务在供应商请求发出前取消"); err != nil {
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务已取消但积分退回失败", err)
		return "任务已取消；本次没有发出供应商请求，但冻结积分退回失败，需管理员核对。"
	}
	return taskCancellationMessageWithBillingOutcome(true, task.BillingOrderID, false) + "。"
}

func (s *Service) TaskLogs(userID string, id string) ([]model.TaskLog, error) {
	task, err := s.repo.TaskForUser(userID, id)
	if err != nil {
		return nil, err
	}
	logs, err := s.repo.TaskLogs(userID, id)
	if err != nil || !isSystemChannelProbeTask(*task) {
		return logs, err
	}
	for index := range logs {
		logs[index].Message = redactSystemChannelProbeText(logs[index].Message)
		logs[index].Payload = redactSystemChannelProbeText(logs[index].Payload)
	}
	return logs, nil
}

func taskSummariesForOutput(tasks []model.Task) []TaskSummary {
	return taskSummariesForOutputWithBilling(tasks, nil)
}

func taskSummariesForOutputWithBilling(tasks []model.Task, orders map[string]model.BillingOrder) []TaskSummary {
	result := make([]TaskSummary, 0, len(tasks))
	for _, task := range tasks {
		summary := taskSummaryForOutput(task)
		if order, ok := orders[task.ID]; ok {
			summary.Billing = &TaskBillingSummary{AmountMicrocredits: order.AmountMicrocredits, Status: order.Status}
			if summary.ProviderRequestID == "" {
				summary.ProviderRequestID = order.ProviderRequestID
			}
		}
		result = append(result, summary)
	}
	return result
}

func taskBillingTaskIDs(tasks []model.Task) []string {
	ids := make([]string, 0, len(tasks))
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if task.BillingOrderID == "" {
			continue
		}
		if _, ok := seen[task.ID]; ok {
			continue
		}
		seen[task.ID] = struct{}{}
		ids = append(ids, task.ID)
	}
	return ids
}

func taskSummaryForOutput(task model.Task) TaskSummary {
	if task.Type == channelProbeTaskType {
		// 列表无需展示任何渠道的探针正文摘要；自定义渠道可在专用测活详情中查看。
		task.Error = redactSystemChannelProbeText(task.Error)
	}
	errorCode := ""
	if isContentModerationFailure(task.Error) {
		errorCode = contentModerationErrorCode
	}
	previewURL, previewKind := taskMediaPreview(task.ResultJSON, task.Type)
	return TaskSummary{
		ID:                  task.ID,
		SessionID:           task.SessionID,
		ProjectID:           task.ProjectID,
		Type:                task.Type,
		Status:              task.Status,
		Stage:               task.Stage,
		Progress:            task.Progress,
		Prompt:              truncateRunes(task.Prompt, 500),
		Operation:           task.Operation,
		Provider:            task.Provider,
		Model:               task.Model,
		ProviderRequestID:   task.ProviderRequestID,
		Error:               truncateRunes(task.Error, 500),
		ErrorCode:           errorCode,
		PreviewURL:          previewURL,
		PreviewKind:         previewKind,
		DeliveryRecoverable: taskDeliveryRecoverableForOutput(task),
		Attempts:            task.Attempts,
		NextPollAt:          task.NextPollAt,
		StartedAt:           task.StartedAt,
		CompletedAt:         task.CompletedAt,
		CreatedAt:           task.CreatedAt,
		UpdatedAt:           task.UpdatedAt,
	}
}

// 列表只暴露首个可访问媒体地址，避免把完整生成结果和内嵌数据带回前端。
func taskMediaPreview(raw string, taskType string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	var payload any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return "", ""
	}
	defaultKind := "image"
	if strings.Contains(strings.ToLower(taskType), "video") {
		defaultKind = "video"
	}
	return findTaskMediaPreview(payload, defaultKind)
}

func findTaskMediaPreview(value any, hint string) (string, string) {
	switch item := value.(type) {
	case string:
		text := strings.TrimSpace(item)
		if !strings.HasPrefix(text, "/api/resources/") && !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			return "", ""
		}
		kind := hint
		lower := strings.ToLower(text)
		if strings.Contains(lower, ".mp4") || strings.Contains(lower, ".webm") || strings.Contains(lower, ".mov") {
			kind = "video"
		} else if kind != "video" {
			kind = "image"
		}
		return text, kind
	case []any:
		for _, child := range item {
			if previewURL, previewKind := findTaskMediaPreview(child, hint); previewURL != "" {
				return previewURL, previewKind
			}
		}
	case map[string]any:
		for _, key := range []string{"images", "image", "video", "dataUrl", "url", "resultUrl", "outputUrl"} {
			child, exists := item[key]
			if !exists {
				continue
			}
			childHint := hint
			if key == "video" {
				childHint = "video"
			} else if key == "images" || key == "image" {
				childHint = "image"
			}
			if previewURL, previewKind := findTaskMediaPreview(child, childHint); previewURL != "" {
				return previewURL, previewKind
			}
		}
	}
	return "", ""
}

func truncateRunes(value string, limit int) string {
	text := []rune(value)
	if len(text) <= limit {
		return value
	}
	return string(text[:limit]) + "..."
}

func taskForOutput(task model.Task) *model.Task {
	task.DeliveryRecoverable = taskDeliveryRecoverableForOutput(task)
	redactSystemChannelProbeOutput(&task)
	task.InputJSON = publicTaskInputJSON(task.InputJSON)
	return &task
}

func taskHasDeliveryCheckpoint(task model.Task) bool {
	return strings.TrimSpace(task.ResultJSON) != "" && strings.TrimSpace(task.DeliveryOpsJSON) != ""
}

func taskDeliveryRecoverableForOutput(task model.Task) bool {
	return taskHasDeliveryCheckpoint(task) && (task.Status == model.TaskStatusFailed || task.Status == model.TaskStatusCancelled)
}

var publicTaskSizePattern = regexp.MustCompile(`(?i)^(auto|[0-9]{1,5}(?:\.[0-9]{1,3})?[x:][0-9]{1,5}(?:\.[0-9]{1,3})?(?:-[a-z0-9]{1,12})?)$`)

func publicTaskInputJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return ""
	}
	public := map[string]any{}
	// 任务完成后仍需依靠这些非敏感 ID 恢复项目产物归属；密钥等配置继续被过滤。
	for _, key := range []string{"mode", "metadata", "workflowStepId", "domainProjectId", "assetVersionId", "resourceId", "mediaType", "role"} {
		if value, ok := input[key]; ok {
			public[key] = value
		}
	}
	if allowRepair, ok := input["allowPaidStructureRepair"].(bool); ok {
		public["allowPaidStructureRepair"] = allowRepair
	}
	// 历史图片任务只需尺寸恢复节点比例；其余渠道配置可能含密钥、地址或提示词，禁止随任务详情返回。
	if config, ok := input["config"].(map[string]any); ok {
		if size, ok := config["size"].(string); ok {
			size = strings.TrimSpace(size)
			if publicTaskSizePattern.MatchString(size) {
				public["config"] = map[string]any{"size": size}
			}
		}
	}
	if len(public) == 0 {
		return ""
	}
	data, _ := json.Marshal(public)
	return string(data)
}

func (s *Service) StoreUpload(userID string, sessionID string, header *multipart.FileHeader) (*model.SessionFile, error) {
	policy, err := s.RuntimePolicy()
	if err != nil {
		return nil, err
	}
	maxBytes := megabytes(policy.Resource.SessionUploadMB)
	if header == nil || header.Size > maxBytes {
		return nil, BadAuthRequest(fmt.Sprintf("会话文件不能超过 %dMB", policy.Resource.SessionUploadMB))
	}
	day, err := s.reserveSessionUploadQuota(userID, header.Size)
	if err != nil {
		return nil, err
	}
	reserved := true
	defer func() {
		if reserved {
			s.releaseUserUploadQuota(userID, day, header.Size)
		}
	}()
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	uploadDir := filepath.Join(s.dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) != "" {
		if _, err := s.repo.SessionForUser(userID, sessionID); err != nil {
			return nil, err
		}
	}
	storedName := newID() + "-" + filepath.Base(header.Filename)
	path := filepath.Join(uploadDir, storedName)
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return nil, err
	}
	size, err := io.Copy(dst, io.LimitReader(file, maxBytes+1))
	closeErr := dst.Close()
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	if size > maxBytes {
		_ = os.Remove(path)
		return nil, BadAuthRequest(fmt.Sprintf("会话文件不能超过 %dMB", policy.Resource.SessionUploadMB))
	}
	item := model.SessionFile{ID: newID(), UserID: userID, SessionID: sessionID, FileName: header.Filename, MimeType: header.Header.Get("Content-Type"), Path: path, Size: size}
	if err := s.repo.Create(&item); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	s.commitUserUploadQuota(userID, header.Size)
	reserved = false
	return &item, nil
}

func (s *Service) processClaimedTask(task *model.Task) error {
	claimOwner := task.LeaseOwner
	if strings.TrimSpace(claimOwner) == "" {
		return repository.ErrTaskStateConflict
	}
	if err := s.hydrateClaimedTaskProviderRequestID(task); err != nil {
		return err
	}
	canContinue, recoveredOrder, err := s.recoveredTaskCanContinue(task)
	if err != nil {
		return err
	}
	if !canContinue {
		return s.stopRecoveredTaskReplay(task, claimOwner, recoveredOrder)
	}
	policy, err := s.RuntimePolicy()
	if err != nil {
		return err
	}
	executionTimeout := taskExecutionTimeoutWithPolicy(task.Type, policy.Task)
	executionDeadline := time.Now().Add(executionTimeout)
	if task.StartedAt != nil && !task.StartedAt.IsZero() {
		executionDeadline = task.StartedAt.Add(executionTimeout)
	}
	ctx, cancel := context.WithDeadline(context.Background(), executionDeadline)
	defer cancel()
	if ctx.Err() != nil && recoveredOrder != nil && recoveredOrder.Status == model.BillingStatusReserved {
		return s.failClaimedTaskBeforeProvider(task, claimOwner, "任务在恢复时已经超过执行时限，本次没有发出供应商请求；系统已停止任务并退回预留积分。")
	}
	leaseDone := make(chan struct{})
	leaseLost := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.repo.RenewTaskLease(task.ID, claimOwner, 45*time.Second); err != nil {
					leaseLost <- err
					cancel()
					return
				}
			case <-leaseDone:
				return
			}
		}
	}()
	defer close(leaseDone)
	s.registerActiveTask(task.ID, cancel)
	defer s.unregisterActiveTask(task.ID)

	capacityCtx, runningHubGuard, capacityDeferred, capacityErr := s.prepareClaimedRunningHubWorkflowCapacity(ctx, task)
	if capacityErr != nil {
		if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
			return nil
		}
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "读取 RunningHub 工作流排队状态失败", capacityErr)
		if strings.TrimSpace(task.ProviderRequestID) != "" || !providerDispatchDefinitelyNotStarted(task.ProviderCallState) {
			return s.deferClaimedRunningHubWorkflowGuardRecovery(task)
		}
		return s.failClaimedTaskBeforeProvider(task, claimOwner, "读取 RunningHub 工作流排队状态失败，本次没有发出供应商请求；系统已停止任务并退回预留积分。")
	}
	if capacityDeferred {
		return nil
	}
	ctx = capacityCtx
	if runningHubGuard != nil {
		defer runningHubGuard.Release()
	}

	if err := s.prepareClaimedTaskProviderCall(task, "调用生成模型", 35, "info", "供应商调用前检查已通过", "当前 Worker 租约、任务状态和调用阶段已原子确认；下一步进入计费运行边界"); err != nil {
		return err
	}
	if err := s.MarkBillingRunning(task.BillingOrderID); err != nil {
		task.Status = model.TaskStatusFailed
		task.Stage = "计费准备失败"
		task.Error = "计费准备失败，本次未调用供应商；预留积分将退回，如未恢复请联系管理员按任务与订单核对"
		task.PollStage = "failed"
		task.NextPollAt = nil
		task.LeaseOwner = ""
		task.LeaseExpiresAt = nil
		task.CompletedAt = ptr(time.Now())
		terminalErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner)
		refundErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "退回计费准备阶段的预留积分", s.RefundBillingFromExecution(task.BillingOrderID, "计费准备失败，上游请求未发出"))
		if terminalErr != nil {
			if errors.Is(terminalErr, repository.ErrTaskStateConflict) {
				if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
					return refundErr
				}
			}
			return errors.Join(err, terminalErr, refundErr)
		}
		return errors.Join(err, refundErr)
	}
	previousPollStage := task.PollStage
	result, canvasOps, err := s.processTask(ctx, *task)
	if stateErr := s.refreshTaskProviderState(task); stateErr != nil {
		return stateErr
	}
	if err != nil && task.ProviderRequestID != "" && (strings.HasPrefix(task.Type, "canvas_video") || strings.HasPrefix(task.Type, "video_")) {
		if deferred := deferTransientProviderPoll(ctx, previousPollStage, err); deferred != nil {
			if !errors.Is(deferred, context.DeadlineExceeded) {
				s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "上游任务查询暂时失败，已安排安全重试", taskFailureMessage(err))
			}
			err = deferred
		}
	}
	var deferredPoll *providerPollDeferredError
	if errors.As(err, &deferredPoll) {
		select {
		case leaseErr := <-leaseLost:
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务租约失效，等待其他 Worker 恢复", leaseErr)
			return leaseErr
		default:
		}
		stage := "等待上游生成"
		if deferredPoll.ProviderStatus != "" {
			stage += " · " + deferredPoll.ProviderStatus
		}
		if err := s.repo.DeferTaskProviderPoll(task.ID, claimOwner, stage, deferredPoll.PollStage, deferredPoll.NextPollAt); err != nil {
			if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
				return nil
			}
			return err
		}
		task.Stage = stage
		task.Progress = 55
		task.PollStage = deferredPoll.PollStage
		task.NextPollAt = &deferredPoll.NextPollAt
		task.LeaseOwner = ""
		task.LeaseExpiresAt = nil
		s.logTaskEventBestEffort(task.UserID, task.ID, "info", "已释放 Worker，等待下次上游查询", deferredPoll.NextPollAt.Format(time.RFC3339))
		return nil
	}
	providerSucceeded := err == nil
	if err == nil {
		result, err = s.persistTaskGeneratedMediaResult(*task, result)
	}
	if err == nil {
		_, err = s.finalizeCharacterTurnaroundTask(*task, result)
	}
	if err != nil {
		channelSlotFailedBeforeRequest := false
		if code, _ := ChannelSlotFailureDetails(err); code != "" {
			channelSlotFailedBeforeRequest = true
		}
		providerRequestFailedBeforeSend := providerRequestDefinitelyNotSent(err)
		failedBeforeProviderRequest := channelSlotFailedBeforeRequest || providerRequestFailedBeforeSend
		billingReviewRequired := providerSucceeded || s.BillingFailureRequiresReview(task.BillingOrderID, task.ID, err)
		select {
		case leaseErr := <-leaseLost:
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务租约失效，等待其他 Worker 恢复", leaseErr)
			return leaseErr
		default:
		}
		deliveryCheckpointed := false
		if providerSucceeded && result != nil {
			deliveryCheckpointed = s.checkpointProviderResultAfterLocalFailure(task, result, canvasOps)
		}
		if errors.Is(err, context.Canceled) {
			task.Status = model.TaskStatusCancelled
			task.Stage = "任务已取消"
			task.Error = taskCancellationMessageWithBillingOutcome(failedBeforeProviderRequest, task.BillingOrderID, billingReviewRequired)
			if billingReviewRequired {
				task.Stage = "任务已取消，费用待核对"
			}
			if providerSucceeded {
				task.Stage = "任务已取消，结果未交付"
				task.Error = providerResultCancellationMessage
			}
			if deliveryCheckpointed {
				task.Stage = "任务已取消，结果待恢复"
				task.Error = providerResultDeliveryFailureMessage
			}
			task.PollStage = "cancelled"
			task.NextPollAt = nil
			task.LeaseOwner = ""
			task.LeaseExpiresAt = nil
			task.CompletedAt = ptr(time.Now())
			if saveErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner); saveErr != nil {
				if errors.Is(saveErr, repository.ErrTaskStateConflict) {
					if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
						return nil
					}
				}
				billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记取消终态保存失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "任务取消时终态保存失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
				return errors.Join(saveErr, billingErr)
			}
			var billingErr error
			if failedBeforeProviderRequest && !billingReviewRequired {
				refundReason := "供应商请求发出前任务已取消，上游请求未发出"
				if channelSlotFailedBeforeRequest {
					refundReason = "等待渠道槽位期间取消，上游请求未发出"
				}
				billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "退回调用前取消的预留积分", s.RefundBillingFromExecution(task.BillingOrderID, refundReason))
			} else {
				billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记取消任务的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, task.Error))
			}
			if billingErr != nil {
				if failedBeforeProviderRequest && !billingReviewRequired {
					task.Error = "任务已在供应商请求发出前取消，本次没有调用供应商；但预留积分退回失败，需管理员按任务与订单核对"
				} else {
					task.Error = "任务已取消；供应商可能仍在执行并产生费用，且计费待核对状态更新失败，需管理员按任务与订单核对，请勿立即重试"
				}
				if updateErr := s.repo.UpdateCancelledTaskError(task.UserID, task.ID, task.Error); updateErr != nil {
					s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "取消任务的计费失败说明保存失败", updateErr)
					billingErr = errors.Join(billingErr, updateErr)
				}
				if task.SessionID != "" {
					if sessionErr := s.markSessionFailed(*task, task.Error); sessionErr != nil {
						s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "取消任务的 Agent 会话计费说明保存失败", sessionErr)
						billingErr = errors.Join(billingErr, sessionErr)
					}
				}
			}
			s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "任务已取消", task.Error)
			return billingErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if channelSlotFailedBeforeRequest {
				err = publicTaskError{message: taskChannelSlotTimeoutMessage(err), cause: err}
			} else if !providerRequestFailedBeforeSend {
				err = errors.New(taskTimeoutMessage(task.Type, task.ProviderRequestID != ""))
			}
		}
		task.Status = model.TaskStatusFailed
		task.Stage = "任务失败"
		if providerSucceeded {
			task.Error = providerResultPersistenceFailureMessage
			if deliveryCheckpointed {
				task.Stage = "任务交付失败"
				task.Error = providerResultDeliveryFailureMessage
			}
		} else {
			task.Error = taskFailureMessageWithBillingOutcome(err, failedBeforeProviderRequest, task.BillingOrderID, billingReviewRequired)
		}
		task.PollStage = "failed"
		task.NextPollAt = nil
		task.LeaseOwner = ""
		task.LeaseExpiresAt = nil
		task.CompletedAt = ptr(time.Now())
		if saveErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner); saveErr != nil {
			if errors.Is(saveErr, repository.ErrTaskStateConflict) {
				if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
					return nil
				}
			}
			billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记失败终态保存异常的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "任务失败时终态保存失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
			return errors.Join(err, saveErr, billingErr)
		}
		billingAction := "标记费用待核对"
		var billingErr error
		if billingReviewRequired {
			billingErr = s.MarkBillingUncertain(task.BillingOrderID, task.Error)
		} else {
			billingAction = "退回预留积分"
			billingErr = s.RefundBillingFromExecution(task.BillingOrderID, task.Error)
		}
		billingErr = s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, billingAction, billingErr)
		err = errors.Join(err, billingErr)
		failureTitle := "任务处理失败"
		if deliveryCheckpointed {
			failureTitle = "任务结果已保存，但本地交付失败"
		}
		s.logTaskEventBestEffort(task.UserID, task.ID, "error", failureTitle, task.Error)
		return err
	}
	latest, err := s.repo.Task(task.ID)
	if err != nil {
		return err
	}
	if latest.Status == model.TaskStatusCancelled {
		billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记结果返回后取消的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "上游已返回结果，但任务被取消"))
		if sessionErr := s.markSessionFailed(*latest, "会话任务已取消。"); sessionErr != nil {
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务取消后 Agent 会话终态保存失败", sessionErr)
			billingErr = errors.Join(billingErr, sessionErr)
		}
		s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "任务已取消，丢弃生成结果", "")
		return billingErr
	}
	resultJSON, opsJSON, completionErr := marshalTaskCompletion(result, canvasOps)
	task.Stage = "持久化生成结果"
	task.Progress = 90
	if progressErr := s.repo.UpdateClaimedTaskProgress(task.ID, claimOwner, task.Stage, task.Progress); progressErr != nil {
		log.Printf("task result persistence checkpoint failed task=%s: %v", task.ID, progressErr)
	}
	resultCheckpointed := false
	if completionErr == nil {
		completionErr = s.checkpointTaskResultWithinStorageQuota(task, resultJSON, opsJSON)
		resultCheckpointed = completionErr == nil
	}
	if completionErr == nil {
		completionErr = s.saveTaskCompletionWithinStorageQuota(task, resultJSON, opsJSON, len(canvasOps) > 0)
	}
	if completionErr != nil {
		log.Printf("task result persistence failed task=%s: %v", task.ID, completionErr)
		if errors.Is(completionErr, repository.ErrTaskStateConflict) {
			billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记任务终态冲突的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, "上游已返回结果，但任务终态已被其他操作修改"))
			if latest, latestErr := s.repo.Task(task.ID); latestErr == nil && latest.Status == model.TaskStatusCancelled {
				if sessionErr := s.markSessionFailed(*latest, "会话任务已取消。"); sessionErr != nil {
					s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务终态冲突后 Agent 会话保存失败", sessionErr)
					billingErr = errors.Join(billingErr, sessionErr)
				}
				s.logTaskEventBestEffort(task.UserID, task.ID, "warn", "任务已取消，丢弃生成结果", "")
				return billingErr
			}
			s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务完成时租约或状态已变化", completionErr)
			return errors.Join(completionErr, billingErr)
		}
		task.Status = model.TaskStatusFailed
		task.Stage = "任务结果保存失败"
		task.Error = providerResultPersistenceFailureMessage
		billingReviewMessage := "上游已成功但任务结果未保存：" + task.Error
		billingAction := "标记任务结果保存失败的费用待核对"
		failureTitle := "任务结果保存失败"
		if resultCheckpointed {
			task.Stage = "任务交付失败"
			task.Error = providerResultDeliveryFailureMessage
			billingReviewMessage = "上游已成功且结果检查点已保存，但任务交付事务未完成：" + task.Error
			billingAction = "标记任务交付失败的费用待核对"
			failureTitle = "任务结果已保存，但本地交付失败"
		}
		task.PollStage = "failed"
		task.NextPollAt = nil
		task.LeaseOwner = ""
		task.LeaseExpiresAt = nil
		task.CompletedAt = ptr(time.Now())
		terminalErr := s.repo.SaveClaimedTaskTerminal(task, model.TaskStatusRunning, claimOwner)
		billingErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, billingAction, s.MarkBillingUncertain(task.BillingOrderID, billingReviewMessage))
		s.logTaskEventBestEffort(task.UserID, task.ID, "error", failureTitle, task.Error)
		if terminalErr != nil {
			return errors.Join(completionErr, terminalErr, billingErr)
		}
		return errors.Join(completionErr, billingErr)
	}
	if _, registerErr := s.RegisterTaskOutputFromTask(*task); registerErr != nil {
		// 任务结果已经可靠持久化；项目产物登记失败会由项目详情读取时按任务唯一键补登记。
		s.logTaskInternalErrorBestEffort(task.UserID, task.ID, "任务成功但项目产物登记失败", registerErr)
	}
	var completionBillingErr error
	if err := s.SettleBillingFromExecution(task.BillingOrderID, ""); err != nil {
		log.Printf("task billing settlement failed task=%s order=%s: %v", task.ID, task.BillingOrderID, err)
		reviewMessage := "生成成功但积分结算失败；请按任务与订单核对服务端日志"
		uncertainErr := s.recordBillingTransitionFailure(task.UserID, task.ID, task.BillingOrderID, "标记结算失败的费用待核对", s.MarkBillingUncertain(task.BillingOrderID, reviewMessage))
		message := "积分结算失败，订单保持待核对"
		if uncertainErr != nil {
			message = "积分结算失败，且待核对状态更新失败"
		}
		logErr := s.log(task.UserID, task.ID, "error", message, "订单："+firstNonEmpty(strings.TrimSpace(task.BillingOrderID), "未生成")+"。请联系管理员按任务与订单核对服务端日志")
		completionBillingErr = errors.Join(fmt.Errorf("积分结算失败：%w", err), uncertainErr, logErr)
	}
	s.logTaskEventBestEffort(task.UserID, task.ID, "info", "任务完成，结果已持久化", "")
	return completionBillingErr
}

func marshalTaskCompletion(result map[string]interface{}, canvasOps []map[string]interface{}) ([]byte, []byte, error) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("serialize task result: %w", err)
	}
	opsJSON, err := json.Marshal(canvasOps)
	if err != nil {
		return nil, nil, fmt.Errorf("serialize canvas operations: %w", err)
	}
	return resultJSON, opsJSON, nil
}

func taskExecutionTimeoutWithPolicy(taskType string, policy RuntimeTaskPolicy) time.Duration {
	switch {
	case taskType == channelProbeTaskType:
		return time.Duration(policy.TextTimeoutMinutes) * time.Minute
	case taskType == "agent_storyboard" || taskType == "agent_storyboard_rows":
		return time.Duration(policy.StoryboardTimeoutMinutes) * time.Minute
	case strings.HasPrefix(taskType, "canvas_video") || strings.HasPrefix(taskType, "video_"):
		return time.Duration(policy.VideoTimeoutMinutes) * time.Minute
	case strings.HasPrefix(taskType, "canvas_image"):
		return time.Duration(policy.ImageTimeoutMinutes) * time.Minute
	case strings.HasPrefix(taskType, "canvas_audio"):
		return time.Duration(policy.AudioTimeoutMinutes) * time.Minute
	case strings.HasPrefix(taskType, "canvas_text"):
		return time.Duration(policy.TextTimeoutMinutes) * time.Minute
	default:
		return time.Duration(policy.DefaultTimeoutMinutes) * time.Minute
	}
}

func taskTimeoutMessage(taskType string, hasProviderTaskID bool) string {
	if taskType == channelProbeTaskType || taskType == "agent_storyboard" || taskType == "agent_storyboard_rows" || strings.HasPrefix(taskType, "canvas_text") {
		return "文本生成等待超时：模型请求可能仍在供应商服务端执行并产生费用，请勿立即重试，请先核对供应商后台或账单。"
	}
	if strings.HasPrefix(taskType, "canvas_video") || strings.HasPrefix(taskType, "video_") {
		if hasProviderTaskID {
			return "视频生成等待超时：已记录上游任务 ID，供应商任务可能仍在执行并产生费用；请勿立即重试，请先到任务中心使用“手动查询任务”。"
		}
		return "视频生成等待超时：上游创建结果或费用状态不确定，供应商可能仍在执行并产生费用；请勿立即重试，请先核对任务详情、供应商后台或账单。"
	}
	if strings.HasPrefix(taskType, "canvas_image") {
		return "图片生成等待超时：供应商请求可能仍在执行并产生费用；请勿立即重试，请先核对任务详情、供应商后台或账单。"
	}
	if strings.HasPrefix(taskType, "canvas_audio") {
		return "音频生成等待超时：供应商请求可能仍在执行并产生费用；请勿立即重试，请先核对任务详情、供应商后台或账单。"
	}
	return "任务执行超时：上游请求状态和费用可能尚未确认；请勿立即重试，请先核对任务详情、供应商后台或账单。"
}

func taskChannelSlotTimeoutMessage(err error) string {
	detail := "等待渠道并发槽位超时"
	if _, failureDetail := ChannelSlotFailureDetails(err); failureDetail != "" {
		detail = failureDetail
	}
	return detail + "：本次没有发出新的供应商请求，请等待已有任务完成后再重新提交。"
}

func (s *Service) processTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	decryptedInput, err := s.decryptTaskInputJSON(task.InputJSON)
	if err != nil {
		return nil, nil, err
	}
	task.InputJSON = decryptedInput
	ctx = withProviderAnalytics(ctx, s, task)
	if task.Type == channelProbeTaskType {
		result, err := s.processChannelProbeTask(ctx, task)
		return result, nil, err
	}
	if task.Type == "agent_storyboard_rows" {
		return s.processStoryboardRowsTask(ctx, task)
	}
	if strings.HasPrefix(task.Type, "canvas_") || canRunProviderTask(task) {
		result, err := s.processCanvasGenerationTask(ctx, task.UserID, task.Type, task.Prompt, task.InputJSON)
		return result, nil, err
	}
	if task.Type == "agent_storyboard" {
		return s.processAgentStoryboardTask(ctx, task)
	}
	if strings.HasPrefix(task.Type, "video_") {
		result, ops := buildVideoWorkflowResult(task)
		return result, ops, nil
	}
	result, ops := buildAgentResult(task)
	return result, ops, nil
}

func canRunProviderTask(task model.Task) bool {
	if !strings.HasPrefix(task.Type, "video_") || strings.TrimSpace(task.InputJSON) == "" {
		return false
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
		return false
	}
	mode, _ := input["mode"].(string)
	config, ok := input["config"].(map[string]any)
	if mode != "video" || !ok || strings.TrimSpace(fmt.Sprint(config["model"])) == "" {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(config["channelId"])) != "" || (strings.TrimSpace(fmt.Sprint(config["baseUrl"])) != "" && strings.TrimSpace(fmt.Sprint(config["apiKey"])) != "")
}

func (s *Service) processAgentStoryboardTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	input := agentStoryboardInput{}
	if strings.TrimSpace(task.InputJSON) != "" {
		if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
			return nil, nil, fmt.Errorf("Agent 会话输入解析失败：%w", err)
		}
	}
	assets := input.CanvasAssets
	if len(assets) == 0 {
		assets = extractStoryboardAssets(input.CanvasSnapshot)
	}
	if !providerConfigReady(input.Config) {
		// 影视 Agent 是真实模型生成入口；配置缺失时不能返回内置样例，
		// 否则页面会把“未调用供应商”误显示成成功分镜。
		return nil, nil, markProviderPreparationFailure(errors.New("请先配置可用的文本模型"))
	}
	config, err := s.resolveProviderConfig(ctx, input.Config)
	if err != nil {
		return nil, nil, markProviderPreparationFailure(err)
	}
	plannerPrompt := s.buildAgentStoryboardPlannerPrompt(task.Prompt, input.Requirements, assets, 0, 0)
	outcome, err := s.requestAgentStoryboardPlan(ctx, task, plannerPrompt, config, 0, 0, input.AllowPaidStructureRepair)
	if err != nil {
		return nil, nil, err
	}
	plan := outcome.Plan
	result, ops, err := buildAgentStoryboardResult(task, plan, assets)
	if err == nil {
		result["structureRepairUsed"] = outcome.RepairUsed
	}
	return result, ops, err
}

func (s *Service) processStoryboardRowsTask(ctx context.Context, task model.Task) (map[string]interface{}, []map[string]interface{}, error) {
	input := agentStoryboardInput{}
	if strings.TrimSpace(task.InputJSON) != "" {
		if err := json.Unmarshal([]byte(task.InputJSON), &input); err != nil {
			return nil, nil, fmt.Errorf("脚本任务输入解析失败：%w", err)
		}
	}
	if !providerConfigReady(input.Config) {
		return nil, nil, markProviderPreparationFailure(errors.New("请先配置可用的文本模型"))
	}
	assets := input.CanvasAssets
	if len(assets) == 0 {
		assets = extractStoryboardAssets(input.CanvasSnapshot)
	}
	config, err := s.resolveProviderConfig(ctx, input.Config)
	if err != nil {
		return nil, nil, markProviderPreparationFailure(err)
	}
	plannerPrompt := s.buildAgentStoryboardPlannerPrompt(task.Prompt, input.Requirements, assets, input.ShotDuration, input.ShotCount)
	if input.ManualDelivery {
		// 手动交付要和 Agent 的短文本入口保持一致：兼容只支持基础文本请求的网关，
		// 不强制 JSON MIME；返回的完整标签文本仍由后端结构解析器收敛为分镜行。
		plannerPrompt = buildManualStoryboardPlannerPrompt(task.Prompt, input.Requirements, input.ShotDuration, input.ShotCount)
	}
	outcome, err := s.requestAgentStoryboardPlanWithMode(ctx, task, plannerPrompt, config, input.ShotDuration, input.ShotCount, input.AllowPaidStructureRepair, input.ManualDelivery)
	if err != nil {
		return nil, nil, err
	}
	plan := outcome.Plan
	rows := make([]map[string]any, 0, len(plan.Shots))
	for index, shot := range plan.Shots {
		matchedAssets := matchStoryboardAssets(assets, shot.AssetTags)
		referenceNodeIDs := make([]string, 0, len(matchedAssets))
		for _, asset := range matchedAssets {
			referenceNodeIDs = append(referenceNodeIDs, asset.ID)
		}
		rows = append(rows, map[string]any{
			"shotNumber": index + 1, "durationSeconds": shot.Duration, "plotDescription": shot.Description,
			"purpose": shot.Purpose, "informationChange": shot.InformationChange,
			"startBoundary": shot.StartBoundary, "endBoundary": shot.EndBoundary,
			"dialogue": shot.Dialogue, "characters": []any{}, "shotSize": shot.ShotSize, "emotion": shot.Emotion,
			"lightingAndAtmosphere": shot.Lighting, "audioEffects": shot.AudioEffects,
			"imageGenerationPrompt": buildStoryboardImagePrompt(plan.StyleGuide, shot), "videoMotionPrompt": buildStoryboardVideoPrompt(plan.StyleGuide, shot),
			"camera": shot.Camera, "motion": shot.Motion, "timeBeats": shot.TimeBeats, "negativePrompt": shot.Negative,
			"referenceNodeIds": referenceNodeIDs, "assetTags": shot.AssetTags,
		})
	}
	return map[string]interface{}{"title": plan.Title, "rows": rows, "structureRepairUsed": outcome.RepairUsed}, nil, nil
}

func providerConfigReady(config providerConfig) bool {
	return strings.TrimSpace(config.Model) != "" && (strings.TrimSpace(config.ChannelID) != "" || (strings.TrimSpace(config.BaseURL) != "" && strings.TrimSpace(config.APIKey) != ""))
}

func fallbackAgentStoryboardPlan(prompt string) agentStoryboardPlan {
	title := shortTitle(prompt, 18)
	return agentStoryboardPlan{
		Title:      title,
		Logline:    "围绕用户 brief 拆解的影视短片工作流。",
		StyleGuide: "沿用项目已经选择的画风、媒介与制作形态，保持角色、空间、道具、色彩和表现方式一致。",
		Characters: []string{"主角：根据 brief 保持服装、动作动机和情绪连续。"},
		Locations:  []string{"主场景：根据 brief 建立可理解的空间关系和稳定环境状态。"},
		Shots: []agentStoryboardShot{
			{
				Title:             "开场建立",
				Description:       "建立故事空间、主角状态和即将出现的冲突线索。",
				Purpose:           "让观众理解主角此刻在哪里、处于什么状态。",
				InformationChange: "未知人物与空间 -> 明确主角、空间和冲突线索。",
				StartBoundary:     &projectShotBoundary{Positions: []string{"主角位于主场景的既定起始位置"}, VisibleState: []string{"环境维持章节开头状态"}},
				EndBoundary:       &projectShotBoundary{Positions: []string{"主角仍位于可承接下一镜的位置"}, Gaze: []string{"注意力落向冲突线索"}, VisibleState: []string{"冲突线索已经可见"}},
				Duration:          8,
				ShotSize:          "按空间建立需要选择",
				Emotion:           "由平稳转为警觉",
				VisualPrompt:      "冻结在故事开始瞬间：主角位于主场景既定位置，环境状态、人物外观和持物与项目设定一致，冲突线索尚未完成变化。",
				VideoPrompt:       "从已定义的开始位置出发，先建立人物与空间，再让冲突线索进入可见状态，最后让主角的注意力落向该线索并到达结束边界。",
				Camera:            "根据项目媒介和空间关系选择能清楚建立场面的机位",
				Motion:            "仅在有助于揭示冲突线索时移动，否则保持稳定",
				TimeBeats:         "0-3秒：建立开始边界；3-6秒：冲突线索出现；6-8秒：主角反应并到达结束边界",
			},
			{
				Title:             "冲突推进",
				Description:       "主角采取可见行动，关系或局势因此发生变化。",
				Purpose:           "让观众看清主角如何回应冲突以及行动造成的结果。",
				InformationChange: "只看到冲突线索 -> 看见主角行动及其阶段结果。",
				StartBoundary:     &projectShotBoundary{Positions: []string{"主角从上一镜结束位置开始"}, Gaze: []string{"注意力锁定冲突对象"}},
				EndBoundary:       &projectShotBoundary{Positions: []string{"主角完成阶段行动并停在结果位置"}, VisibleState: []string{"行动结果已经可见"}},
				Duration:          10,
				ShotSize:          "按动作可读性选择",
				Emotion:           "冲突升级",
				VisualPrompt:      "冻结在冲突行动开始前：主角从上一镜结束位置面对冲突对象，人物、道具和环境状态连续，尚未出现阶段结果。",
				VideoPrompt:       "从上一镜的结束状态开始，按因果顺序完成主角行动、对象反馈和环境反应，最后停在阶段结果已经可见的结束边界。",
				Camera:            "选择能同时读清主体动作、接触关系和结果的机位",
				Motion:            "跟随关键动作保持空间关系可读，不添加无动机的额外技法",
				TimeBeats:         "0-2秒：确认开始边界；2-7秒：执行主要行动与反馈；7-10秒：结果显现并到达结束边界",
			},
			{
				Title:             "结果与钩子",
				Description:       "交代冲突结果，并把注意力引向可承接下一段的信息。",
				Purpose:           "让观众确认本段结果，同时形成继续观看的问题。",
				InformationChange: "阶段结果刚出现 -> 结果被确认并出现下一段钩子。",
				StartBoundary:     &projectShotBoundary{Positions: []string{"人物与物件沿用上一镜的结果位置"}, VisibleState: []string{"冲突结果刚刚形成"}},
				EndBoundary:       &projectShotBoundary{Positions: []string{"主体停在能承接下一镜的位置"}, Gaze: []string{"注意力落向钩子信息"}, VisibleState: []string{"钩子信息清晰可见"}},
				Duration:          8,
				ShotSize:          "按结果与钩子的注意层级选择",
				Emotion:           "结果落定并留下悬念",
				VisualPrompt:      "冻结在阶段结果刚形成的瞬间：人物、道具和环境后果与上一镜连续，下一段钩子尚未完成揭示。",
				VideoPrompt:       "从冲突结果刚形成的开始边界出发，先确认人物反应与环境后果，再揭示钩子信息，最后让主体注意力和画面焦点共同落到结束边界。",
				Camera:            "选择能从结果自然转移到钩子信息的机位",
				Motion:            "只执行一次有明确揭示目的的注意力转移，或保持固定完成揭示",
				TimeBeats:         "0-3秒：确认结果；3-6秒：揭示钩子；6-8秒：注意力落点并保持结束边界",
			},
		},
	}
}

func buildAgentStoryboardResult(task model.Task, plan agentStoryboardPlan, assets []storyboardAsset) (map[string]interface{}, []map[string]interface{}, error) {
	prefix := "agent-" + task.ID
	scriptID := prefix + "-script"
	sceneID := prefix + "-scenes"
	styleID := prefix + "-style"
	referenceID := prefix + "-assets"
	storyboardID := prefix + "-storyboard"
	finalID := prefix + "-final"
	sceneX := 380
	styleX := sceneX + 380
	storyboardRows := make([]map[string]any, 0, len(plan.Shots))
	for index, shot := range plan.Shots {
		matchedAssets := matchStoryboardAssets(assets, shot.AssetTags)
		referenceNodeIDs := make([]string, 0, len(matchedAssets))
		for _, asset := range matchedAssets {
			referenceNodeIDs = append(referenceNodeIDs, asset.ID)
		}
		storyboardRows = append(storyboardRows, map[string]any{
			"id": fmt.Sprintf("%s-row-%d", prefix, index+1), "shotNumber": index + 1, "durationSeconds": shot.Duration,
			"plotDescription": shot.Description, "dialogue": shot.Dialogue, "characters": []map[string]any{},
			"purpose": shot.Purpose, "informationChange": shot.InformationChange,
			"startBoundary": shot.StartBoundary, "endBoundary": shot.EndBoundary,
			"shotSize": shot.ShotSize, "emotion": shot.Emotion, "lightingAndAtmosphere": shot.Lighting,
			"audioEffects": shot.AudioEffects, "camera": shot.Camera, "motion": shot.Motion, "timeBeats": shot.TimeBeats,
			"imageGenerationPrompt": buildStoryboardImagePrompt(plan.StyleGuide, shot),
			"videoMotionPrompt": buildStoryboardVideoPrompt(plan.StyleGuide, shot), "negativePrompt": shot.Negative,
			"referenceNodeIds": referenceNodeIDs, "status": "idle",
		})
	}
	allReferenceNodeIDs := make([]string, 0, len(assets))
	for _, asset := range assets {
		allReferenceNodeIDs = append(allReferenceNodeIDs, asset.ID)
	}
	ops := []map[string]any{
		nodeOpWithMetadata(scriptID, "text", "剧本 · "+shortTitle(plan.Title, 24), 0, 0, map[string]any{"workflowKind": "script", "workflowTitle": "剧本", "status": "success", "content": strings.Join([]string{plan.Title, "", plan.Logline, "", task.Prompt}, "\n")}),
		nodeOpWithMetadata(sceneID, "text", "场景设定", sceneX, 0, map[string]any{"workflowKind": "scene", "workflowTitle": "场景", "status": "success", "content": listContent("场景", plan.Locations)}),
		nodeOpWithMetadata(styleID, "text", "风格板", styleX, 0, map[string]any{"workflowKind": "styleboard", "workflowTitle": "风格板", "status": "success", "content": plan.StyleGuide}),
		nodeOpWithMetadata(referenceID, "text", "参考素材组", 0, 270, map[string]any{"workflowKind": "reference_set", "workflowTitle": "参考素材组", "status": "success", "content": storyboardAssetsContent(assets)}),
		nodeOpWithMetadata(storyboardID, "script", "分镜脚本 · "+shortTitle(plan.Title, 24), 0, 560, map[string]any{
			"workflowKind": "storyboard", "workflowTitle": "分镜脚本", "workflowDescription": "由影视 Agent 生成的结构化分镜，可继续生成分镜图或复制视频提示词。", "status": "success",
			"storyboard": map[string]any{
				"rows": storyboardRows,
				"visibleColumns": []string{"shotNumber", "durationSeconds", "plotDescription", "dialogue", "shotSize", "emotion", "camera", "motion", "imageGenerationPrompt", "videoMotionPrompt", "negativePrompt"},
				"referenceNodeIds": allReferenceNodeIDs,
			},
		}),
		nodeOpWithMetadata(finalID, "video", "成片 · 待生成", styleX, 270, map[string]any{"workflowKind": "final", "workflowTitle": "成片", "status": "idle"}),
		connectOp(scriptID, sceneID),
		connectOp(scriptID, storyboardID),
		connectOp(sceneID, storyboardID),
		connectOp(styleID, storyboardID),
		connectOp(referenceID, storyboardID),
		connectOp(storyboardID, finalID),
	}
	resultShots := make([]map[string]any, 0, len(plan.Shots))
	for index, shot := range plan.Shots {
		shotID := fmt.Sprintf("%s-shot-%d", prefix, index+1)
		matchedAssets := matchStoryboardAssets(assets, shot.AssetTags)
		assetIDs := make([]string, 0, len(matchedAssets))
		for _, asset := range matchedAssets {
			assetIDs = append(assetIDs, asset.ID)
		}
		ops = append(ops,
			nodeOpWithMetadata(shotID, "config", fmt.Sprintf("镜头 %d · %s", index+1, shortTitle(shot.Title, 18)), index*360, 1040, map[string]any{
				"workflowKind":          "shot",
				"workflowTitle":         shot.Title,
				"workflowDescription":   shotDescription(shot),
				"shotIndex":             index + 1,
				"generationMode":        "video",
				"prompt":                buildStoryboardVideoPrompt(plan.StyleGuide, shot),
				"composerContent":       shotComposerContent(buildStoryboardVideoPrompt(plan.StyleGuide, shot), matchedAssets),
				"videoEditOperation":    "image_to_video",
				"assetTags":             shot.AssetTags,
				"referenceAssetNodeIds": assetIDs,
				"status":                "idle",
			}),
			connectOp(scriptID, shotID),
			connectOp(shotID, finalID),
		)
		for _, asset := range matchedAssets {
			ops = append(ops, connectOp(asset.ID, shotID))
		}
		resultShots = append(resultShots, map[string]any{"title": shot.Title, "description": shot.Description, "assetTags": shot.AssetTags, "referenceAssetNodeIds": assetIDs})
	}
	ops = append(ops, map[string]any{"type": "select_nodes", "ids": append([]string{storyboardID}, shotIDs(prefix, len(plan.Shots))...)})
	result := map[string]any{
		"taskId":     task.ID,
		"operation":  task.Operation,
		"provider":   defaultString(task.Provider, "internal-agent"),
		"model":      defaultString(task.Model, "workflow-router"),
		"title":      plan.Title,
		"logline":    plan.Logline,
		"styleGuide": plan.StyleGuide,
		"characters": plan.Characters,
		"locations":  plan.Locations,
		"shots":      resultShots,
	}
	return result, ops, nil
}

func extractStoryboardAssets(snapshot map[string]any) []storyboardAsset {
	rawNodes, _ := snapshot["nodes"].([]interface{})
	assets := make([]storyboardAsset, 0, len(rawNodes))
	for _, raw := range rawNodes {
		node, _ := raw.(map[string]interface{})
		if node == nil || fmt.Sprint(node["type"]) != "image" {
			continue
		}
		metadata, _ := node["metadata"].(map[string]interface{})
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		id := stringValue(node["id"])
		if id == "" {
			continue
		}
		tags := stringSlice(metadata["assetTags"])
		prompt := stringValue(metadata["prompt"])
		content := stringValue(metadata["content"])
		if len(tags) == 0 && prompt == "" && content == "" {
			continue
		}
		assets = append(assets, storyboardAsset{ID: id, Title: defaultString(stringValue(node["title"]), "未命名图片"), Type: "image", Tags: tags, Prompt: prompt})
		if len(assets) >= 30 {
			break
		}
	}
	return assets
}

func matchStoryboardAssets(assets []storyboardAsset, shotTags []string) []storyboardAsset {
	wanted := map[string]bool{}
	for _, tag := range shotTags {
		for _, token := range storyboardTagTokens(tag) {
			wanted[token] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	matched := make([]storyboardAsset, 0)
	for _, asset := range assets {
		tokens := map[string]bool{}
		for _, token := range storyboardTagTokens(asset.Title) {
			tokens[token] = true
		}
		for _, tag := range asset.Tags {
			for _, token := range storyboardTagTokens(tag) {
				tokens[token] = true
			}
		}
		if storyboardTokensMatch(wanted, tokens) {
			matched = append(matched, asset)
		}
		if len(matched) >= 6 {
			break
		}
	}
	return matched
}

func storyboardTokensMatch(wanted map[string]bool, tokens map[string]bool) bool {
	for want := range wanted {
		if tokens[want] {
			return true
		}
		for token := range tokens {
			if meaningfulStoryboardTagToken(want) && meaningfulStoryboardTagToken(token) && (strings.Contains(token, want) || strings.Contains(want, token)) {
				return true
			}
		}
	}
	return false
}

func storyboardTagTokens(value string) []string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.Join(strings.Fields(strings.ReplaceAll(value, "：", ":")), ""), "，", ","))
	if normalized == "" {
		return nil
	}
	tokens := []string{normalized}
	if index := strings.Index(normalized, ":"); index >= 0 {
		tokens = append(tokens, normalized[index+1:])
	}
	unique := make([]string, 0, len(tokens))
	seen := map[string]bool{}
	for _, token := range tokens {
		if meaningfulStoryboardTagToken(token) && !seen[token] {
			seen[token] = true
			unique = append(unique, token)
		}
	}
	return unique
}

func meaningfulStoryboardTagToken(value string) bool {
	if len([]rune(value)) < 2 {
		return false
	}
	switch value {
	case "角色", "环境", "场景", "道具", "武器", "风格":
		return false
	}
	return true
}

func listContent(title string, items []string) string {
	if len(items) == 0 {
		return title + "\n\n- 暂无明确内容。"
	}
	lines := []string{title, ""}
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			lines = append(lines, "- "+item)
		}
	}
	return strings.Join(lines, "\n")
}

func storyboardAssetsContent(assets []storyboardAsset) string {
	if len(assets) == 0 {
		return "当前画布暂无可用图片资产。建议先给角色、环境、道具图片添加资产标签。"
	}
	lines := make([]string, 0, len(assets))
	for _, asset := range assets {
		line := asset.Title + "\nID: " + asset.ID
		if len(asset.Tags) > 0 {
			line += "\n标签: " + strings.Join(asset.Tags, "、")
		}
		if asset.Prompt != "" {
			line += "\n原提示词: " + asset.Prompt
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n\n")
}

func shotDescription(shot agentStoryboardShot) string {
	parts := []string{shot.Description}
	if strings.TrimSpace(shot.VisualPrompt) != "" {
		parts = append(parts, "画面提示词："+shot.VisualPrompt)
	}
	if strings.TrimSpace(shot.Camera) != "" {
		parts = append(parts, "镜头："+shot.Camera)
	}
	if strings.TrimSpace(shot.Motion) != "" {
		parts = append(parts, "运动："+shot.Motion)
	}
	if strings.TrimSpace(shot.TimeBeats) != "" {
		parts = append(parts, "时间节拍："+shot.TimeBeats)
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n\n")
}

func buildStoryboardVideoPrompt(styleGuide string, shot agentStoryboardShot) string {
	parts := make([]string, 0, 8)
	if style := strings.TrimSpace(styleGuide); style != "" {
		parts = append(parts, "【项目画风与制作形态】\n"+style)
	}
	if start := storyboardBoundaryPrompt(shot.StartBoundary); start != "" {
		parts = append(parts, "【开始边界】\n"+start)
	}
	parts = append(parts,
		"【边界变化】\n"+strings.TrimSpace(shot.VideoPrompt),
		"【镜头与主体运动】\n"+strings.TrimSpace(shot.ShotSize)+"；"+strings.TrimSpace(shot.Camera)+"；"+strings.TrimSpace(shot.Motion),
		"【时间分配】\n"+strings.TrimSpace(shot.TimeBeats),
	)
	if strings.TrimSpace(shot.Dialogue) != "" || strings.TrimSpace(shot.AudioEffects) != "" {
		parts = append(parts, "【台词/声音】\n"+strings.TrimSpace(shot.Dialogue)+"；音效："+strings.TrimSpace(shot.AudioEffects))
	}
	if end := storyboardBoundaryPrompt(shot.EndBoundary); end != "" {
		parts = append(parts, "【结束边界】\n"+end)
	}
	parts = append(parts, "只实现上述开始边界到结束边界的变化，不改写角色身份、持物、空间关系或结束状态。")
	if negative := strings.TrimSpace(shot.Negative); negative != "" {
		parts = append(parts, "【本镜风险排除】\n"+negative)
	}
	return strings.Join(parts, "\n\n")
}

func buildStoryboardImagePrompt(styleGuide string, shot agentStoryboardShot) string {
	parts := make([]string, 0, 4)
	if style := strings.TrimSpace(styleGuide); style != "" {
		parts = append(parts, "【项目画风与制作形态】\n"+style)
	}
	if start := storyboardBoundaryPrompt(shot.StartBoundary); start != "" {
		parts = append(parts, "【冻结画面边界】\n"+start)
	}
	parts = append(parts, "【图片提示词】\n"+strings.TrimSpace(shot.VisualPrompt), "只表现上述单一开始瞬间，不描述动作过程、结束状态或时间推进。")
	if negative := strings.TrimSpace(shot.Negative); negative != "" {
		parts = append(parts, "【本镜风险排除】\n"+negative)
	}
	return strings.Join(parts, "\n\n")
}

func storyboardBoundaryPrompt(boundary *projectShotBoundary) string {
	if boundary == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	appendValues := func(label string, values []string) {
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				cleaned = append(cleaned, value)
			}
		}
		if len(cleaned) > 0 {
			parts = append(parts, label+"："+strings.Join(cleaned, "；"))
		}
	}
	appendValues("位置", boundary.Positions)
	appendValues("朝向", boundary.Facing)
	appendValues("目光", boundary.Gaze)
	appendValues("双手", boundary.Hands)
	appendValues("持物", boundary.HeldProps)
	appendValues("可见状态", boundary.VisibleState)
	return strings.Join(parts, "\n")
}

func shotComposerContent(prompt string, assets []storyboardAsset) string {
	if len(assets) == 0 {
		return prompt
	}
	lines := []string{"参考素材："}
	for _, asset := range assets {
		label := asset.Title
		if len(asset.Tags) > 0 {
			label += "（" + strings.Join(asset.Tags, "、") + "）"
		}
		lines = append(lines, "- "+label+"：@[node:"+asset.ID+"]")
	}
	lines = append(lines, "", "分镜视频提示词：", prompt)
	return strings.Join(lines, "\n")
}

func shotIDs(prefix string, count int) []string {
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, fmt.Sprintf("%s-shot-%d", prefix, index+1))
	}
	return ids
}

func stringSlice(value any) []string {
	items, ok := value.([]interface{})
	if !ok {
		text := stringValue(value)
		if text == "" {
			return nil
		}
		return []string{text}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func stringValue(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func (s *Service) log(userID string, taskID string, level string, message string, payload string) error {
	return s.repo.Create(&model.TaskLog{ID: newID(), UserID: userID, TaskID: taskID, Level: level, Message: message, Payload: truncateTaskLogPayload(payload)})
}

// 计费写入失败时订单会继续停在非终态并阻止重试；任务日志与进程日志必须同时留下可追踪证据。
func (s *Service) recordBillingTransitionFailure(userID string, taskID string, orderID string, action string, transitionErr error) error {
	if transitionErr == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s失败：%w", action, transitionErr)
	log.Printf("billing transition failed order=%s task=%s action=%s: %v", orderID, taskID, action, transitionErr)
	if strings.TrimSpace(taskID) == "" {
		return wrapped
	}
	// 任务日志对普通用户可见，只给核对入口；数据库、Redis 等底层原因仅留在上面的 Backend 日志。
	publicDetail := fmt.Sprintf("操作：%s；订单：%s。系统不会自动重试，请联系管理员按任务与订单核对服务端日志", action, firstNonEmpty(strings.TrimSpace(orderID), "未生成"))
	if logErr := s.log(userID, taskID, "error", "计费状态更新失败", publicDetail); logErr != nil {
		log.Printf("billing transition task log failed order=%s task=%s: %v", orderID, taskID, logErr)
		return errors.Join(wrapped, fmt.Errorf("计费失败日志写入失败：%w", logErr))
	}
	return wrapped
}

func truncateTaskLogPayload(payload string) string {
	if len(payload) <= taskLogPayloadLimit {
		return payload
	}
	end := taskLogPayloadLimit
	for end > 0 && !utf8.ValidString(payload[:end]) {
		end--
	}
	return payload[:end] + fmt.Sprintf("\n...（日志内容已截断，原始长度 %d 字符）", len(payload))
}

func (s *Service) registerActiveTask(id string, cancel context.CancelFunc) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	s.activeCancels[id] = cancel
}

func (s *Service) unregisterActiveTask(id string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.activeCancels, id)
}

func (s *Service) cancelActiveTask(id string) {
	s.cancelMu.Lock()
	cancel := s.activeCancels[id]
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) markSessionFailed(task model.Task, message string) error {
	return s.repo.MarkSessionFailedForTask(task, defaultString(message, "会话任务失败。"))
}

func buildAgentResult(task model.Task) (map[string]any, []map[string]any) {
	title := strings.TrimSpace(task.Prompt)
	if len([]rune(title)) > 28 {
		title = string([]rune(title)[:28]) + "..."
	}
	result := map[string]any{
		"taskId":    task.ID,
		"operation": task.Operation,
		"provider":  defaultString(task.Provider, "internal-agent"),
		"model":     defaultString(task.Model, "workflow-router"),
		"plan": []map[string]any{
			{"kind": "script", "title": "创意脚本", "content": task.Prompt},
			{"kind": "scene", "title": "主场景", "content": "根据用户输入拆解为可生成的视频场景。"},
			{"kind": "shot", "title": "镜头 1", "content": "建立画面、主体、风格和运镜。"},
			{"kind": "final", "title": "成片", "content": "等待视频生成 Provider 回填成片结果。"},
		},
	}
	ops := []map[string]any{
		nodeOp("script-"+task.ID, "text", "剧本 · "+title, 0, 0, "script", task.Prompt),
		nodeOp("scene-"+task.ID, "text", "场景 · 主场景", 380, 0, "scene", "主场景设定、角色关系、视觉风格。"),
		nodeOp("shot-"+task.ID, "config", "分镜 · 镜头 1", 760, 0, "shot", task.Prompt),
		nodeOp("final-"+task.ID, "video", "成片 · 待生成", 1140, 0, "final", ""),
		connectOp("script-"+task.ID, "scene-"+task.ID),
		connectOp("scene-"+task.ID, "shot-"+task.ID),
		connectOp("shot-"+task.ID, "final-"+task.ID),
	}
	return result, ops
}

func buildVideoWorkflowResult(task model.Task) (map[string]any, []map[string]any) {
	title := strings.TrimSpace(task.Prompt)
	if len([]rune(title)) > 28 {
		title = string([]rune(title)[:28]) + "..."
	}
	operation := defaultString(task.Operation, strings.TrimPrefix(task.Type, "video_"))
	result := map[string]any{
		"taskId":    task.ID,
		"operation": operation,
		"provider":  defaultString(task.Provider, "internal-agent"),
		"model":     defaultString(task.Model, "workflow-router"),
		"plan": []map[string]any{
			{"kind": "reference_set", "title": "参考素材组", "content": "收集原视频、参考图、参考音频和版本样片。"},
			{"kind": "shot", "title": "编辑镜头", "content": task.Prompt},
			{"kind": "final", "title": "结果版本", "content": "等待 provider 生成或人工确认后回填版本结果。"},
		},
	}
	ops := []map[string]any{
		nodeOp("video-brief-"+task.ID, "text", "编辑需求 · "+title, 0, 0, "script", task.Prompt),
		nodeOpWithMetadata("video-ref-"+task.ID, "text", "参考素材组", 380, 0, map[string]any{"workflowKind": "reference_set", "status": "idle", "content": "原片、参考图、参考音频、风格板或历史版本。", "videoEditOperation": operation}),
		nodeOpWithMetadata("video-shot-"+task.ID, "config", "视频任务 · "+operation, 760, 0, map[string]any{"workflowKind": "shot", "status": "idle", "generationMode": "video", "prompt": task.Prompt, "composerContent": task.Prompt, "videoEditOperation": operation}),
		nodeOpWithMetadata("video-result-"+task.ID, "video", "结果版本 · 待回填", 1140, 0, map[string]any{"workflowKind": "final", "status": "idle", "videoEditOperation": operation, "versionLabel": "v1"}),
		connectOp("video-brief-"+task.ID, "video-ref-"+task.ID),
		connectOp("video-ref-"+task.ID, "video-shot-"+task.ID),
		connectOp("video-shot-"+task.ID, "video-result-"+task.ID),
	}
	return result, ops
}

func nodeOp(id string, nodeType string, title string, x int, y int, workflowKind string, content string) map[string]any {
	return nodeOpWithMetadata(id, nodeType, title, x, y, map[string]any{"content": content, "workflowKind": workflowKind, "status": "idle"})
}

func nodeOpWithMetadata(id string, nodeType string, title string, x int, y int, metadata map[string]any) map[string]any {
	return map[string]any{
		"type":     "add_node",
		"id":       id,
		"nodeType": nodeType,
		"title":    title,
		"position": map[string]int{"x": x, "y": y},
		"metadata": metadata,
	}
}

func connectOp(from string, to string) map[string]any {
	return map[string]any{"type": "connect_nodes", "fromNodeId": from, "toNodeId": to}
}

func ptr[T any](value T) *T {
	return &value
}

func shortTitle(value string, max int) string {
	title := strings.TrimSpace(value)
	if title == "" {
		title = "影视分镜"
	}
	if len([]rune(title)) > max {
		return string([]rune(title)[:max]) + "..."
	}
	return title
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
