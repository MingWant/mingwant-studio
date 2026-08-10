package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

const xaiVideoStoredResultExpiresAfterSeconds = 7 * 24 * 60 * 60

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
		// 视频没有 base64 返回模式。保存一份短期私有 Files 结果，避免部署网络必须直连 vidgen.x.ai。
		"storage_options": map[string]interface{}{
			"filename":      "mingwant-video.mp4",
			"expires_after": xaiVideoStoredResultExpiresAfterSeconds,
		},
	}
	operation := metadataString(input.Metadata, "videoEditOperation")
	if operation == "reference_to_video" {
		return xaiReferenceVideoBody(body, input)
	}
	if metadataString(input.Metadata, "videoEndFrameNodeId") != "" {
		return nil, errors.New("xAI 图生视频不支持指定尾帧，本次未调用供应商")
	}
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

func xaiReferenceVideoBody(body map[string]interface{}, input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceImages) < 1 || len(input.ReferenceImages) > 7 {
		return nil, fmt.Errorf("xAI 多参考图实验模式必须提供 1-7 张参考图，当前连接了 %d 张，本次未调用供应商", len(input.ReferenceImages))
	}
	if normalizeXAIVideoResolution(input.Config.VQuality) == "1080p" {
		return nil, errors.New("xAI 多参考图实验模式最高支持 720P，本次未调用供应商")
	}
	references := make([]map[string]interface{}, 0, len(input.ReferenceImages))
	for index, image := range input.ReferenceImages {
		imageURL, err := openAIImageInputURL(image)
		if err != nil {
			return nil, fmt.Errorf("xAI 第 %d 张参考图无效，本次未调用供应商：%w", index+1, err)
		}
		references = append(references, map[string]interface{}{"url": imageURL})
	}
	prompt, err := xaiReferenceVideoPrompt(input)
	if err != nil {
		return nil, err
	}
	body["prompt"] = prompt
	body["reference_images"] = references
	body["aspect_ratio"] = normalizeXAIVideoAspectRatio(input.Config.Size)
	return body, nil
}

func xaiReferenceVideoPrompt(input canvasGenerationInput) (string, error) {
	prompt := strings.TrimSpace(input.Prompt)
	// 画布 @ 引用会编译成“图片N”；xAI 参考视频协议只识别与数组顺序对应的 <IMAGE_N>。
	for index := len(input.ReferenceImages); index >= 1; index-- {
		tag := fmt.Sprintf("<IMAGE_%d>", index)
		prompt = strings.ReplaceAll(prompt, fmt.Sprintf("@图片%d", index), tag)
		prompt = strings.ReplaceAll(prompt, fmt.Sprintf("图片%d", index), tag)
		prompt = strings.ReplaceAll(prompt, fmt.Sprintf("参考图%d", index), tag)
	}
	appendGuidance := func(metadataKey, label, instruction string) error {
		imageID := metadataString(input.Metadata, metadataKey)
		if imageID == "" {
			return nil
		}
		for index, image := range input.ReferenceImages {
			if image.ID != imageID {
				continue
			}
			tag := fmt.Sprintf("<IMAGE_%d>", index+1)
			prompt = strings.TrimSpace(prompt + "\n\n" + fmt.Sprintf(instruction, tag))
			return nil
		}
		return fmt.Errorf("已选择的 xAI %s未包含在当前参考图中，本次未调用供应商", label)
	}
	if err := appendGuidance("videoStartFrameNodeId", "开场参考", "开场画面尽量采用 %s 的主体、构图和氛围作为视觉引导，但不要求锁定为精确首帧。"); err != nil {
		return "", err
	}
	if err := appendGuidance("videoEndFrameNodeId", "结尾参考", "在最后 1-2 秒尽量向 %s 的主体状态、构图和氛围过渡，但不要求锁定为精确尾帧。"); err != nil {
		return "", err
	}
	return prompt, nil
}

func downloadXAIVideoResult(ctx context.Context, config providerConfig, state map[string]interface{}) (map[string]interface{}, error) {
	var storedFileErr error
	if fileID := xaiVideoFileID(state); fileID != "" {
		data, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), config, "/files/"+url.PathEscape(fileID)+"/content")
		if err == nil {
			result, payloadErr := xaiVideoResultPayload(data, mimeType)
			if payloadErr == nil {
				return result, nil
			}
			storedFileErr = fmt.Errorf("xAI Files 视频内容无效：%w", payloadErr)
		} else {
			storedFileErr = fmt.Errorf("xAI Files 视频读取失败：%w", err)
		}
	}

	// 历史任务和部分兼容中转不会回传 file_output，仍可尝试官方临时 URL；这只是下载，不会重新生成视频。
	if videoURL := newAPIVideoResultURL(state); videoURL != "" {
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err == nil {
			result, payloadErr := xaiVideoResultPayload(data, mimeType)
			if payloadErr == nil {
				return result, nil
			}
			err = payloadErr
		}
		if storedFileErr != nil {
			err = errors.Join(storedFileErr, err)
		}
		return nil, xaiVideoResultError(err)
	}
	if storedFileErr != nil {
		return nil, xaiVideoResultError(storedFileErr)
	}
	return nil, xaiVideoResultError(errors.New("完成响应没有 file_id 或视频 URL"))
}

func xaiVideoFileID(state map[string]interface{}) string {
	return nestedXAIVideoFileID(state, 0)
}

func nestedXAIVideoFileID(value map[string]interface{}, depth int) string {
	if output, ok := value["file_output"].(map[string]interface{}); ok {
		if fileID := strings.TrimSpace(stringField(output, "file_id")); fileID != "" {
			return fileID
		}
	}
	if depth >= 3 {
		return ""
	}
	for _, key := range []string{"video", "result", "data"} {
		if nested, ok := value[key].(map[string]interface{}); ok {
			if fileID := nestedXAIVideoFileID(nested, depth+1); fileID != "" {
				return fileID
			}
		}
	}
	return ""
}

func xaiVideoResultPayload(data []byte, mimeType string) (map[string]interface{}, error) {
	mimeType = normalizedMediaMimeType(mimeType, data)
	if !strings.HasPrefix(mimeType, "video/") {
		return nil, errors.New("结果内容不是视频")
	}
	return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
}

func xaiVideoResultError(cause error) error {
	return publicTaskError{
		message: "xAI Imagine 视频已生成，但结果读取失败；请从任务详情查询原任务恢复，勿重新生成",
		cause:   cause,
	}
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
