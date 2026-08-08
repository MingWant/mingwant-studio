package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

var (
	runtimeService        *service.Service
	geminiGeneratePath    = regexp.MustCompile(`^/models/([^/:]+):(generateContent|streamGenerateContent)$`)
	customGeminiRelayPath = regexp.MustCompile(`(?:^|/)models/[^/:]+:(generateContent|streamGenerateContent)$`)
	openAIPostEndpoints   = map[string]bool{
		"/responses": true, "/chat/completions": true,
	}
)

func ConfigureRuntime(svc *service.Service) {
	runtimeService = svc
}

func authorizeCustomRelay(method string, target *url.URL, apiFormat string, contentType string) error {
	requestPath, err := normalizedCustomRelayPath(target.EscapedPath())
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return errors.New("自定义渠道查询参数无效")
	}
	for key := range query {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "key", "api_key", "access_token", "token":
			return errors.New("自定义渠道地址不允许在查询参数中携带密钥")
		}
	}

	apiFormat = strings.ToLower(strings.TrimSpace(apiFormat))
	if apiFormat != "openai" && apiFormat != "gemini" {
		return errors.New("自定义渠道调用格式无效")
	}
	if method == http.MethodGet {
		// 媒体轮询由持久任务负责；同步中继的 GET 只开放模型目录。
		allowed := requestPath == "/models" || strings.HasSuffix(requestPath, "/models")
		if len(query) != 0 || !allowed {
			return errors.New("自定义渠道不允许访问该上游接口")
		}
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return errors.New("自定义渠道生成请求必须使用 application/json")
	}
	if method != http.MethodPost {
		return errors.New("自定义渠道不允许使用该请求方法")
	}
	if apiFormat == "openai" {
		if len(query) != 0 || (!strings.HasSuffix(requestPath, "/responses") && !strings.HasSuffix(requestPath, "/chat/completions")) {
			return errors.New("自定义渠道不允许访问该上游接口")
		}
		return nil
	}
	if !customGeminiRelayPath.MatchString(requestPath) {
		return errors.New("自定义渠道不允许访问该上游接口")
	}
	if len(query) == 0 {
		return nil
	}
	if len(query) == 1 && len(query["alt"]) == 1 && query.Get("alt") == "sse" && strings.HasSuffix(requestPath, ":streamGenerateContent") {
		return nil
	}
	return errors.New("自定义渠道不允许使用该查询参数")
}

func normalizedCustomRelayPath(value string) (string, error) {
	if len(value) > 2048 {
		return "", errors.New("自定义渠道请求路径过长")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil || strings.Contains(decoded, "\\") || strings.Contains(decoded, "\x00") {
		return "", errors.New("自定义渠道请求路径无效")
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(decoded, "/"))
	if cleaned != decoded && cleaned != "/"+strings.TrimPrefix(decoded, "/") {
		return "", errors.New("自定义渠道请求路径无效")
	}
	return cleaned, nil
}

func enforceRateLimit(c *gin.Context, key string, limit int, window time.Duration) bool {
	if runtimeService == nil {
		failInternal(c, http.StatusServiceUnavailable, "请求协调服务暂时不可用", errors.New("请求协调器尚未初始化"))
		return false
	}
	allowed, err := runtimeService.AllowRequest(c.Request.Context(), key, limit, window)
	if err != nil {
		failInternal(c, http.StatusServiceUnavailable, "请求协调服务暂时不可用", err)
		return false
	}
	if allowed {
		return true
	}
	retryAfterSeconds := int64((window + time.Second - 1) / time.Second)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
	fail(c, http.StatusTooManyRequests, errors.New("请求过于频繁，请稍后再试"))
	return false
}

// 账号级限流必须跨 IP 生效，但 Redis 键和本机诊断中不应出现邮箱或用户名原文。
func rateLimitSubjectHash(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
	return fmt.Sprintf("%x", sum)
}

func loadRuntimePolicy(c *gin.Context, svc *service.Service) (service.RuntimePolicySetting, bool) {
	policy, err := svc.RuntimePolicy()
	if err != nil {
		failInternal(c, http.StatusServiceUnavailable, "运行时策略暂时不可用，请稍后再试", err)
		return service.RuntimePolicySetting{}, false
	}
	return policy, true
}

func authorizeSystemProxy(_ *model.ModelChannel, method string, requestPath string, contentType string, body []byte) error {
	requestPath, err := normalizedProxyPath(requestPath)
	if err != nil {
		return err
	}
	if method == http.MethodGet && requestPath == "/models" {
		return nil
	}
	matches := geminiGeneratePath.FindStringSubmatch(requestPath)
	if len(matches) == 3 {
		if method != http.MethodPost {
			return errors.New("系统渠道不允许使用该请求方法")
		}
		modelName, err := url.PathUnescape(matches[1])
		if err != nil || strings.TrimSpace(modelName) == "" {
			return errors.New("系统渠道请求缺少模型标识")
		}
		return authorizeInteractiveModelBody(body)
	}
	if method != http.MethodPost || !openAIPostEndpoints[requestPath] {
		return errors.New("系统渠道不允许访问该上游接口")
	}
	modelName := proxyRequestModel(contentType, body)
	if modelName == "" {
		return errors.New("系统渠道请求缺少模型标识")
	}
	return authorizeInteractiveModelBody(body)
}

func authorizeInteractiveModelBody(body []byte) error {
	var payload map[string]any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil || payload == nil {
		return errors.New("同步模型请求体必须是 JSON 对象")
	}
	if _, exists := payload["audio"]; exists {
		return errors.New("音频输出必须通过后端持久任务创建")
	}
	for _, key := range []string{"modalities", "response_modalities"} {
		if value, exists := payload[key]; exists && !textOnlyModalities(value) {
			return errors.New("媒体输出必须通过后端持久任务创建")
		}
	}
	for _, key := range []string{"generationConfig", "generation_config"} {
		config, _ := payload[key].(map[string]any)
		if config == nil {
			continue
		}
		for _, modalitiesKey := range []string{"responseModalities", "response_modalities"} {
			if value, exists := config[modalitiesKey]; exists && !textOnlyModalities(value) {
				return errors.New("Gemini 媒体输出必须通过后端持久任务创建")
			}
		}
	}
	tools, _ := payload["tools"].([]any)
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		toolType, _ := tool["type"].(string)
		switch strings.ToLower(strings.TrimSpace(toolType)) {
		case "image_generation", "audio_generation", "video_generation":
			return errors.New("供应商原生媒体工具必须通过后端持久任务调用")
		}
	}
	return nil
}

func textOnlyModalities(value any) bool {
	modalities, ok := value.([]any)
	if !ok || len(modalities) == 0 {
		return false
	}
	for _, modality := range modalities {
		name, ok := modality.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "text") {
			return false
		}
	}
	return true
}

func normalizedProxyPath(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil || strings.Contains(decoded, "\\") || strings.Contains(decoded, "\x00") {
		return "", errors.New("系统渠道请求路径无效")
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(decoded, "/"))
	if cleaned != decoded && cleaned != "/"+strings.TrimPrefix(decoded, "/") {
		return "", errors.New("系统渠道请求路径无效")
	}
	return cleaned, nil
}

func proxyRequestModel(contentType string, body []byte) string {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				return ""
			}
			if part.FormName() == "model" {
				value, _ := io.ReadAll(io.LimitReader(part, 1024))
				return strings.TrimSpace(string(value))
			}
			_ = part.Close()
		}
	}
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	modelName, _ := payload["model"].(string)
	return strings.TrimSpace(modelName)
}

func proxyRequestModelFromPath(requestPath string) string {
	matches := geminiGeneratePath.FindStringSubmatch(requestPath)
	if len(matches) != 3 {
		return ""
	}
	modelName, err := url.PathUnescape(matches[1])
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
}

func proxyTextProtocol(requestPath string) model.ChannelInterfaceType {
	if len(geminiGeneratePath.FindStringSubmatch(requestPath)) == 3 {
		return model.ChannelInterfaceGeminiContent
	}
	switch requestPath {
	case "/responses":
		return model.ChannelInterfaceOpenAIResponse
	case "/chat/completions":
		return model.ChannelInterfaceChatCompletion
	default:
		return ""
	}
}

func proxyAPIFormat(requestPath string, fallback string) string {
	if protocol := proxyTextProtocol(requestPath); protocol != "" {
		if protocol == model.ChannelInterfaceGeminiContent {
			return "gemini"
		}
		return "openai"
	}
	if strings.EqualFold(strings.TrimSpace(fallback), "gemini") {
		return "gemini"
	}
	return "openai"
}
