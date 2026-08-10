package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// 官方 Grok Imagine 视频必须走 /videos/generations JSON。历史配置只有在误选
// 通用 OpenAI Compatible Videos 时才按模型名纠正，其他显式视频协议保持原契约。
func isXAIVideoConfig(config providerConfig) bool {
	interfaceType := strings.TrimSpace(config.InterfaceType)
	if interfaceType == string(model.ChannelInterfaceXAIVideo) {
		return true
	}
	if interfaceType != "" && interfaceType != string(model.ChannelInterfaceNewAPIVideo) {
		return false
	}
	return isXAIVideoModelName(config.Model)
}

func isXAIVideoModelName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.LastIndex(value, "::"); index >= 0 {
		value = value[index+2:]
	}
	value = strings.TrimPrefix(value, "models/")
	return strings.HasPrefix(value, "grok-imagine-video")
}

// xAI 生成接口与 legacy /videos 使用不同字段，保持独立可避免兼容字段触发上游 400/422。
func xaiVideoBody(input canvasGenerationInput) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"model":      input.Config.Model,
		"prompt":     strings.TrimSpace(input.Prompt),
		"duration":   normalizeXAIVideoDuration(input.Config.VideoSeconds),
		"resolution": normalizeXAIVideoResolution(input.Config.VQuality),
	}
	operation := metadataString(input.Metadata, "videoEditOperation")
	if operation == "image_to_video" && len(input.ReferenceImages) == 0 {
		return nil, errors.New("xAI 图生视频必须提供 1 张起始图，本次未调用供应商")
	}
	useStartImage := shouldSendNewAPIVideoImages(input) && len(input.ReferenceImages) > 0
	if !useStartImage {
		body["aspect_ratio"] = normalizeXAIVideoAspectRatio(input.Config.Size)
		return body, nil
	}
	if len(input.ReferenceImages) > 1 {
		return nil, fmt.Errorf("xAI 图生视频只支持 1 张起始图，当前连接了 %d 张，本次未调用供应商", len(input.ReferenceImages))
	}
	imageURL, err := openAIImageInputURL(input.ReferenceImages[0])
	if err != nil {
		return nil, fmt.Errorf("xAI 起始图无效，本次未调用供应商：%w", err)
	}
	body["image"] = map[string]interface{}{"url": imageURL}
	// 官方协议在未指定画幅时会沿用起始图比例，不能把 auto 强制改成 16:9。
	if !isXAIVideoAutomaticAspectRatio(input.Config.Size) {
		body["aspect_ratio"] = normalizeXAIVideoAspectRatio(input.Config.Size)
	}
	return body, nil
}

func isXAIVideoAutomaticAspectRatio(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "adaptive":
		return true
	default:
		return false
	}
}

func normalizeXAIVideoDuration(value string) int {
	duration, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 6
	}
	if duration > 15 {
		return 15
	}
	return duration
}

func normalizeXAIVideoResolution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "480", "480p", "low":
		return "480p"
	case "1080", "1080p", "high":
		return "1080p"
	default:
		return "720p"
	}
}

func normalizeXAIVideoAspectRatio(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	allowed := map[string]bool{
		"1:1": true, "16:9": true, "9:16": true, "4:3": true,
		"3:4": true, "3:2": true, "2:3": true,
	}
	if allowed[value] {
		return value
	}
	parts := strings.Split(value, "x")
	if len(parts) != 2 {
		return "16:9"
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "16:9"
	}
	ratio := float64(width) / float64(height)
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
	}
	bestName := "16:9"
	bestDifference := 2.0
	for _, candidate := range candidates {
		difference := ratio - candidate.ratio
		if difference < 0 {
			difference = -difference
		}
		if difference < bestDifference {
			bestName = candidate.name
			bestDifference = difference
		}
	}
	return bestName
}
