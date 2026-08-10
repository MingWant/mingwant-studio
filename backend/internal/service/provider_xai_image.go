package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// xAI Imagine 的编辑接口只接受 JSON；即使历史配置误选了 OpenAI Images，
// 也要根据官方 Grok 图片模型名切换报文，不能把 multipart 请求发给上游后再重试。
func isXAIImageConfig(config providerConfig) bool {
	interfaceType := strings.TrimSpace(config.InterfaceType)
	if interfaceType == string(model.ChannelInterfaceXAIImage) {
		return true
	}
	if interfaceType != "" && interfaceType != string(model.ChannelInterfaceOpenAIImage) {
		return false
	}
	return isXAIImageModelName(config.Model)
}

func isXAIImageModelName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.LastIndex(value, "::"); index >= 0 {
		value = value[index+2:]
	}
	value = strings.TrimPrefix(value, "models/")
	return strings.HasPrefix(value, "grok-imagine-image")
}

func runXAIImageTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	path, body, err := xaiImageRequest(input)
	if err != nil {
		return nil, err
	}
	var payload imageResponse
	if err := postJSON(ctx, input.Config, path, body, &payload); err != nil {
		return nil, err
	}
	images, err := xaiImageDataURLs(ctx, payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

func xaiImageRequest(input canvasGenerationInput) (string, map[string]interface{}, error) {
	if input.Mask != nil {
		return "", nil, errors.New("xAI Imagine 图片协议暂不支持蒙版编辑，本次未调用供应商")
	}
	if input.Config.TransparentBackground == "true" {
		return "", nil, errors.New("xAI Imagine 图片协议暂不支持透明背景参数，本次未调用供应商")
	}
	if len(input.ReferenceImages) > 3 {
		return "", nil, fmt.Errorf("xAI Imagine 图片编辑最多支持 3 张参考图，当前连接了 %d 张，本次未调用供应商", len(input.ReferenceImages))
	}

	// 编辑接口默认给短期 imgen.x.ai URL；部署网络无法直连 CDN 时，上游已计费却无法落盘。
	// 生成与编辑都强制内联结果，URL 仅保留为兼容上游忽略该字段时的兜底。
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
		url, err := openAIImageInputURL(image)
		if err != nil {
			return "", nil, fmt.Errorf("xAI Imagine 参考图片无效：%w", err)
		}
		images = append(images, map[string]interface{}{"type": "image_url", "url": url})
	}
	if len(images) == 1 {
		body["image"] = images[0]
	} else {
		body["images"] = images
	}
	// auto 编辑保持首张输入图的比例；显式比例才覆盖 xAI 的默认行为。
	if aspectRatio != "auto" {
		body["aspect_ratio"] = aspectRatio
	}
	return "/images/edits", body, nil
}

// xAI 默认返回临时 URL。任务必须立刻把结果物化为内联媒体，后续资源落盘才不会
// 依赖会过期的供应商地址；这个下载不会再次触发图片生成或编辑计费。
func xaiImageDataURLs(ctx context.Context, payload imageResponse) ([]map[string]string, error) {
	if len(payload.Data) == 0 {
		return nil, errors.New("xAI Imagine 接口没有返回图片")
	}
	images := make([]map[string]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if encoded, ok := item["b64_json"].(string); ok && strings.TrimSpace(encoded) != "" {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, xaiImageResultError(err)
			}
			mimeType := normalizedMediaMimeType(defaultString(stringField(item, "mime_type"), "image/jpeg"), raw)
			if !strings.HasPrefix(mimeType, "image/") {
				return nil, xaiImageResultError(errors.New("上游返回的 base64 不是图片"))
			}
			images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
			continue
		}
		url := strings.TrimSpace(stringField(item, "url"))
		if url == "" {
			continue
		}
		raw, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), url)
		if err != nil {
			return nil, xaiImageResultError(err)
		}
		mimeType = normalizedMediaMimeType(mimeType, raw)
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, xaiImageResultError(errors.New("上游临时地址没有返回图片"))
		}
		images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
	}
	if len(images) == 0 {
		return nil, xaiImageResultError(errors.New("上游响应没有可用图片"))
	}
	return images, nil
}

func xaiImageResultError(cause error) error {
	return publicTaskError{
		message: "xAI Imagine 已返回结果，但临时图片读取失败；费用可能已经产生，请勿立即重试",
		cause:   cause,
	}
}

func normalizeXAIImageAspectRatio(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{"-2k", "-4k"} {
		value = strings.TrimSuffix(value, suffix)
	}
	if value == "" || value == "auto" {
		return "auto"
	}
	candidates := []struct {
		name  string
		ratio float64
	}{
		{name: "1:1", ratio: 1},
		{name: "16:9", ratio: 16.0 / 9},
		{name: "9:16", ratio: 9.0 / 16},
		{name: "4:3", ratio: 4.0 / 3},
		{name: "3:4", ratio: 3.0 / 4},
		{name: "3:2", ratio: 3.0 / 2},
		{name: "2:3", ratio: 2.0 / 3},
		{name: "2:1", ratio: 2},
		{name: "1:2", ratio: 0.5},
		{name: "19.5:9", ratio: 19.5 / 9},
		{name: "9:19.5", ratio: 9.0 / 19.5},
		{name: "20:9", ratio: 20.0 / 9},
		{name: "9:20", ratio: 9.0 / 20},
	}
	for _, candidate := range candidates {
		if value == candidate.name {
			return candidate.name
		}
	}
	ratio, ok := parseXAIImageRatio(value)
	if !ok {
		return "auto"
	}
	best := candidates[0]
	bestDistance := math.Abs(ratio - best.ratio)
	for _, candidate := range candidates[1:] {
		distance := math.Abs(ratio - candidate.ratio)
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best.name
}

func parseXAIImageRatio(value string) (float64, bool) {
	separator := ":"
	if strings.Contains(value, "x") {
		separator = "x"
	}
	parts := strings.Split(value, separator)
	if len(parts) != 2 {
		return 0, false
	}
	width, widthErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	height, heightErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, false
	}
	return width / height, true
}

func normalizeXAIImageResolution(size string, quality string) string {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "high", "2k", "4k":
		return "2k"
	}
	value := strings.ToLower(strings.TrimSpace(size))
	if strings.HasSuffix(value, "-2k") || strings.HasSuffix(value, "-4k") {
		return "2k"
	}
	parts := strings.Split(value, "x")
	if len(parts) == 2 {
		width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if widthErr == nil && heightErr == nil && width > 0 && height > 0 && minInt(width, height) > 1024 {
			return "2k"
		}
	}
	return "1k"
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
