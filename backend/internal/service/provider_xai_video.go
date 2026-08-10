package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const (
	xaiVideoStoredResultExpiresAfterSeconds = 7 * 24 * 60 * 60
	xaiVideoRecoveryLocatorPrefix           = "xai-video-file:"
	xaiVideoRecoveryPollStagePrefix         = "xai_video_file_"
	xaiVideoRecoveryMaxPolls                = 80
	xaiVideoRecoveryPollInterval            = 15 * time.Second
)

// 官方 Grok Imagine 视频必须走 /videos/* JSON。历史配置只有在误选通用
// OpenAI Compatible Videos 时才按模型名纠正，其他显式视频协议保持原契约。
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

// xAI 的生成、编辑和续写共享轮询接口，但创建端点和允许字段不同。先按操作拆分请求，
// 避免把 generation 的时长、画幅或分辨率误带到 edits/extensions 后触发上游 400。
func xaiVideoRequest(input canvasGenerationInput) (string, map[string]interface{}, error) {
	body := map[string]interface{}{
		"model":  input.Config.Model,
		"prompt": strings.TrimSpace(input.Prompt),
		// 视频没有 base64 返回模式。保存一份短期私有 Files 结果，避免部署网络必须直连 vidgen.x.ai。
		"storage_options": map[string]interface{}{
			"filename":      "mingwant-video.mp4",
			"expires_after": xaiVideoStoredResultExpiresAfterSeconds,
		},
	}
	operation := metadataString(input.Metadata, "videoEditOperation")
	if operation == "" {
		switch {
		case len(input.ReferenceVideos) > 0:
			operation = "extend"
		case len(input.ReferenceImages) > 0:
			operation = "image_to_video"
		default:
			operation = "text_to_video"
		}
	}
	switch operation {
	case "edit_video":
		video, err := xaiSourceVideoInput(input, "编辑")
		if err != nil {
			return "", nil, err
		}
		if duration := input.ReferenceVideos[0].DurationMs; duration > 8_700 {
			return "", nil, errors.New("xAI 视频编辑的原片最长为 8.7 秒，本次未调用供应商")
		}
		body["video"] = video
		return "/videos/edits", body, nil
	case "extend":
		video, err := xaiSourceVideoInput(input, "续写")
		if err != nil {
			return "", nil, err
		}
		if duration := input.ReferenceVideos[0].DurationMs; duration > 0 && (duration < 2_000 || duration > 15_000) {
			return "", nil, errors.New("xAI 视频续写的原片时长必须为 2-15 秒，本次未调用供应商")
		}
		extensionDuration, err := xaiVideoExtensionDuration(input.Config.VideoSeconds)
		if err != nil {
			return "", nil, err
		}
		body["duration"] = extensionDuration
		body["video"] = video
		return "/videos/extensions", body, nil
	case "text_to_video", "image_to_video", "reference_to_video":
	default:
		return "", nil, fmt.Errorf("xAI 官方视频不支持生成模式 %q，本次未调用供应商", operation)
	}

	if len(input.ReferenceAudios) > 0 {
		return "", nil, errors.New("xAI 公共视频接口的 reference_audios 只接受预设 voice_id，不能直接连接画布音频文件，本次未调用供应商")
	}
	if len(input.ReferenceVideos) > 0 {
		return "", nil, errors.New("xAI 视频生成只接受文本、起始图或参考图；编辑和续写请改用对应模式，本次未调用供应商")
	}
	body["duration"] = normalizeXAIVideoDuration(input.Config.VideoSeconds)
	body["resolution"] = normalizeXAIVideoResolution(input.Config.VQuality)
	if operation == "reference_to_video" {
		result, err := xaiReferenceVideoBody(body, input)
		return "/videos/generations", result, err
	}
	if metadataString(input.Metadata, "videoEndFrameNodeId") != "" {
		return "", nil, errors.New("xAI 图生视频不支持指定尾帧，本次未调用供应商")
	}
	if operation == "image_to_video" && len(input.ReferenceImages) == 0 {
		return "", nil, errors.New("xAI 图生视频必须提供 1 张起始图，本次未调用供应商")
	}
	useStartImage := shouldSendNewAPIVideoImages(input) && len(input.ReferenceImages) > 0
	if !useStartImage {
		if len(input.ReferenceImages) > 0 {
			return "", nil, errors.New("xAI 文生视频不能同时携带图片；请切换到图生视频或多参考图模式，本次未调用供应商")
		}
		body["aspect_ratio"] = normalizeXAIVideoAspectRatio(input.Config.Size)
		return "/videos/generations", body, nil
	}
	if len(input.ReferenceImages) > 1 {
		return "", nil, fmt.Errorf("xAI 图生视频只支持 1 张起始图，当前连接了 %d 张，本次未调用供应商", len(input.ReferenceImages))
	}
	imageURL, err := openAIImageInputURL(input.ReferenceImages[0])
	if err != nil {
		return "", nil, fmt.Errorf("xAI 起始图无效，本次未调用供应商：%w", err)
	}
	body["image"] = map[string]interface{}{"url": imageURL}
	// 官方协议在未指定画幅时会沿用起始图比例，不能把 auto 强制改成 16:9。
	if !isXAIVideoAutomaticAspectRatio(input.Config.Size) {
		body["aspect_ratio"] = normalizeXAIVideoAspectRatio(input.Config.Size)
	}
	return "/videos/generations", body, nil
}

// 保留纯请求体入口供协议单元测试与其他调用方使用；真实创建必须使用 xaiVideoRequest 返回的端点。
func xaiVideoBody(input canvasGenerationInput) (map[string]interface{}, error) {
	_, body, err := xaiVideoRequest(input)
	return body, err
}

func xaiVideoRecoveryLocator(ctx context.Context) (string, string) {
	metadata, _ := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if strings.TrimSpace(metadata.TaskID) == "" {
		return "", ""
	}
	attempt := metadata.TaskAttempts
	if attempt < 1 {
		attempt = 1
	}
	seed := strings.TrimSpace(metadata.TaskID) + "\x00" + strings.TrimSpace(metadata.BillingOrderID) + "\x00" + strconv.Itoa(attempt)
	sum := sha256.Sum256([]byte(seed))
	filename := "mingwant-video-" + hex.EncodeToString(sum[:16]) + ".mp4"
	return xaiVideoRecoveryLocatorPrefix + filename, filename
}

func xaiVideoRecoveryFilename(locator string) (string, bool) {
	locator = strings.TrimSpace(locator)
	if !strings.HasPrefix(locator, xaiVideoRecoveryLocatorPrefix) {
		return "", false
	}
	filename := strings.TrimSpace(strings.TrimPrefix(locator, xaiVideoRecoveryLocatorPrefix))
	if filename == "" || strings.ContainsAny(filename, `/\\?&#`) || !strings.HasPrefix(filename, "mingwant-video-") || !strings.HasSuffix(filename, ".mp4") {
		return "", false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(filename, "mingwant-video-"), ".mp4")
	if len(token) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", false
	}
	return filename, true
}

func checkpointXAIVideoRecoveryLocator(ctx context.Context, locator string) (context.Context, error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || strings.TrimSpace(metadata.TaskID) == "" || strings.TrimSpace(metadata.LeaseOwner) == "" {
		return withProviderRequestID(ctx, locator), nil
	}
	if err := metadata.Service.repo.UpdateClaimedTaskProviderState(metadata.TaskID, metadata.LeaseOwner, locator, metadata.PollStage, nil); err != nil {
		return ctx, fmt.Errorf("保存 xAI 视频恢复标识失败：%w", err)
	}
	metadata.Service.logTaskEventBestEffort(metadata.UserID, metadata.TaskID, "warn", "xAI 视频创建响应可能丢失，开始查询原结果", "恢复过程只读取 xAI Files，不会重新发起视频创建")
	return withProviderRequestID(ctx, locator), nil
}

func recoverXAIVideoStoredTask(ctx context.Context, config providerConfig, filename string) (map[string]interface{}, error) {
	fileID, err := xaiStoredFileID(ctx, config, filename)
	if err == nil {
		raw, mimeType, downloadErr := getBinary(withProviderRequestKind(ctx, "download"), config, "/files/"+url.PathEscape(fileID)+"/content")
		if downloadErr == nil {
			result, payloadErr := xaiVideoResultPayload(raw, mimeType)
			if payloadErr == nil {
				if metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext); ok && metadata.Service != nil {
					metadata.Service.logTaskEventBestEffort(metadata.UserID, metadata.TaskID, "info", "已从 xAI Files 恢复视频", "未创建新的视频任务")
				}
				return result, nil
			}
			err = payloadErr
		} else {
			err = downloadErr
		}
	}
	if shouldDeferXAIVideoRecovery(err) {
		retry := providerPollRetryCount(ctx, xaiVideoRecoveryPollStagePrefix) + 1
		if retry <= xaiVideoRecoveryMaxPolls {
			return nil, deferProviderPoll(ctx, "等待 xAI Files 中的原视频结果", xaiVideoRecoveryPollStagePrefix+strconv.Itoa(retry), xaiVideoRecoveryPollInterval)
		}
	}
	return nil, xaiVideoCreateRecoveryError(err)
}

func shouldDeferXAIVideoRecovery(err error) bool {
	if errors.Is(err, errXAIStoredResultPending) {
		return true
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == 404 || httpErr.StatusCode == 501) {
		return false
	}
	return isTransientProviderPollError(err)
}

func xaiVideoCreateRecoveryError(cause error) error {
	return providerBillingReviewError{
		reason: "xAI Imagine 视频创建请求可能已经成功，但响应或原结果读取失败；系统没有重新创建视频，费用可能已经产生，请勿直接重试。可稍后从任务详情查询原结果，系统只会读取本次尝试的 xAI Files 副本",
		cause:  cause,
	}
}

func xaiSourceVideoInput(input canvasGenerationInput, label string) (map[string]interface{}, error) {
	if len(input.ReferenceImages) > 0 || len(input.ReferenceAudios) > 0 || len(input.ReferenceVideos) != 1 {
		return nil, fmt.Errorf("xAI 视频%s必须且只能连接 1 段 MP4 原片，不能混用图片或音频，本次未调用供应商", label)
	}
	media := input.ReferenceVideos[0]
	value := strings.TrimSpace(media.DataURL)
	if value != "" {
		if strings.HasPrefix(strings.ToLower(value), "data:video/mp4") {
			return map[string]interface{}{"url": value}, nil
		}
		return nil, fmt.Errorf("xAI 视频%s原片必须是 MP4，本次未调用供应商", label)
	}
	value = strings.TrimSpace(media.URL)
	if strings.HasPrefix(strings.ToLower(value), "data:video/mp4") {
		return map[string]interface{}{"url": value}, nil
	}
	if !isPublicMediaURL(value) {
		return nil, fmt.Errorf("xAI 视频%s原片需要公网 URL 或 MP4 base64，本次未调用供应商", label)
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Path), ".mp4") {
		return nil, fmt.Errorf("xAI 视频%s原片 URL 必须以 .mp4 结尾，本次未调用供应商", label)
	}
	return map[string]interface{}{"url": value}, nil
}

func xaiVideoExtensionDuration(value string) (int, error) {
	duration, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || duration < 2 || duration > 10 {
		return 0, errors.New("xAI 视频续写的新增时长必须为 2-10 秒，本次未调用供应商")
	}
	return duration, nil
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
