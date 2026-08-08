package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Gemini Veo 使用原生鉴权和长任务协议，不能复用 OpenAI 视频任务的创建与轮询路径。
func runGeminiVeoVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceImages) > 1 || len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return nil, errors.New("Gemini Veo 当前只支持 1 张起始图，不支持参考视频或音频")
	}
	id := resumedProviderRequestID(ctx)
	if id == "" {
		instance := map[string]interface{}{"prompt": strings.TrimSpace(input.Prompt)}
		if len(input.ReferenceImages) == 1 {
			raw, mimeType, err := mediaBytes(input.ReferenceImages[0])
			if err != nil {
				return nil, fmt.Errorf("读取 Gemini Veo 起始图失败：%w", err)
			}
			instance["image"] = map[string]interface{}{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(raw), "mimeType": mimeType}
		}
		body := map[string]interface{}{
			"instances": []interface{}{instance},
			"parameters": map[string]interface{}{
				"aspectRatio":     normalizeNewAPIChannel2Ratio(input.Config.Size, strings.ToLower(input.Config.Model)),
				"durationSeconds": normalizeSeedanceVideosDuration(input.Config.VideoSeconds),
				"resolution":      normalizeNewAPIChannel2Resolution(input.Config.VQuality, strings.ToLower(input.Config.Model)),
				"sampleCount":     1,
			},
		}
		var created map[string]interface{}
		if err := postGeminiJSON(ctx, input.Config, "/models/"+url.PathEscape(input.Config.Model)+":predictLongRunning", body, &created); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(stringField(created, "name"))
	}
	if id == "" {
		return nil, errors.New("Gemini Veo 没有返回 operation name")
	}
	ctx = withProviderRequestID(ctx, id)
	var operation map[string]interface{}
	if err := getGeminiJSON(ctx, input.Config, "/"+strings.TrimLeft(id, "/"), &operation); err != nil {
		return nil, err
	}
	if errorValue, ok := operation["error"].(map[string]interface{}); ok && stringField(errorValue, "message") != "" {
		return nil, fmt.Errorf("Gemini Veo 视频生成失败（任务 %s）：%s", id, stringField(errorValue, "message"))
	}
	done, _ := operation["done"].(bool)
	if !done {
		return nil, deferProviderPoll(ctx, "processing", "processing", 5*time.Second)
	}
	videoURL := findProviderMediaURL(operation["response"])
	if videoURL == "" {
		return nil, fmt.Errorf("Gemini Veo 任务 %s 已完成但没有返回视频地址", id)
	}
	data, mimeType, err := getGeminiBinary(withProviderRequestKind(ctx, "download"), input.Config, videoURL)
	if err != nil {
		return nil, fmt.Errorf("Gemini Veo 视频下载失败（任务 %s）：%w", id, err)
	}
	mimeType = normalizedMediaMimeType(mimeType, data)
	return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
}

func postGeminiJSON(ctx context.Context, config providerConfig, path string, body interface{}, target interface{}) error {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiAPIURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, target)
}

func getGeminiJSON(ctx context.Context, config providerConfig, path string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiAPIURL(config.BaseURL, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", config.APIKey)
	return doJSON(req, target)
}

func getGeminiBinary(ctx context.Context, config providerConfig, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-goog-api-key", config.APIKey)
	return doBinary(req)
}

func geminiAPIURL(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lowerBase := strings.ToLower(base)
	if !strings.HasSuffix(lowerBase, "/v1") && !strings.HasSuffix(lowerBase, "/v1beta") {
		base += "/v1beta"
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

// Veo 不同版本的结果嵌套层级不同，只递归接受明确的公网媒体地址。
func findProviderMediaURL(value interface{}) string {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, key := range []string{"uri", "url", "videoUri", "video_url"} {
			if candidate := strings.TrimSpace(stringField(typed, key)); isPublicMediaURL(candidate) {
				return candidate
			}
		}
		for _, child := range typed {
			if candidate := findProviderMediaURL(child); candidate != "" {
				return candidate
			}
		}
	case []interface{}:
		for _, child := range typed {
			if candidate := findProviderMediaURL(child); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}
