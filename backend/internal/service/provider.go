package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

type canvasGenerationInput struct {
	Mode            string                 `json:"mode"`
	Prompt          string                 `json:"prompt"`
	Config          providerConfig         `json:"config"`
	ReferenceImages []providerMedia        `json:"referenceImages"`
	ReferenceVideos []providerMedia        `json:"referenceVideos"`
	ReferenceAudios []providerMedia        `json:"referenceAudios"`
	Mask            *providerMedia         `json:"mask"`
	Metadata        map[string]interface{} `json:"metadata"`
	MaxOutputTokens int                    `json:"-"`
}

type providerConfig struct {
	ChannelID             string `json:"channelId"`
	APIFormat             string `json:"apiFormat"`
	InterfaceType         string `json:"interfaceType"`
	BaseURL               string `json:"baseUrl"`
	APIKey                string `json:"apiKey"`
	Model                 string `json:"model"`
	CapabilityConfig      *ModelCapabilityConfig `json:"capabilityConfig,omitempty"`
	Size                  string `json:"size"`
	Quality               string `json:"quality"`
	TransparentBackground string `json:"transparentBackground"`
	Count                 string `json:"count"`
	VideoSeconds          string `json:"videoSeconds"`
	VQuality              string `json:"vquality"`
	VideoGenerateAudio    string `json:"videoGenerateAudio"`
	VideoWatermark        string `json:"videoWatermark"`
	AudioVoice            string `json:"audioVoice"`
	AudioFormat           string `json:"audioFormat"`
	AudioSpeed            string `json:"audioSpeed"`
	AudioInstructions     string `json:"audioInstructions"`
	SystemPrompt          string `json:"systemPrompt"`
	RequireStreaming      bool   `json:"requireStreaming,omitempty"`
	// 手动交付会沿用测活确认的传输方式；非流式渠道不能先被后台强行发起 SSE 请求。
	PreferNonStreaming    bool   `json:"preferNonStreaming,omitempty"`
	ResponseMIMEType      string `json:"-"`
}

// 没有任务上下文时仍保留一个默认保护；后台任务必须遵守其策略上下文的截止时间，
// 否则短测活可以成功而长分镜固定在 5 分钟左右被本地 HTTP 客户端提前截断。
const providerHTTPDefaultTimeout = 5 * time.Minute
const maxProviderResponseBytes int64 = 64 << 20

// 普通画布文本也必须有界；探针和分镜分别传入更小/更大的专用上限。
// 否则慢推理模型会在创作台文本节点里走无界输出，容易撞上游网关 524。
const canvasTextMaxOutputTokens = 2_048

type providerMedia struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	DataURL    string `json:"dataUrl"`
	URL        string `json:"url"`
	StorageKey string `json:"storageKey"`
	MimeType   string `json:"mimeType"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
}

type imageResponse struct {
	Data  []map[string]interface{} `json:"data"`
	Error *providerError           `json:"error"`
	Code  *int                     `json:"code"`
	Msg   string                   `json:"msg"`
}

type providerError struct {
	Message string `json:"message"`
}

type providerHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

type providerAnalyticsKey struct{}

type providerAnalyticsContext struct {
	Service                    *Service
	UserID                     string
	TaskID                     string
	LeaseOwner                 string
	DispatchCheckpointRequired bool
	BillingOrderID             string
	Capability                 string
	Operation                  string
	ChannelID                  string
	Model                      string
	VideoSeconds               int
	RequestKind                string
	ProviderRequestID          string
	PollStage                  string
	ConcurrencyLimit           int
}

func withProviderAnalytics(ctx context.Context, service *Service, task model.Task) context.Context {
	metadata := providerAnalyticsContext{
		Service: service, UserID: task.UserID, TaskID: task.ID, LeaseOwner: task.LeaseOwner,
		DispatchCheckpointRequired: service != nil && task.Status == model.TaskStatusRunning && strings.TrimSpace(task.LeaseOwner) != "",
		BillingOrderID:             task.BillingOrderID, Capability: capabilityFromTaskType(task.Type), Operation: task.Operation,
		Model: task.Model, ProviderRequestID: task.ProviderRequestID, PollStage: task.PollStage,
	}
	var input struct {
		Mode   string         `json:"mode"`
		Config providerConfig `json:"config"`
	}
	if json.Unmarshal([]byte(task.InputJSON), &input) == nil {
		metadata.ChannelID = firstNonEmpty(input.Config.ChannelID, systemChannelIDFromBaseURL(input.Config.BaseURL))
		metadata.Model = firstNonEmpty(input.Config.Model, metadata.Model)
		metadata.VideoSeconds, _ = strconv.Atoi(input.Config.VideoSeconds)
		if normalized := normalizeCapability(input.Mode); normalized != "" {
			metadata.Capability = normalized
		}
	}
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata)
}

func resumedProviderRequestID(ctx context.Context) string {
	metadata, _ := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	return strings.TrimSpace(metadata.ProviderRequestID)
}

func withProviderRequestKind(ctx context.Context, requestKind string) context.Context {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok {
		return ctx
	}
	metadata.RequestKind = requestKind
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata)
}

func withProviderRequestID(ctx context.Context, providerRequestID string) context.Context {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok {
		return ctx
	}
	metadata.ProviderRequestID = strings.TrimSpace(providerRequestID)
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata)
}

func (e providerHTTPError) Error() string {
	return providerHTTPErrorMessage(e)
}

func (s *Service) processCanvasGenerationTask(ctx context.Context, userID string, taskType string, fallbackPrompt string, rawInput string) (map[string]interface{}, error) {
	var input canvasGenerationInput
	if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
		return nil, fmt.Errorf("任务输入解析失败：%w", err)
	}
	if strings.TrimSpace(input.Prompt) == "" {
		input.Prompt = fallbackPrompt
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if input.Mode == "" && strings.HasPrefix(taskType, "video_") {
		input.Mode = "video"
	}
	var config providerConfig
	var err error
	if resumedProviderRequestID(ctx) != "" {
		config, err = s.resolveProviderPollingConfig(ctx, input.Config)
	} else {
		config, err = s.resolveProviderConfig(ctx, input.Config)
	}
	if err != nil {
		return nil, markProviderPreparationFailure(err)
	}
	input.Config = config
	input.Config.APIFormat = strings.ToLower(strings.TrimSpace(input.Config.APIFormat))
	if input.Config.APIFormat == "" {
		input.Config.APIFormat = "openai"
	}
	if strings.EqualFold(strings.TrimSpace(input.Config.APIFormat), "gemini") && strings.TrimSpace(input.Config.InterfaceType) == "" {
		// 自定义 Gemini 渠道历史上只保存报文族；任务模式足以安全确定原生文本或 Veo 协议。
		switch input.Mode {
		case "text":
			input.Config.InterfaceType = string(model.ChannelInterfaceGeminiContent)
		case "video":
			input.Config.InterfaceType = string(model.ChannelInterfaceGeminiVeo)
		}
	}
	if input.Config.APIFormat == "gemini" && input.Config.InterfaceType != string(model.ChannelInterfaceGeminiContent) && input.Config.InterfaceType != string(model.ChannelInterfaceGeminiVeo) {
		return nil, errors.New("当前任务不支持所选 Gemini 协议，请为文本选择 Gemini GenerateContent、为视频选择 Gemini Veo")
	}
	if strings.TrimSpace(input.Config.BaseURL) == "" || strings.TrimSpace(input.Config.APIKey) == "" || strings.TrimSpace(input.Config.Model) == "" {
		return nil, markProviderPreparationFailure(errors.New("后端生成任务缺少 Base URL、API Key 或模型名"))
	}
	if err := validateGenerationInterface(input.Mode, input.Config.InterfaceType); err != nil {
		return nil, markProviderPreparationFailure(err)
	}
	if resumedProviderRequestID(ctx) == "" {
		requirePublicURL := input.Config.InterfaceType == "newapi-channel-1" || input.Config.InterfaceType == "newapi-channel-2"
		if err := s.hydrateGenerationMedia(ctx, userID, &input, requirePublicURL); err != nil {
			return nil, markProviderPreparationFailure(err)
		}
	}
	if input.Mode == "text" && input.MaxOutputTokens <= 0 {
		input.MaxOutputTokens = canvasTextMaxOutputTokens
	}
	switch input.Mode {
	case "image":
		return runImageTask(ctx, input)
	case "text":
		return runTextTask(ctx, input)
	case "video":
		return runVideoTask(ctx, input)
	case "audio":
		return runAudioTask(ctx, input)
	default:
		return nil, markProviderPreparationFailure(fmt.Errorf("不支持的生成模式：%s", input.Mode))
	}
}

func (s *Service) hydrateGenerationMedia(ctx context.Context, userID string, input *canvasGenerationInput, requirePublicURL bool) error {
	groups := [][]providerMedia{input.ReferenceImages, input.ReferenceVideos, input.ReferenceAudios}
	for _, group := range groups {
		for index := range group {
			if err := s.hydrateProviderMedia(ctx, userID, &group[index], requirePublicURL); err != nil {
				return err
			}
		}
	}
	if input.Mask != nil {
		return s.hydrateProviderMedia(ctx, userID, input.Mask, requirePublicURL)
	}
	return nil
}

func (s *Service) hydrateProviderMedia(ctx context.Context, userID string, media *providerMedia, requirePublicURL bool) error {
	if !strings.HasPrefix(media.StorageKey, "resource:") {
		if requirePublicURL && strings.HasPrefix(strings.TrimSpace(media.DataURL), "data:") {
			return errors.New("当前 JSON 视频协议的参考素材不能使用内嵌数据，请先上传到 OSS 或提供公网素材地址")
		}
		return nil
	}
	resourceID := strings.TrimPrefix(media.StorageKey, "resource:")
	if requirePublicURL {
		resource, err := s.repo.ResourceForUser(userID, resourceID)
		if err != nil {
			return fmt.Errorf("读取任务参考资源失败：%w", err)
		}
		if resource.Status != "ready" {
			return errors.New("任务参考资源尚未上传完成")
		}
		if resource.Provider == "local" {
			return errors.New("当前 JSON 视频协议的参考素材需要公网可访问地址，请启用 OSS 后重新上传该素材")
		}
		setting, err := s.ossSettingForResource(userID, resource)
		if err != nil {
			return err
		}
		if setting.Provider != "aliyun" {
			return errors.New("当前 JSON 视频协议的私有参考素材暂时只支持阿里云 OSS 签名地址")
		}
		signedURL, err := signedOSSObjectURL(setting, resource.ObjectKey, time.Now().Add(providerResourceURLTTL))
		if err != nil {
			return fmt.Errorf("生成 JSON 视频协议参考素材地址失败：%w", err)
		}
		media.URL = signedURL
		media.DataURL = ""
		media.MimeType = firstNonEmpty(media.MimeType, resource.MimeType)
		media.Bytes = resource.Size
		media.DurationMs = resource.DurationMs
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(media.DataURL), "data:") {
		return nil
	}
	resource, body, err := s.OpenResourceContext(ctx, userID, resourceID)
	if err != nil {
		return fmt.Errorf("读取任务参考资源失败：%w", err)
	}
	defer body.Close()
	policy, err := s.RuntimePolicy()
	if err != nil {
		return err
	}
	resourceLimit := megabytes(policy.Resource.ResourceUploadMB)
	data, err := io.ReadAll(io.LimitReader(body, resourceLimit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > resourceLimit {
		return fmt.Errorf("任务参考资源超过 %dMB", policy.Resource.ResourceUploadMB)
	}
	mimeType := normalizedMediaMimeType(firstNonEmpty(media.MimeType, resource.MimeType), data)
	media.DataURL = dataURL(mimeType, data)
	media.MimeType = mimeType
	media.Bytes = resource.Size
	media.DurationMs = resource.DurationMs
	return nil
}

func normalizedMediaMimeType(declared string, data []byte) string {
	declared = strings.TrimSpace(strings.Split(declared, ";")[0])
	if declared != "" && declared != "application/octet-stream" {
		return declared
	}
	detected := strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0])
	return defaultString(detected, "application/octet-stream")
}

func (s *Service) resolveProviderConfig(ctx context.Context, config providerConfig) (providerConfig, error) {
	channelID := strings.TrimSpace(config.ChannelID)
	if channelID == "" {
		channelID = systemChannelIDFromBaseURL(config.BaseURL)
	}
	if channelID == "" {
		if _, err := ValidateOutboundURLContext(ctx, config.BaseURL); err != nil {
			return providerConfig{}, err
		}
		return config, nil
	}
	channel, err := s.repo.SystemChannel(channelID)
	if err != nil {
		return providerConfig{}, errors.New("系统渠道不存在或已停用")
	}
	modelName := strings.TrimPrefix(strings.TrimSpace(config.Model), "models/")
	var channelModel *model.ChannelModel
	if modelName == "" {
		models, modelErr := s.repo.ChannelModels(channel.ID, false)
		if modelErr != nil {
			return providerConfig{}, modelErr
		}
		if len(models) == 0 {
			return providerConfig{}, errors.New("系统渠道未配置可用模型")
		}
		channelModel = &models[0]
		modelName = channelModel.ModelKey
	}
	if channelModel == nil {
		channelModel, err = s.repo.ChannelModelByKey(channel.ID, modelName)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return providerConfig{}, errors.New("当前系统渠道未授权该模型")
		}
		if err != nil {
			return providerConfig{}, err
		}
	}
	config.ChannelID = channel.ID
	config.APIFormat = channel.APIFormat
	config.InterfaceType = string(channel.InterfaceType)
	if strings.EqualFold(strings.TrimSpace(channelModel.Capability), "text") {
		protocol, protocolErr := resolveSystemTextModelProtocol(*channel, *channelModel)
		if protocolErr != nil {
			return providerConfig{}, protocolErr
		}
		config.InterfaceType = string(protocol)
	} else if channelModel.Protocol != "" {
		config.InterfaceType = string(channelModel.Protocol)
	}
	// 模型协议是实际请求契约；混合渠道中鉴权格式也必须随模型协议切换。
	config.APIFormat = apiFormatForProtocol(model.ChannelInterfaceType(config.InterfaceType), channel.APIFormat)
	config.BaseURL = channel.BaseURL
	config.APIKey = channel.APIKey
	config.Model = modelName
	return config, nil
}

// 既有任务轮询只读取已经创建的上游任务，不应因模型后来被停用或移出可创建清单而失去恢复能力。
// 系统渠道仍必须处于启用状态并使用当前服务端密钥；只有“模型可创建”校验被刻意跳过。
func (s *Service) resolveProviderPollingConfig(ctx context.Context, config providerConfig) (providerConfig, error) {
	channelID := strings.TrimSpace(config.ChannelID)
	if channelID == "" {
		channelID = systemChannelIDFromBaseURL(config.BaseURL)
	}
	if channelID == "" {
		if _, err := ValidateOutboundURLContext(ctx, config.BaseURL); err != nil {
			return providerConfig{}, err
		}
		return config, nil
	}
	channel, err := s.repo.SystemChannel(channelID)
	if err != nil {
		return providerConfig{}, errors.New("系统渠道不存在或已停用")
	}
	modelName := strings.TrimPrefix(strings.TrimSpace(config.Model), "models/")
	if modelName == "" {
		return providerConfig{}, errors.New("任务没有记录供应商模型")
	}
	interfaceType := model.ChannelInterfaceType(strings.TrimSpace(config.InterfaceType))
	if interfaceType == "" {
		if channelModel, modelErr := s.repo.ChannelModelByKeyIncludingDisabled(channel.ID, modelName); modelErr == nil && channelModel.Protocol != "" {
			interfaceType = channelModel.Protocol
		}
	}
	if interfaceType == "" {
		interfaceType = channel.InterfaceType
	}
	config.ChannelID = channel.ID
	config.InterfaceType = string(interfaceType)
	config.BaseURL = channel.BaseURL
	config.APIKey = channel.APIKey
	config.Model = modelName
	config.APIFormat = apiFormatForProtocol(interfaceType, channel.APIFormat)
	return config, nil
}

func systemChannelIDFromBaseURL(baseURL string) string {
	value := strings.TrimSpace(baseURL)
	// 只有服务端生成的相对代理路径才可推断系统渠道。不能从任意绝对
	// URL 或中间路径片段提取 channelId，否则自定义配置可能被误当成系统
	// 渠道，绕过个人渠道边界或把请求错误计入平台积分。
	const prefix = "/api/ai/system/"
	if value == "" || !strings.HasPrefix(value, prefix) || strings.ContainsAny(value, "?#") {
		return ""
	}
	id := strings.TrimPrefix(value, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return strings.TrimSpace(id)
}

func runImageTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if isXAIImageConfig(input.Config) {
		return runXAIImageTask(ctx, input)
	}
	var payload imageResponse
	if input.Mask != nil {
		// 蒙版编辑是强校验写路径：协议能力不明确时必须失败，不能静默退化为整图重绘。
		if strings.TrimSpace(input.Config.InterfaceType) != string(model.ChannelInterfaceOpenAIImage) {
			return nil, errors.New("当前渠道未声明 OpenAI Images 编辑协议，已拒绝可能忽略蒙版的整图重绘")
		}
		if len(input.ReferenceImages) == 0 {
			return nil, errors.New("蒙版编辑必须提供与蒙版同尺寸的源图片")
		}
	}
	if len(input.ReferenceImages) > 0 || input.Mask != nil {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		write := func(key string, value string) error {
			if err := writeField(writer, key, value); err != nil {
				return fmt.Errorf("图片请求体序列化失败：%w", err)
			}
			return nil
		}
		for _, field := range [][2]string{
			{"model", input.Config.Model},
			{"prompt", withSystemPrompt(input.Config, input.Prompt)},
			{"n", "1"},
			{"response_format", "b64_json"},
			{"output_format", "png"},
		} {
			if err := write(field[0], field[1]); err != nil {
				return nil, err
			}
		}
		if input.Config.TransparentBackground == "true" {
			if err := write("background", "transparent"); err != nil {
				return nil, err
			}
		}
		if input.Config.Quality != "" {
			if err := write("quality", normalizeImageQuality(input.Config.Quality)); err != nil {
				return nil, err
			}
		}
		if size := normalizePixelSize(input.Config.Size); size != "" {
			if err := write("size", size); err != nil {
				return nil, err
			}
		}
		for _, image := range input.ReferenceImages {
			if err := writeMediaPart(writer, "image", image); err != nil {
				return nil, err
			}
		}
		if input.Mask != nil {
			if err := writeMediaPart(writer, "mask", *input.Mask); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		if err := postForm(ctx, input.Config, "/images/edits", writer.FormDataContentType(), body, &payload); err != nil {
			return nil, err
		}
	} else {
		body := map[string]interface{}{
			"model":           input.Config.Model,
			"prompt":          withSystemPrompt(input.Config, input.Prompt),
			"n":               1,
			"response_format": "b64_json",
			"output_format":   "png",
		}
		if input.Config.TransparentBackground == "true" {
			body["background"] = "transparent"
		}
		if input.Config.Quality != "" {
			body["quality"] = normalizeImageQuality(input.Config.Quality)
		}
		if size := normalizePixelSize(input.Config.Size); size != "" {
			body["size"] = size
		}
		if err := postJSON(ctx, input.Config, "/images/generations", body, &payload); err != nil {
			return nil, err
		}
	}
	images, err := imageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

func runTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if strings.EqualFold(strings.TrimSpace(input.Config.APIFormat), "gemini") && strings.TrimSpace(input.Config.InterfaceType) == "" {
		input.Config.InterfaceType = string(model.ChannelInterfaceGeminiContent)
	}
	switch input.Config.InterfaceType {
	case "chat-completion":
		if input.Config.PreferNonStreaming {
			return runNonStreamingChatCompletionsTextTask(ctx, input)
		}
		return runChatCompletionsTextTask(ctx, input)
	case "openai-response":
		if input.Config.PreferNonStreaming {
			return runNonStreamingResponsesTextTask(ctx, input)
		}
		return runResponsesTextTask(ctx, input)
	case "gemini-content":
		return runGeminiTextTask(ctx, input)
	}
	return runLegacyTextTask(ctx, input)
}

func runLegacyTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	result, err := runResponsesTextTask(ctx, input)
	if err != nil {
		if !shouldFallbackTextToChat(err) {
			return nil, err
		}
		result, chatErr := runChatCompletionsTextTask(ctx, input)
		if chatErr == nil {
			return result, nil
		}
		// 兼容协议的第二条路径才是最终失败原因；保留其错误链，供 524 计费边界和流式门禁准确分类。
		return nil, fmt.Errorf("文本接口请求失败：Responses API %v；Chat Completions %w", err, chatErr)
	}
	return result, nil
}

func runResponsesTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	result, err := runStreamingResponsesTextTask(ctx, input)
	if err == nil {
		return result, nil
	}
	if !shouldFallbackStreamToNonStream(err) {
		return nil, err
	}
	if input.Config.RequireStreaming {
		return nil, streamingRequiredError(err)
	}
	result, fallbackErr := runNonStreamingResponsesTextTask(ctx, input)
	if fallbackErr != nil {
		return nil, fmt.Errorf("文本接口不支持流式响应，非流式回退也失败：%w", fallbackErr)
	}
	result["transport"] = "non-stream-fallback"
	return result, nil
}

func runNonStreamingResponsesTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	var payload map[string]interface{}
	responseInput, err := textResponseInput(input)
	if err != nil {
		return nil, err
	}
	if err := postJSON(ctx, input.Config, "/responses", responsesTextRequestBody(input, responseInput, false), &payload); err != nil {
		return nil, err
	}
	if err := validateResponsesCompletion(payload); err != nil {
		return nil, err
	}
	text := stringField(payload, "output_text")
	if text == "" {
		text = extractResponseText(payload)
	}
	if text == "" {
		return nil, errors.New("文本接口没有返回内容")
	}
	return map[string]interface{}{"mode": "text", "text": text}, nil
}

func runChatCompletionsTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	result, err := runStreamingChatCompletionsTextTask(ctx, input)
	if err == nil {
		return result, nil
	}
	if !shouldFallbackStreamToNonStream(err) {
		return nil, err
	}
	if input.Config.RequireStreaming {
		return nil, streamingRequiredError(err)
	}
	result, fallbackErr := runNonStreamingChatCompletionsTextTask(ctx, input)
	if fallbackErr != nil {
		return nil, fmt.Errorf("文本接口不支持流式响应，非流式回退也失败：%w", fallbackErr)
	}
	result["transport"] = "non-stream-fallback"
	return result, nil
}

func runNonStreamingChatCompletionsTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	var payload map[string]interface{}
	messages := []map[string]interface{}{}
	if systemPrompt := strings.TrimSpace(input.Config.SystemPrompt); systemPrompt != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": systemPrompt})
	}
	userContent, err := textChatContent(input)
	if err != nil {
		return nil, err
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": userContent})
	body := chatCompletionsTextRequestBody(input, messages, false)
	if err := postJSON(ctx, input.Config, "/chat/completions", body, &payload); err != nil {
		return nil, err
	}
	if err := validateChatCompletionFinishReasons(payload); err != nil {
		return nil, err
	}
	text := extractChatCompletionText(payload)
	if text == "" {
		return nil, errors.New("文本接口没有返回内容")
	}
	return map[string]interface{}{"mode": "text", "text": text}, nil
}

func textResponseInput(input canvasGenerationInput) (interface{}, error) {
	systemPrompt := strings.TrimSpace(input.Config.SystemPrompt)
	if len(input.ReferenceImages) == 0 {
		return withSystemPrompt(input.Config, input.Prompt), nil
	}
	messages := make([]map[string]interface{}, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": systemPrompt})
	}
	content, err := textResponseContent(input)
	if err != nil {
		return nil, err
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": content})
	return messages, nil
}

func textResponseContent(input canvasGenerationInput) ([]map[string]interface{}, error) {
	content := []map[string]interface{}{{"type": "input_text", "text": input.Prompt}}
	for _, image := range input.ReferenceImages {
		url, err := openAIImageInputURL(image)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]interface{}{"type": "input_image", "image_url": url})
	}
	return content, nil
}

func textChatContent(input canvasGenerationInput) (interface{}, error) {
	if len(input.ReferenceImages) == 0 {
		return input.Prompt, nil
	}
	content := []map[string]interface{}{{"type": "text", "text": input.Prompt}}
	for _, image := range input.ReferenceImages {
		url, err := openAIImageInputURL(image)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}})
	}
	return content, nil
}

func openAIImageInputURL(media providerMedia) (string, error) {
	value := strings.TrimSpace(media.DataURL)
	if strings.HasPrefix(value, "data:image/") {
		return value, nil
	}
	if strings.HasPrefix(value, "data:") {
		return "", errors.New("参考图片 MIME 类型无效，请重新读取或上传图片")
	}
	value = strings.TrimSpace(media.URL)
	if strings.HasPrefix(value, "data:image/") || isPublicMediaURL(value) {
		return value, nil
	}
	if strings.HasPrefix(value, "data:") {
		return "", errors.New("参考图片 MIME 类型无效，请重新读取或上传图片")
	}
	return "", errors.New("OpenAI 文本多模态参考图片需要公网 URL 或 base64 data URL")
}

func shouldFallbackTextToChat(err error) bool {
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func runAudioTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if resolved, ok := input.Metadata["resolvedCharacterVersions"].([]interface{}); ok && len(resolved) > 0 {
		voiceKey := metadataString(input.Metadata, "resolvedCharacterVoiceKey")
		if voiceKey == "" || strings.TrimSpace(input.Config.AudioVoice) != voiceKey {
			return nil, errors.New("角色配音缺少已解析的声音绑定")
		}
	}
	format := defaultString(input.Config.AudioFormat, "mp3")
	body := map[string]interface{}{
		"model":           input.Config.Model,
		"input":           input.Prompt,
		"voice":           defaultString(input.Config.AudioVoice, "alloy"),
		"response_format": format,
		"speed":           1,
	}
	if input.Config.AudioSpeed != "" {
		body["speed"] = parseFloat(input.Config.AudioSpeed, 1)
	}
	if input.Config.AudioInstructions != "" {
		body["instructions"] = input.Config.AudioInstructions
	}
	data, mimeType, err := postBinary(ctx, input.Config, "/audio/speech", body)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "audio", "audio": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType, "format": format}}, nil
}

// 只在上游明确拒绝 stream 参数时回退；524、网络中断和 2xx 后解析失败都可能已经计费，禁止自动发送第二次请求。
func shouldFallbackStreamToNonStream(err error) bool {
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) || (httpErr.StatusCode != http.StatusBadRequest && httpErr.StatusCode != http.StatusUnprocessableEntity) {
		return false
	}
	body := strings.ToLower(httpErr.Body)
	if !strings.Contains(body, "stream") {
		return false
	}
	for _, marker := range []string{"not support", "unsupported", "unknown", "unrecognized", "not permitted", "invalid parameter", "extra field", "不支持"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func streamingRequiredError(cause error) error {
	return publicTaskError{
		message: "当前长文本任务要求真实 SSE 流式响应，但渠道明确拒绝 stream=true；为避免非流式请求被中间网关截断并继续产生费用，系统未发起第二次非流式请求。请更换支持流式的协议或渠道并重新测活",
		cause:   cause,
	}
}

func runVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if input.Config.InterfaceType == "gemini-veo" {
		return runGeminiVeoVideoTask(ctx, input)
	}
	if input.Config.InterfaceType == "newapi-channel-2" {
		return runNewAPIChannel2VideoTask(ctx, input)
	}
	if input.Config.InterfaceType == "newapi-channel-1" {
		return runNewAPIChannel1VideoTask(ctx, input)
	}
	if isArkPlanVideoConfig(input.Config) {
		return runSeedanceAgentPlanVideoTask(ctx, input)
	}
	if isSeedanceVideoConfig(input.Config) {
		return runSeedanceVideosTask(ctx, input)
	}
	isXAIVideo := isXAIVideoConfig(input.Config)
	if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		if isXAIVideo {
			return nil, errors.New("当前 xAI 官方视频接入只支持文本生视频和单张起始图图生视频，本次未调用供应商")
		}
		return nil, errors.New("OpenAI Compatible 视频接口不支持参考视频或参考音频，请切换到 Seedance / Agent Plan 渠道")
	}
	id := resumedProviderRequestID(ctx)
	var created map[string]interface{}
	if id == "" && isXAIVideo {
		requestBody, err := xaiVideoBody(input)
		if err != nil {
			return nil, err
		}
		if err := postJSON(ctx, input.Config, "/videos/generations", requestBody, &created); err != nil {
			return nil, err
		}
	} else if id == "" {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		write := func(key string, value string) error {
			if err := writeField(writer, key, value); err != nil {
				return fmt.Errorf("视频请求体序列化失败：%w", err)
			}
			return nil
		}
		for _, field := range [][2]string{
			{"model", input.Config.Model},
			{"prompt", newAPIVideoPromptText(input)},
			{"seconds", defaultString(input.Config.VideoSeconds, "6")},
		} {
			if err := write(field[0], field[1]); err != nil {
				return nil, err
			}
		}
		if size := normalizeVideoSize(input.Config.Size); size != "" {
			if err := write("size", size); err != nil {
				return nil, err
			}
		}
		if err := write("resolution_name", normalizeVideoResolution(input.Config.VQuality)); err != nil {
			return nil, err
		}
		if err := write("preset", "normal"); err != nil {
			return nil, err
		}
		if shouldSendNewAPIVideoImages(input) {
			for _, image := range input.ReferenceImages {
				if err := writeMediaPart(writer, "input_reference[]", image); err != nil {
					return nil, err
				}
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		if err := postForm(ctx, input.Config, "/videos", writer.FormDataContentType(), body, &created); err != nil {
			return nil, err
		}
	}
	if id == "" {
		id = firstNonEmptyString(stringField(created, "id"), stringField(created, "request_id"), stringField(created, "task_id"))
	}
	if id == "" {
		if data, ok := created["data"].(map[string]interface{}); ok {
			id = firstNonEmptyString(stringField(data, "id"), stringField(data, "request_id"), stringField(data, "task_id"))
		}
	}
	if id == "" {
		return nil, errors.New("视频接口没有返回任务 ID")
	}
	ctx = withProviderRequestID(ctx, id)
	var state map[string]interface{}
	if err := getJSON(ctx, input.Config, "/videos/"+id, &state); err != nil {
		return nil, err
	}
	if data, ok := state["data"].(map[string]interface{}); ok {
		state = data
	}
	status := strings.ToLower(stringField(state, "status"))
	if status == "completed" || status == "succeeded" || status == "success" || status == "done" {
		if videoURL := newAPIVideoResultURL(state); videoURL != "" {
			data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
			if err != nil {
				return nil, fmt.Errorf("视频结果下载失败（任务 %s）：%w", id, err)
			}
			mimeType = normalizedMediaMimeType(mimeType, data)
			return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
		}
		data, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), input.Config, "/videos/"+id+"/content")
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
	}
	if status == "failed" || status == "cancelled" || status == "expired" {
		return nil, errors.New("视频生成失败")
	}
	return nil, deferProviderPoll(ctx, status, status, 2500*time.Millisecond)
}

func newAPIVideoResultURL(state map[string]interface{}) string {
	return nestedNewAPIVideoResultURL(state, false, 0)
}

func nestedNewAPIVideoResultURL(payload map[string]interface{}, allowResultURL bool, depth int) string {
	if depth < 2 {
		for _, key := range []string{"data", "result", "video"} {
			if nested, ok := payload[key].(map[string]interface{}); ok {
				if videoURL := nestedNewAPIVideoResultURL(nested, true, depth+1); videoURL != "" {
					return videoURL
				}
			}
		}
	}
	keys := []string{"video_url", "videoUrl", "url"}
	if allowResultURL {
		keys = append(keys, "result_url", "resultUrl")
	}
	for _, key := range keys {
		if videoURL := strings.TrimSpace(stringField(payload, key)); isPublicMediaURL(videoURL) {
			return videoURL
		}
	}
	return ""
}

const newAPIChannel1VideoPollInterval = 20 * time.Second

const (
	newAPIChannel2VideoPollInterval    = 10 * time.Second
	newAPIChannel2VideoRetryInterval   = time.Minute
	newAPIChannel2VideoMaxQueryRetries = 3
	newAPIChannel2RetryStagePrefix     = "newapi2_retry_"
)

type newAPIChannel2ResponseError struct {
	Code    string
	Message string
}

func (e newAPIChannel2ResponseError) Error() string {
	return fmt.Sprintf("NewAPI Video Generations 任务查询失败（%s）：%s", e.Code, defaultString(e.Message, "上游查询失败"))
}

func runNewAPIChannel2VideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	var created map[string]interface{}
	if id == "" {
		body, err := newAPIChannel2VideoBody(input)
		if err != nil {
			return nil, err
		}
		if err := postJSON(ctx, input.Config, "/video/generations", body, &created); err != nil {
			return nil, err
		}
		id = firstNonEmptyString(stringField(created, "task_id"), stringField(created, "id"))
	}
	if id == "" {
		if data, ok := created["data"].(map[string]interface{}); ok {
			id = firstNonEmptyString(stringField(data, "task_id"), stringField(data, "id"))
		}
	}
	if id == "" {
		return nil, errors.New("NewAPI Video Generations 没有返回任务 ID")
	}
	ctx = withProviderRequestID(ctx, id)

	result, providerStatus, err := queryNewAPIChannel2VideoTask(ctx, input, id)
	if err != nil {
		if !isTransientNewAPIChannel2QueryError(err) {
			return nil, err
		}
		retry := providerPollRetryCount(ctx, newAPIChannel2RetryStagePrefix) + 1
		if retry > newAPIChannel2VideoMaxQueryRetries {
			return nil, err
		}
		deferred := deferProviderPoll(ctx, "", newAPIChannel2RetryStagePrefix+strconv.Itoa(retry), newAPIChannel2VideoRetryInterval)
		if !errors.Is(deferred, context.DeadlineExceeded) {
			logNewAPIChannel2QueryRetry(ctx, id, retry, err)
		}
		return nil, deferred
	}
	if result != nil {
		return result, nil
	}
	return nil, deferProviderPoll(ctx, providerStatus, providerStatus, newAPIChannel2VideoPollInterval)
}

// 单次查询只读取既有上游任务，不创建新任务；自动轮询和人工恢复共用这条安全边界。
func queryNewAPIChannel2VideoTask(ctx context.Context, input canvasGenerationInput, id string) (map[string]interface{}, string, error) {
	var payload map[string]interface{}
	if err := getJSON(ctx, input.Config, "/video/generations/"+id, &payload); err != nil {
		return nil, "", err
	}
	if err := newAPIChannel2PayloadError(payload); err != nil {
		return nil, "", err
	}
	state := payload
	if data, ok := payload["data"].(map[string]interface{}); ok {
		state = data
	}
	status := strings.ToUpper(strings.TrimSpace(stringField(state, "status")))
	switch status {
	case "SUCCESS":
		videoURL := strings.TrimSpace(stringField(state, "result_url"))
		if videoURL == "" {
			return nil, status, fmt.Errorf("NewAPI Video Generations 任务 %s 已成功但没有返回视频地址", id)
		}
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err != nil {
			return nil, status, fmt.Errorf("NewAPI Video Generations 视频结果下载失败（任务 %s）：%w", id, err)
		}
		mimeType = normalizedMediaMimeType(mimeType, data)
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, status, nil
	case "FAILURE":
		reason := strings.TrimSpace(stringField(state, "fail_reason"))
		return nil, status, fmt.Errorf("NewAPI Video Generations 视频生成失败（任务 %s）：%s", id, defaultString(reason, "上游返回失败"))
	case "SUBMITTED", "QUEUED", "IN_PROGRESS", "NOT_START", "":
		return nil, status, nil
	default:
		return nil, status, fmt.Errorf("NewAPI Video Generations 任务 %s 返回未知状态：%s", id, status)
	}
}

func newAPIChannel2PayloadError(payload map[string]interface{}) error {
	code := strings.ToLower(strings.TrimSpace(stringField(payload, "code")))
	if code == "" || code == "0" || code == "ok" || code == "success" {
		return nil
	}
	return newAPIChannel2ResponseError{Code: code, Message: firstNonEmptyString(stringField(payload, "message"), stringField(payload, "msg"))}
}

func isTransientNewAPIChannel2QueryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var responseErr newAPIChannel2ResponseError
	if errors.As(err, &responseErr) {
		return responseErr.Code == "do_request_failed" || strings.Contains(strings.ToLower(responseErr.Message), "do request failed")
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		body := strings.ToLower(httpErr.Body)
		return httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError || strings.Contains(body, "do_request_failed") || strings.Contains(body, "do request failed")
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "do_request_failed") || strings.Contains(message, "do request failed")
}

func logNewAPIChannel2QueryRetry(ctx context.Context, providerTaskID string, retry int, err error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || metadata.TaskID == "" {
		return
	}
	payload := fmt.Sprintf("供应商任务 %s，第 %d/%d 次重试：%s", providerTaskID, retry, newAPIChannel2VideoMaxQueryRetries, SafeProviderLogError(err))
	if logErr := metadata.Service.log(metadata.UserID, metadata.TaskID, "warn", "上游任务查询失败，1 分钟后重试", payload); logErr != nil {
		stdlog.Printf("provider query retry task log failed task=%s: %v", metadata.TaskID, logErr)
	}
}

func newAPIChannel2VideoBody(input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceImages) > 9 || len(input.ReferenceVideos) > 3 || len(input.ReferenceAudios) > 3 {
		return nil, errors.New("NewAPI Video Generations 最多支持 9 张参考图、3 个参考视频和 3 个参考音频")
	}
	modelName := strings.ToLower(strings.TrimSpace(input.Config.Model))
	requiresSingleImage := modelName == "grok-video-1.5" || modelName == "grok-video-1.5-1080p"
	images := make([]string, 0, len(input.ReferenceImages))
	// 单图模型以实际参考图为准，兼容旧画布中未随连接关系更新的 text_to_video 元数据。
	if shouldSendNewAPIVideoImages(input) || requiresSingleImage {
		for _, image := range input.ReferenceImages {
			url, err := videoGenerationsMediaURL(image)
			if err != nil {
				return nil, err
			}
			images = append(images, url)
		}
	}
	if requiresSingleImage {
		if len(images) != 1 {
			return nil, fmt.Errorf("NewAPI Video Generations 的 %s 必须且只能提供 1 张参考图（当前 %d 张）", input.Config.Model, len(images))
		}
	}
	frameImages, err := videoFrameImageURLs(input, images)
	if err != nil {
		return nil, err
	}
	if len(frameImages) > 0 {
		images = frameImages
	}

	seconds, secondsErr := strconv.Atoi(strings.TrimSpace(input.Config.VideoSeconds))
	if secondsErr != nil || seconds < 1 {
		seconds = 6
	}
	if len(images) > 1 && seconds > 10 {
		seconds = 10
	} else if seconds > 15 {
		seconds = 15
	}
	ratio := normalizeNewAPIChannel2Ratio(input.Config.Size, modelName)
	resolution := normalizeNewAPIChannel2Resolution(input.Config.VQuality, modelName)
	body := map[string]interface{}{
		"model":          input.Config.Model,
		"prompt":         strings.TrimSpace(input.Prompt),
		"seconds":        strconv.Itoa(seconds),
		"aspect_ratio":   ratio,
		"resolution":     resolution,
		"generate_audio": parseBool(input.Config.VideoGenerateAudio, true),
	}
	if len(images) > 0 {
		body["image_urls"] = images
	}
	videoURLs := make([]string, 0, len(input.ReferenceVideos))
	for _, video := range input.ReferenceVideos {
		url, err := videoGenerationsMediaURL(video)
		if err != nil {
			return nil, err
		}
		videoURLs = append(videoURLs, url)
	}
	if len(videoURLs) > 0 {
		body["video_urls"] = videoURLs
	}
	audioURLs := make([]string, 0, len(input.ReferenceAudios))
	for _, audio := range input.ReferenceAudios {
		url, err := videoGenerationsMediaURL(audio)
		if err != nil {
			return nil, err
		}
		audioURLs = append(audioURLs, url)
	}
	if len(audioURLs) > 0 {
		body["audio_urls"] = audioURLs
	}
	return body, nil
}

func videoGenerationsMediaURL(media providerMedia) (string, error) {
	value := strings.TrimSpace(firstNonEmpty(media.URL, media.DataURL))
	if isPublicMediaURL(value) || strings.HasPrefix(value, "data:") {
		return value, nil
	}
	return "", errors.New("NewAPI Video Generations 的参考素材需要公网 URL；私有素材请先保存到 OSS")
}

func normalizeNewAPIChannel2Ratio(value string, modelName string) string {
	ratio := strings.TrimSpace(value)
	if strings.Contains(ratio, "x") {
		parts := strings.SplitN(ratio, "x", 2)
		width, widthErr := strconv.Atoi(parts[0])
		height, heightErr := strconv.Atoi(parts[1])
		if widthErr == nil && heightErr == nil && width > 0 && height > 0 {
			switch {
			case width == height:
				ratio = "1:1"
			case width > height:
				ratio = "16:9"
			default:
				ratio = "9:16"
			}
		}
	}
	if modelName == "grok-video-1.5" || modelName == "grok-video-1.5-1080p" {
		if ratio != "9:16" {
			return "16:9"
		}
		return ratio
	}
	switch ratio {
	case "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3":
		return ratio
	default:
		return "16:9"
	}
}

func normalizeNewAPIChannel2Resolution(value string, modelName string) string {
	if modelName == "grok-video-1.5-1080p" {
		return "1080p"
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "480", "480p", "low":
		return "480p"
	default:
		return "720p"
	}
}

func runNewAPIChannel1VideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	var created map[string]interface{}
	if id == "" {
		body, err := newAPIChannel1VideoBody(ctx, input)
		if err != nil {
			return nil, markProviderPreparationFailure(err)
		}
		if err := postJSON(ctx, input.Config, "/videos", body, &created); err != nil {
			return nil, err
		}
		if data, ok := created["data"].(map[string]interface{}); ok {
			created = data
		}
		id = firstNonEmptyString(stringField(created, "id"), stringField(created, "task_id"))
	}
	status := strings.ToUpper(strings.TrimSpace(stringField(created, "status")))
	if strings.HasPrefix(status, "FAILED") {
		return nil, fmt.Errorf("NewAPI 媒体任务视频生成失败（任务 %s）：%s", id, strings.TrimSpace(strings.TrimPrefix(status, "FAILED:")))
	}
	if id == "" {
		return nil, errors.New("NewAPI 媒体任务没有返回任务 ID")
	}
	ctx = withProviderRequestID(ctx, id)
	var state map[string]interface{}
	if err := getJSON(ctx, input.Config, "/videos/"+id, &state); err != nil {
		return nil, err
	}
	if data, ok := state["data"].(map[string]interface{}); ok {
		state = data
	}
	status = strings.ToUpper(strings.TrimSpace(stringField(state, "status")))
	switch {
	case status == "SUCCEEDED":
		videoURL := stringField(state, "object")
		if videoURL == "" {
			return nil, fmt.Errorf("NewAPI 媒体任务 %s 已完成但没有返回视频 URL", id)
		}
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err != nil {
			return nil, fmt.Errorf("NewAPI 媒体任务视频结果下载失败（任务 %s）：%w", id, err)
		}
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
	case strings.HasPrefix(status, "FAILED"):
		message := strings.TrimSpace(strings.TrimPrefix(status, "FAILED:"))
		return nil, fmt.Errorf("NewAPI 媒体任务视频生成失败（任务 %s）：%s", id, defaultString(message, "上游返回失败"))
	}
	return nil, deferProviderPoll(ctx, status, status, newAPIChannel1VideoPollInterval)
}

func newAPIChannel1VideoBody(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceImages) > 9 || len(input.ReferenceVideos) > 3 || len(input.ReferenceAudios) > 3 {
		return nil, errors.New("NewAPI 媒体任务最多支持 9 张参考图、3 个参考视频和 3 个参考音频")
	}
	media := make([]map[string]string, 0, len(input.ReferenceImages)+len(input.ReferenceVideos)+len(input.ReferenceAudios))
	if shouldSendNewAPIVideoImages(input) {
		for _, image := range input.ReferenceImages {
			url, err := newAPIChannel1MediaURL(ctx, image)
			if err != nil {
				return nil, err
			}
			media = append(media, map[string]string{"type": seedanceImageRole(input, image), "url": url})
		}
	}
	for _, video := range input.ReferenceVideos {
		url, err := newAPIChannel1MediaURL(ctx, video)
		if err != nil {
			return nil, err
		}
		media = append(media, map[string]string{"type": "reference_video", "url": url})
	}
	for _, audio := range input.ReferenceAudios {
		url, err := newAPIChannel1MediaURL(ctx, audio)
		if err != nil {
			return nil, err
		}
		media = append(media, map[string]string{"type": "reference_voice", "url": url})
	}
	body := map[string]interface{}{
		"model": input.Config.Model,
		"input": map[string]interface{}{"prompt": strings.TrimSpace(input.Prompt)},
		"parameters": map[string]interface{}{
			"resolution":    normalizeNewAPIChannel1Resolution(input.Config.VQuality),
			"ratio":         normalizeNewAPIChannel1Ratio(input.Config.Size),
			"prompt_extend": false,
			"watermark":     parseBool(input.Config.VideoWatermark, false),
			"duration":      normalizeSeedanceVideosDuration(input.Config.VideoSeconds),
		},
	}
	if len(media) > 0 {
		body["input"].(map[string]interface{})["media"] = media
	}
	return body, nil
}

func newAPIChannel1MediaURL(ctx context.Context, media providerMedia) (string, error) {
	value := strings.TrimSpace(media.URL)
	if !isPublicMediaURL(value) {
		return "", errors.New("NewAPI 媒体任务的参考素材必须使用公网 HTTP(S) URL，请启用 OSS 或提供公网素材地址")
	}
	if _, err := ValidateOutboundURLContext(ctx, value); err != nil {
		return "", markProviderPreparationFailure(err)
	}
	return value, nil
}

func normalizeNewAPIChannel1Resolution(value string) string {
	resolution := strings.TrimSuffix(strings.TrimSpace(value), "p")
	if resolution != "480" && resolution != "720" && resolution != "1080" {
		resolution = "720"
	}
	return resolution + "P"
}

func normalizeNewAPIChannel1Ratio(value string) string {
	switch strings.TrimSpace(value) {
	case "1:1", "16:9", "9:16", "4:3", "3:4":
		return strings.TrimSpace(value)
	default:
		return "16:9"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateGenerationInterface(mode string, interfaceType string) error {
	interfaceType = strings.TrimSpace(interfaceType)
	if interfaceType == "" {
		return nil
	}
	allowed := map[string]map[string]bool{
		"text":  {"chat-completion": true, "openai-response": true, "gemini-content": true},
		"image": {"openai-image": true, "xai-image": true},
		"video": {"newapi": true, "newapi-channel-1": true, "newapi-channel-2": true, "xai-video": true, "gemini-veo": true},
		"audio": {"openai-audio": true},
	}
	if allowed[mode] != nil && !allowed[mode][interfaceType] {
		return fmt.Errorf("接口类型 %s 不支持%s生成", interfaceType, mode)
	}
	return nil
}

func runSeedanceVideosTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	var created map[string]interface{}
	if id == "" {
		body, err := seedanceVideosBody(input)
		if err != nil {
			return nil, err
		}
		if err := postJSON(ctx, input.Config, "/videos", body, &created); err != nil {
			return nil, err
		}
		if data, ok := created["data"].(map[string]interface{}); ok {
			created = data
		}
		id = firstNonEmptyString(stringField(created, "id"), stringField(created, "task_id"))
	}
	if id == "" {
		return nil, errors.New("Seedance 接口没有返回任务 ID")
	}
	ctx = withProviderRequestID(ctx, id)
	var state map[string]interface{}
	if err := getJSON(ctx, input.Config, "/videos/"+id, &state); err != nil {
		return nil, err
	}
	if data, ok := state["data"].(map[string]interface{}); ok {
		state = data
	}
	status := strings.ToLower(stringField(state, "status"))
	if status == "completed" || status == "succeeded" {
		videoURL := stringField(state, "video_url")
		if videoURL != "" {
			data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
			if err != nil {
				return nil, fmt.Errorf("视频结果下载失败：%w", err)
			}
			return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
		}
		data, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), input.Config, "/videos/"+id+"/content")
		if err != nil {
			return nil, errors.New("Seedance 任务成功但没有返回视频 URL")
		}
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
	}
	if status == "failed" || status == "cancelled" || status == "expired" {
		return nil, errors.New(defaultString(seedanceErrorMessage(state), "Seedance 视频生成失败"))
	}
	return nil, deferProviderPoll(ctx, status, status, 5*time.Second)
}

func runSeedanceAgentPlanVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	var created map[string]interface{}
	if id == "" {
		content, err := seedanceContent(input)
		if err != nil {
			return nil, err
		}
		body := map[string]interface{}{
			"model":          input.Config.Model,
			"content":        content,
			"ratio":          normalizeSeedanceRatio(input.Config.Size),
			"resolution":     normalizeSeedanceResolution(input.Config.VQuality, input.Config.Model),
			"duration":       normalizeSeedanceDuration(input.Config.VideoSeconds),
			"generate_audio": parseBool(input.Config.VideoGenerateAudio, true),
			"watermark":      parseBool(input.Config.VideoWatermark, false),
		}
		if err := postJSON(ctx, input.Config, "/contents/generations/tasks", body, &created); err != nil {
			return nil, err
		}
		if data, ok := created["data"].(map[string]interface{}); ok {
			created = data
		}
		id = stringField(created, "id")
	}
	if id == "" {
		return nil, errors.New("Seedance 接口没有返回任务 ID")
	}
	ctx = withProviderRequestID(ctx, id)
	var state map[string]interface{}
	if err := getJSON(ctx, input.Config, "/contents/generations/tasks/"+id, &state); err != nil {
		return nil, err
	}
	if data, ok := state["data"].(map[string]interface{}); ok {
		state = data
	}
	status := stringField(state, "status")
	if status == "succeeded" {
		content, _ := state["content"].(map[string]interface{})
		videoURL := stringField(content, "video_url")
		if videoURL == "" {
			return nil, errors.New("Seedance 任务成功但没有返回视频 URL")
		}
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err != nil {
			return nil, fmt.Errorf("视频结果下载失败：%w", err)
		}
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
	}
	if status == "failed" || status == "cancelled" || status == "expired" {
		return nil, errors.New("Seedance 视频生成失败")
	}
	return nil, deferProviderPoll(ctx, status, status, 5*time.Second)
}

func postJSON(ctx context.Context, config providerConfig, path string, body interface{}, target interface{}) error {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, target)
}

func postForm(ctx context.Context, config providerConfig, path string, contentType string, body io.Reader, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(config.BaseURL, path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", contentType)
	return doJSON(req, target)
}

func getJSON(ctx context.Context, config providerConfig, path string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(config.BaseURL, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	return doJSON(req, target)
}

func postBinary(ctx context.Context, config providerConfig, path string, body interface{}) ([]byte, string, error) {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return doBinary(req)
}

// 供应商请求体必须在建连前完成序列化；空正文继续下发既可能产生费用，也无法安全判断供应商是否执行。
func marshalProviderRequest(body interface{}) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, newProviderRequestNotSentError(providerRequestSerializationFailureMessage, err)
	}
	return data, nil
}

func getBinary(ctx context.Context, config providerConfig, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(config.BaseURL, path), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	return doBinary(req)
}

func getExternalBinary(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	return doBinary(req)
}

func doJSON(req *http.Request, target interface{}) error {
	data, mimeType, err := doBinary(req)
	if err != nil {
		return err
	}
	if !strings.Contains(mimeType, "json") && !json.Valid(data) {
		return fmt.Errorf("接口返回非 JSON 内容：%s", mimeType)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	if payload, ok := target.(*imageResponse); ok {
		if payload.Error != nil && payload.Error.Message != "" {
			return errors.New(payload.Error.Message)
		}
		if payload.Code != nil && *payload.Code != 0 {
			return errors.New(defaultString(payload.Msg, "请求失败"))
		}
	}
	if payload, ok := target.(*map[string]interface{}); ok {
		if code, ok := (*payload)["code"].(float64); ok && code != 0 {
			return errors.New(defaultString(stringField(*payload, "msg"), "请求失败"))
		}
		if errValue, ok := (*payload)["error"].(map[string]interface{}); ok && stringField(errValue, "message") != "" {
			return errors.New(stringField(errValue, "message"))
		}
	}
	return nil
}

func doBinary(req *http.Request) ([]byte, string, error) {
	return doBinaryWithResponseLimit(req, 0)
}

func doBinaryWithResponseLimit(req *http.Request, explicitResponseLimit int64) ([]byte, string, error) {
	startedAt := time.Now()
	requestTimeout := providerHTTPDefaultTimeout
	if deadline, ok := req.Context().Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			requestTimeout = remaining
		}
	}
	var release func()
	var coordinator *runtimeCoordinator
	var runtimeService *Service
	responseLimit := maxProviderResponseBytes
	channelID := ""
	taskID := ""
	if metadata, ok := req.Context().Value(providerAnalyticsKey{}).(providerAnalyticsContext); ok && metadata.Service != nil {
		runtimeService = metadata.Service
		coordinator = metadata.Service.coordinator
		channelID = metadata.ChannelID
		taskID = metadata.TaskID
		policy, err := metadata.Service.RuntimePolicy()
		if err != nil {
			return nil, "", fmt.Errorf("读取生成资源限制失败：%w", err)
		}
		responseLimit = megabytes(policy.Resource.GeneratedFileMB)
		// 低频测活是管理员确认渠道恢复的入口；允许它穿过已打开的熔断器，成功后会清除失败状态。
		if metadata.RequestKind != "health_check" {
			open, err := coordinator.circuitOpen(req.Context(), channelID)
			if err != nil {
				return nil, "", fmt.Errorf("读取渠道熔断状态失败：%w", err)
			}
			if open {
				return nil, "", errors.New("当前渠道连续失败，已暂时熔断，请稍后重试")
			}
		}
		slotID := channelID
		if slotID == "" {
			slotID = "custom:" + strings.ToLower(req.URL.Host)
		}
		var concurrencyLimit int
		release, concurrencyLimit, err = metadata.Service.AcquireChannelSlot(req.Context(), channelID, slotID, requestTimeout+time.Minute)
		metadata.ConcurrencyLimit = concurrencyLimit
		req = req.WithContext(context.WithValue(req.Context(), providerAnalyticsKey{}, metadata))
		if err != nil {
			logErr := recordProviderRequest(req, startedAt, 0, nil, err, false)
			return nil, "", errors.Join(err, logErr)
		}
		defer release()
	}
	if explicitResponseLimit > 0 && explicitResponseLimit < responseLimit {
		responseLimit = explicitResponseLimit
	}
	if _, err := ValidateOutboundURLContext(req.Context(), req.URL.String()); err != nil {
		preflightErr := newProviderRequestNotSentError(providerRequestPreflightFailureMessage, err)
		logErr := recordProviderRequest(req, startedAt, 0, nil, preflightErr, false)
		return nil, "", errors.Join(preflightErr, logErr)
	}
	if metadata, ok := req.Context().Value(providerAnalyticsKey{}).(providerAnalyticsContext); ok && metadata.DispatchCheckpointRequired && metadata.Service != nil {
		if err := metadata.Service.markClaimedTaskProviderCallDispatched(metadata.TaskID, metadata.LeaseOwner); err != nil {
			logErr := recordProviderRequest(req, startedAt, 0, nil, err, false)
			return nil, "", errors.Join(err, logErr)
		}
	}
	client := OutboundHTTPClient(requestTimeout)
	readObservation, _ := req.Context().Value(providerResponseReadObservationKey{}).(*providerResponseReadObservation)
	if readObservation != nil {
		readObservation.begin(time.Now())
	}
	resp, err := client.Do(req)
	if err != nil {
		if taskID != "" {
			stdlog.Printf("provider transport failed task=%s method=%s path=%s: %v", taskID, req.Method, req.URL.Path, err)
		}
		if runtimeService != nil && !errors.Is(err, context.Canceled) {
			recordProviderChannelResultBestEffort(runtimeService, channelID, taskID, true, "transport_error")
		}
		logErr := recordProviderRequest(req, startedAt, 0, nil, err, true)
		return nil, "", errors.Join(err, logErr)
	}
	defer resp.Body.Close()
	if resp.ContentLength > responseLimit {
		err = fmt.Errorf("上游响应超过 %s 限制", formatStorageLimit(responseLimit))
		logErr := recordProviderRequest(req, startedAt, resp.StatusCode, nil, err, true)
		return nil, "", errors.Join(err, logErr)
	}
	responseReader := io.Reader(resp.Body)
	if readObservation != nil {
		responseReader = &observedProviderResponseReader{source: resp.Body, observation: readObservation}
	}
	data, err := io.ReadAll(io.LimitReader(responseReader, responseLimit+1))
	if err != nil {
		if taskID != "" {
			stdlog.Printf("provider response read failed task=%s method=%s path=%s: %v", taskID, req.Method, req.URL.Path, err)
		}
		logErr := recordProviderRequest(req, startedAt, resp.StatusCode, nil, err, true)
		return nil, "", errors.Join(err, logErr)
	}
	if int64(len(data)) > responseLimit {
		err = fmt.Errorf("上游响应超过 %s 限制", formatStorageLimit(responseLimit))
		logErr := recordProviderRequest(req, startedAt, resp.StatusCode, nil, err, true)
		return nil, "", errors.Join(err, logErr)
	}
	mimeType := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if runtimeService != nil {
			recordProviderChannelResultBestEffort(runtimeService, channelID, taskID, resp.StatusCode >= 500, fmt.Sprintf("http_%d", resp.StatusCode))
		}
		httpErr := providerHTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(data)}
		logErr := recordProviderRequest(req, startedAt, resp.StatusCode, data, httpErr, ProviderHTTPStatusRequiresBillingReview(resp.StatusCode))
		return nil, "", errors.Join(httpErr, logErr)
	}
	if logErr := recordProviderRequest(req, startedAt, resp.StatusCode, data, nil, true); logErr != nil {
		return nil, "", logErr
	}
	if runtimeService != nil {
		recordProviderChannelResultBestEffort(runtimeService, channelID, taskID, false, "success")
	}
	return data, mimeType, nil
}

// 请求结束时原上下文可能已经超时；熔断统计用独立短上下文落盘，客户端主动取消则不计成功或失败。
func recordProviderChannelResultBestEffort(s *Service, channelID string, taskID string, failed bool, outcome string) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.RecordChannelResult(ctx, channelID, failed); err != nil {
		stdlog.Printf("provider channel result write failed task=%s channel=%s outcome=%s: %v", taskID, channelID, outcome, err)
	}
}

func recordProviderRequest(req *http.Request, startedAt time.Time, statusCode int, responseBody []byte, requestErr error, billingRisk bool) error {
	metadata, ok := req.Context().Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil {
		return nil
	}
	status := model.ApiCallStatusSucceeded
	errorText := ""
	if requestErr != nil || statusCode < 200 || statusCode >= 300 {
		status = model.ApiCallStatusFailed
		if requestErr != nil {
			errorText = SafeProviderLogError(requestErr)
		}
	}
	requestKind := providerRequestKind(req.Method, req.URL.Path)
	if metadata.RequestKind != "" {
		requestKind = metadata.RequestKind
	}
	apiFormat := "openai"
	if req.Header.Get("x-goog-api-key") != "" {
		apiFormat = "gemini"
	}
	apiLog := model.ApiCallLog{
		UserID: metadata.UserID, ChannelID: metadata.ChannelID, TaskID: metadata.TaskID, BillingOrderID: metadata.BillingOrderID,
		Source: "backend-task", Capability: metadata.Capability, Operation: metadata.Operation,
		RequestKind: requestKind, Billable: req.Method == http.MethodPost,
		APIFormat: apiFormat, Method: req.Method, Path: req.URL.Path, Model: metadata.Model,
		Status: status, StatusCode: statusCode, DurationMs: time.Since(startedAt).Milliseconds(),
		Error: errorText, ConcurrencyLimit: metadata.ConcurrencyLimit, UpstreamURL: req.URL.Scheme + "://" + req.URL.Host + req.URL.Path,
		CreatedAt: startedAt,
	}
	channelSlotFailure := false
	if code, message := ChannelSlotFailureDetails(requestErr); code != "" {
		channelSlotFailure = true
		apiLog.ErrorCode = code
		apiLog.Error = message
	}
	if requestKind == "create" && metadata.Capability == "video" {
		apiLog.VideoSeconds = metadata.VideoSeconds
		if apiLog.VideoSeconds <= 0 {
			if strings.Contains(strings.ToLower(metadata.Model), "seedance") || strings.Contains(req.URL.Path, "/contents/generations/tasks") {
				apiLog.VideoSeconds = 5
			} else {
				apiLog.VideoSeconds = 6
			}
		}
	}
	metadata.Service.EnrichAPICallLog(&apiLog, responseBody)
	if err := metadata.Service.LogAPICall(apiLog); err != nil {
		stdlog.Printf("provider request log write failed task=%s billing_order=%s request_kind=%s: %v", metadata.TaskID, metadata.BillingOrderID, requestKind, err)
		logErr := fmt.Errorf("上游调用日志写入失败：%w", err)
		if !billingRisk || channelSlotFailure {
			return logErr
		}
		reviewErr := providerBillingReviewError{reason: "上游请求可能已经执行，但调用日志写入失败，费用状态待核对且请勿立即重试", cause: err}
		transitionErr := metadata.Service.recordBillingTransitionFailure(
			metadata.UserID,
			metadata.TaskID,
			metadata.BillingOrderID,
			"标记调用日志缺失的费用待核对",
			metadata.Service.MarkBillingUncertain(metadata.BillingOrderID, reviewErr.reason),
		)
		return errors.Join(reviewErr, transitionErr)
	}
	return nil
}

func providerRequestKind(method string, path string) string {
	if method == http.MethodGet {
		if strings.HasSuffix(strings.TrimRight(path, "/"), "/content") || strings.Contains(path, "/download") {
			return "download"
		}
		return "poll"
	}
	if strings.Contains(path, "repair") {
		return "repair"
	}
	return "create"
}

func apiURL(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lowerBase := strings.ToLower(base)
	if strings.HasSuffix(lowerBase, "/v1") || strings.HasSuffix(lowerBase, "/v1beta") || strings.HasSuffix(lowerBase, "/api/v3") || strings.HasSuffix(lowerBase, "/api/plan/v3") {
		return base + path
	}
	return base + "/v1" + path
}

func writeField(writer *multipart.Writer, key string, value string) error {
	if err := writer.WriteField(key, value); err != nil {
		return newProviderRequestNotSentError(providerRequestSerializationFailureMessage, err)
	}
	return nil
}

func writeMediaPart(writer *multipart.Writer, field string, media providerMedia) error {
	raw, mimeType, err := mediaBytes(media)
	if err != nil {
		return err
	}
	filename := providerMediaFilename(media, mimeType)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": field, "filename": filename}))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(raw)
	return err
}

func providerMediaFilename(media providerMedia, mimeType string) string {
	base := strings.TrimSpace(media.ID)
	if base == "" {
		base = "reference"
	}
	var builder strings.Builder
	for _, char := range base {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			builder.WriteRune(char)
			if builder.Len() >= 64 {
				break
			}
		}
	}
	base = builder.String()
	if base == "" {
		base = "reference"
	}
	extensions, _ := mime.ExtensionsByType(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	extension := ".bin"
	if len(extensions) > 0 {
		extension = extensions[0]
	}
	return "reference-" + base + extension
}

func mediaBytes(media providerMedia) ([]byte, string, error) {
	value := media.DataURL
	if value == "" {
		value = media.URL
	}
	if !strings.HasPrefix(value, "data:") {
		return nil, "", errors.New("后端任务队列需要 data URL 形式的本地参考素材")
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok {
		return nil, "", errors.New("data URL 格式错误")
	}
	mimeType := strings.TrimPrefix(strings.Split(strings.TrimPrefix(header, "data:"), ";")[0], " ")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", err
	}
	return raw, normalizedMediaMimeType(defaultString(mimeType, media.Type), raw), nil
}

func imageDataURLs(payload imageResponse) ([]map[string]string, error) {
	if len(payload.Data) == 0 {
		return nil, errors.New("接口没有返回图片")
	}
	images := make([]map[string]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if b64, ok := item["b64_json"].(string); ok && b64 != "" {
			images = append(images, map[string]string{"dataUrl": "data:image/png;base64," + b64})
			continue
		}
		if url, ok := item["url"].(string); ok && url != "" {
			images = append(images, map[string]string{"dataUrl": url})
		}
	}
	if len(images) == 0 {
		return nil, errors.New("接口没有返回可用图片")
	}
	return images, nil
}

func dataURL(mimeType string, data []byte) string {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + strings.Split(mimeType, ";")[0] + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func stringField(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return value
}

func extractResponseText(payload map[string]interface{}) string {
	payload = unwrapProviderStreamPayload(payload)
	output, ok := payload["output"].([]interface{})
	if !ok {
		return ""
	}
	var chunks []string
	for _, item := range output {
		record, ok := item.(map[string]interface{})
		if !ok || record["type"] != "message" {
			continue
		}
		content, _ := record["content"].([]interface{})
		for _, part := range content {
			partRecord, ok := part.(map[string]interface{})
			if ok && stringField(partRecord, "text") != "" {
				chunks = append(chunks, stringField(partRecord, "text"))
			}
		}
	}
	return strings.Join(chunks, "")
}

func extractChatCompletionText(payload map[string]interface{}) string {
	payload = unwrapProviderStreamPayload(payload)
	choices, ok := payload["choices"].([]interface{})
	if !ok {
		return ""
	}
	var chunks []string
	for _, choice := range choices {
		record, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		if message, ok := record["message"].(map[string]interface{}); ok {
			if text := stringField(message, "content"); text != "" {
				chunks = append(chunks, text)
			}
		}
		if text := stringField(record, "text"); text != "" {
			chunks = append(chunks, text)
		}
	}
	return strings.Join(chunks, "")
}

func withSystemPrompt(config providerConfig, prompt string) string {
	systemPrompt := strings.TrimSpace(config.SystemPrompt)
	if systemPrompt == "" {
		return prompt
	}
	return systemPrompt + "\n\n" + prompt
}

func seedanceContent(input canvasGenerationInput) ([]map[string]interface{}, error) {
	content := make([]map[string]interface{}, 0, 1+len(input.ReferenceImages)+len(input.ReferenceVideos)+len(input.ReferenceAudios))
	text := seedancePromptText(input)
	if strings.TrimSpace(text) != "" {
		content = append(content, map[string]interface{}{"type": "text", "text": text})
	}
	for _, image := range input.ReferenceImages {
		url, err := mediaReferenceURL(image)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}, "role": seedanceImageRole(input, image)})
	}
	for _, video := range input.ReferenceVideos {
		url, err := mediaReferenceURL(video)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]interface{}{"type": "video_url", "video_url": map[string]interface{}{"url": url}, "role": "reference_video"})
	}
	for _, audio := range input.ReferenceAudios {
		url, err := mediaReferenceURL(audio)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]interface{}{"type": "audio_url", "audio_url": map[string]interface{}{"url": url}, "role": "reference_audio"})
	}
	if len(content) == 0 {
		return nil, errors.New("请输入视频提示词或连接参考素材")
	}
	return content, nil
}

func shouldSendNewAPIVideoImages(input canvasGenerationInput) bool {
	if input.Metadata == nil {
		return true
	}
	operation, _ := input.Metadata["videoEditOperation"].(string)
	return strings.TrimSpace(operation) != "text_to_video"
}

func newAPIVideoPromptText(input canvasGenerationInput) string {
	return strings.TrimSpace(input.Prompt)
}

func seedanceVideosBody(input canvasGenerationInput) (map[string]interface{}, error) {
	if (len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0) && len(input.ReferenceImages) == 0 {
		return nil, errors.New("Seedance 参考视频或参考音频需要同时连接至少 1 张主参考图")
	}
	body := map[string]interface{}{
		"model":          input.Config.Model,
		"prompt":         seedanceVideosPromptText(input),
		"aspect_ratio":   normalizeSeedanceVideosRatio(input.Config.Size),
		"duration":       normalizeSeedanceVideosDuration(input.Config.VideoSeconds),
		"generate_audio": parseBool(input.Config.VideoGenerateAudio, true),
	}
	imageURLs := make([]string, 0, len(input.ReferenceImages))
	for _, image := range input.ReferenceImages {
		url, err := openAIImageInputURL(image)
		if err != nil {
			return nil, err
		}
		imageURLs = append(imageURLs, url)
	}
	frameImageURLs, err := videoFrameImageURLs(input, imageURLs)
	if err != nil {
		return nil, err
	}
	if len(frameImageURLs) > 0 {
		body["image_urls"] = frameImageURLs
	} else if len(imageURLs) > 0 {
		body["image_url"] = imageURLs[0]
		if len(imageURLs) > 1 {
			body["reference_image_urls"] = imageURLs[1:]
		}
	}
	videoURLs := make([]string, 0, len(input.ReferenceVideos))
	for _, video := range input.ReferenceVideos {
		url, err := seedanceVideosMediaURL(video)
		if err != nil {
			return nil, err
		}
		videoURLs = append(videoURLs, url)
	}
	if len(videoURLs) > 0 {
		body["reference_videos"] = videoURLs
	}
	audioURLs := make([]string, 0, len(input.ReferenceAudios))
	for _, audio := range input.ReferenceAudios {
		url, err := seedanceVideosMediaURL(audio)
		if err != nil {
			return nil, err
		}
		audioURLs = append(audioURLs, url)
	}
	if len(audioURLs) > 0 {
		body["reference_audios"] = audioURLs
	}
	return body, nil
}

func seedancePromptText(input canvasGenerationInput) string {
	return strings.TrimSpace(input.Prompt)
}

func seedanceVideosPromptText(input canvasGenerationInput) string {
	return strings.TrimSpace(input.Prompt)
}

func seedanceImageRole(input canvasGenerationInput, image providerMedia) string {
	if id := metadataString(input.Metadata, "videoStartFrameNodeId"); id != "" && image.ID == id {
		return "first_frame"
	}
	if id := metadataString(input.Metadata, "videoEndFrameNodeId"); id != "" && image.ID == id {
		return "last_frame"
	}
	return "reference_image"
}

func videoFrameImageURLs(input canvasGenerationInput, imageURLs []string) ([]string, error) {
	startFrameID := metadataString(input.Metadata, "videoStartFrameNodeId")
	endFrameID := metadataString(input.Metadata, "videoEndFrameNodeId")
	if startFrameID == "" && endFrameID == "" {
		return nil, nil
	}
	// image_urls 按首帧、尾帧、普通参考图排序，保持 JSON 视频协议的结构化帧语义。
	ordered := make([]string, 0, len(imageURLs))
	used := make([]bool, len(imageURLs))
	appendFrame := func(frameID string, label string) error {
		if frameID == "" {
			return nil
		}
		for index, image := range input.ReferenceImages {
			if index >= len(imageURLs) || image.ID != frameID {
				continue
			}
			ordered = append(ordered, imageURLs[index])
			used[index] = true
			return nil
		}
		return fmt.Errorf("已配置的%s参考图未包含在视频请求中", label)
	}
	if err := appendFrame(startFrameID, "首帧"); err != nil {
		return nil, err
	}
	if err := appendFrame(endFrameID, "尾帧"); err != nil {
		return nil, err
	}
	for index, imageURL := range imageURLs {
		if !used[index] {
			ordered = append(ordered, imageURL)
		}
	}
	return ordered, nil
}

func metadataString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func mediaReferenceURL(media providerMedia) (string, error) {
	value := strings.TrimSpace(media.URL)
	if isPublicMediaURL(value) || strings.HasPrefix(value, "asset://") || strings.HasPrefix(value, "data:") {
		return value, nil
	}
	value = strings.TrimSpace(media.DataURL)
	if value != "" {
		return value, nil
	}
	return "", errors.New("参考素材需要公网 URL、asset:// 素材 ID 或 data URL")
}

func seedanceVideosMediaURL(media providerMedia) (string, error) {
	value := strings.TrimSpace(media.DataURL)
	if strings.HasPrefix(value, "data:") {
		return value, nil
	}
	value = strings.TrimSpace(media.URL)
	if strings.HasPrefix(value, "data:") || isPublicMediaURL(value) {
		return value, nil
	}
	return "", errors.New("Seedance /videos 参考素材需要公网 URL 或 data URL")
}

func seedanceErrorMessage(state map[string]interface{}) string {
	if errorValue, ok := state["error"].(map[string]interface{}); ok {
		message := stringField(errorValue, "message")
		code := stringField(errorValue, "code")
		if message != "" && code != "" {
			return code + "：" + message
		}
		if message != "" {
			return message
		}
	}
	code := stringField(state, "error_code")
	if code != "" {
		return code
	}
	return ""
}

func isPublicMediaURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func isSeedanceVideoConfig(config providerConfig) bool {
	model := strings.ToLower(config.Model)
	return strings.Contains(model, "seedance") || strings.Contains(model, "doubao-seedance") || isArkPlanVideoConfig(config)
}

func isArkPlanVideoConfig(config providerConfig) bool {
	return strings.Contains(strings.ToLower(config.BaseURL), "/api/plan/v3")
}

func normalizeImageQuality(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1k":
		return "low"
	case "2k":
		return "medium"
	case "4k":
		return "high"
	default:
		return value
	}
}

func normalizePixelSize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" || strings.Contains(value, ":") {
		return ""
	}
	if strings.Contains(value, "x") {
		return value
	}
	return ""
}

func normalizeVideoSize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" {
		return ""
	}
	if strings.Contains(value, "x") {
		return value
	}
	if value == "9:16" || value == "2:3" || value == "3:4" {
		return "720x1280"
	}
	return "1280x720"
}

func normalizeVideoResolution(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" || value == "medium" || value == "high" {
		return "720p"
	}
	if value == "low" {
		return "480p"
	}
	if strings.HasSuffix(value, "p") {
		return value
	}
	return value + "p"
}

func normalizeSeedanceDuration(value string) int {
	if strings.TrimSpace(value) == "-1" {
		return -1
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds == 0 {
		seconds = 5
	}
	if seconds < 4 {
		return 4
	}
	if seconds > 15 {
		return 15
	}
	return seconds
}

func normalizeSeedanceVideosDuration(value string) int {
	seconds := normalizeSeedanceDuration(value)
	if seconds < 4 {
		return 5
	}
	return seconds
}

func normalizeSeedanceRatio(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" || value == "adaptive" {
		return "adaptive"
	}
	switch value {
	case "16:9", "9:16", "1:1", "4:3", "3:4", "21:9":
		return value
	default:
		return "adaptive"
	}
}

func normalizeSeedanceVideosRatio(value string) string {
	ratio := normalizeSeedanceRatio(value)
	if ratio == "adaptive" {
		return "16:9"
	}
	return ratio
}

func normalizeSeedanceResolution(value string, model string) string {
	resolution := strings.TrimSuffix(strings.TrimSpace(value), "p")
	switch resolution {
	case "480", "720", "1080":
	default:
		if value == "low" {
			resolution = "480"
		} else {
			resolution = "720"
		}
	}
	if strings.Contains(strings.ToLower(model), "fast") && resolution == "1080" {
		resolution = "720"
	}
	return resolution + "p"
}

func parseBool(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true
	case "false":
		return false
	default:
		return fallback
	}
}

func parseFloat(value string, fallback float64) float64 {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || number == 0 {
		return fallback
	}
	return number
}
