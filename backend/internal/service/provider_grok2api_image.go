package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"infinite-canvas/backend/internal/model"
)

func isGrok2APIImageConfig(config providerConfig) bool {
	return strings.TrimSpace(config.InterfaceType) == string(model.ChannelInterfaceGrok2APIImage)
}

func runGrok2APIImageTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if resumedProviderRequestID(ctx) != "" {
		return nil, providerBillingReviewError{
			reason: "grok2api 图片任务已有更早的供应商调用，但该兼容层没有可安全恢复的图片任务 ID；本次未重新生图，请先核对原费用",
			cause:  errors.New("grok2api image request cannot be replayed"),
		}
	}
	path, body, err := grok2APIImageRequest(input)
	if err != nil {
		return nil, err
	}
	var payload imageResponse
	if err := postJSON(ctx, input.Config, path, body, &payload); err != nil {
		return nil, grok2APIImageCreateError(err)
	}
	images, err := grok2APIImageDataURLs(ctx, input.Config, payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

// grok2api 图片兼容层与 xAI 都使用 JSON，但对象字段和恢复能力不同：编辑图片
// 只发送 url，不发送 xAI 专用 type，也不能携带 storage_options 或 Files 恢复配置。
func grok2APIImageRequest(input canvasGenerationInput) (string, map[string]interface{}, error) {
	if input.Mask != nil {
		return "", nil, errors.New("grok2api 图片协议暂不支持蒙版编辑，本次未调用供应商")
	}
	if input.Config.TransparentBackground == "true" {
		return "", nil, errors.New("grok2api 图片协议暂不支持透明背景参数，本次未调用供应商")
	}
	if len(input.ReferenceImages) > 8 {
		return "", nil, fmt.Errorf("grok2api 图片编辑最多支持 8 张参考图，当前连接了 %d 张，本次未调用供应商", len(input.ReferenceImages))
	}
	body := map[string]interface{}{
		"model":           input.Config.Model,
		"prompt":          withSystemPrompt(input.Config, input.Prompt),
		"response_format": "b64_json",
	}
	aspectRatio := normalizeXAIImageAspectRatio(input.Config.Size)
	if len(input.ReferenceImages) == 0 {
		body["n"] = 1
		body["aspect_ratio"] = aspectRatio
		body["resolution"] = normalizeXAIImageResolution(input.Config.Size, input.Config.Quality)
		return "/images/generations", body, nil
	}

	images := make([]map[string]interface{}, 0, len(input.ReferenceImages))
	for _, image := range input.ReferenceImages {
		imageURL, err := openAIImageInputURL(image)
		if err != nil {
			return "", nil, fmt.Errorf("grok2api 参考图片无效：%w", err)
		}
		images = append(images, map[string]interface{}{"url": imageURL})
	}
	if len(images) == 1 {
		body["image"] = images[0]
	} else {
		body["images"] = images
	}
	body["resolution"] = normalizeXAIImageResolution(input.Config.Size, input.Config.Quality)
	if len(images) > 1 && aspectRatio != "auto" {
		body["aspect_ratio"] = aspectRatio
	}
	return "/images/edits", body, nil
}

func grok2APIImageDataURLs(ctx context.Context, config providerConfig, payload imageResponse) ([]map[string]string, error) {
	if len(payload.Data) == 0 {
		return nil, grok2APIImageResultError(errors.New("response contains no images"))
	}
	images := make([]map[string]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if encoded := strings.TrimSpace(stringField(item, "b64_json")); encoded != "" {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, grok2APIImageResultError(err)
			}
			mimeType := normalizedMediaMimeType(defaultString(stringField(item, "mime_type"), "image/png"), raw)
			if !strings.HasPrefix(mimeType, "image/") {
				return nil, grok2APIImageResultError(errors.New("base64 result is not an image"))
			}
			images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
			continue
		}
		rawURL := strings.TrimSpace(stringField(item, "url"))
		if rawURL == "" {
			return nil, grok2APIImageResultError(errors.New("image result has no b64_json or url"))
		}
		raw, mimeType, err := downloadGrok2APIImageURL(withProviderRequestKind(ctx, "download"), config, rawURL)
		if err != nil {
			return nil, grok2APIImageResultError(err)
		}
		mimeType = normalizedMediaMimeType(mimeType, raw)
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, grok2APIImageResultError(errors.New("image URL did not return an image"))
		}
		images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
	}
	return images, nil
}

func downloadGrok2APIImageURL(ctx context.Context, config providerConfig, rawURL string) ([]byte, string, error) {
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		raw, mimeType, err := mediaBytes(providerMedia{DataURL: rawURL})
		return raw, mimeType, err
	}
	resultURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	baseURL, baseErr := url.Parse(strings.TrimSpace(config.BaseURL))
	sameProvider := baseErr == nil && resultURL.IsAbs() && strings.EqualFold(resultURL.Host, baseURL.Host)
	if !resultURL.IsAbs() || sameProvider {
		path := resultURL.EscapedPath()
		if path == "" {
			return nil, "", errors.New("grok2api image URL has no path")
		}
		basePath := ""
		if sameProvider {
			basePath = strings.TrimRight(baseURL.EscapedPath(), "/")
		}
		if basePath != "" && strings.HasPrefix(strings.ToLower(path), strings.ToLower(basePath)+"/") {
			path = path[len(basePath):]
		} else if strings.HasPrefix(strings.ToLower(path), "/v1/") {
			path = path[len("/v1"):]
		}
		if resultURL.RawQuery != "" {
			path += "?" + resultURL.RawQuery
		}
		return getBinary(ctx, config, path)
	}
	return getExternalBinary(ctx, rawURL)
}

func grok2APIImageCreateError(err error) error {
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		return err
	}
	return publicTaskError{
		message: "当前 Base URL 没有开放 grok2api 图片 JSON 路由；若外层是 New API 中转，需要中转方透传 /v1/images/generations 与 /v1/images/edits。本次请求已被明确拒绝，系统不会自动换协议重发",
		cause:   err,
	}
}

func grok2APIImageResultError(cause error) error {
	return publicTaskError{
		message: "grok2api 已返回图片结果，但本系统读取媒体失败；费用可能已经产生，请勿立即重试",
		cause:   cause,
	}
}
