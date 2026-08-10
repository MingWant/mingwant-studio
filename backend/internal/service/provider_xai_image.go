package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const (
	xaiImageStoredResultExpiresAfterSeconds = 7 * 24 * 60 * 60
	xaiImageRecoveryLocatorPrefix           = "xai-file:"
	xaiImageRecoveryPollStagePrefix         = "xai_file_"
	xaiImageRecoveryMaxPolls                = 12
	xaiImageRecoveryPollInterval            = 15 * time.Second
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
	if providerRequestID := resumedProviderRequestID(ctx); providerRequestID != "" {
		if filename, ok := xaiImageRecoveryFilename(providerRequestID); ok {
			return recoverXAIImageStoredTask(ctx, input.Config, filename)
		}
		return nil, providerBillingReviewError{
			reason: "xAI 图片任务已有更早的供应商调用，但恢复标识无效；本次没有发出新的图片生成请求。请先核对供应商后台与原费用，不要直接重试",
			cause:  errors.New("invalid xAI image recovery locator"),
		}
	}
	path, body, err := xaiImageRequest(input)
	if err != nil {
		return nil, err
	}
	recoveryLocator, storedFilename := xaiImageRecoveryLocator(ctx)
	if storedFilename != "" {
		// 图片接口是同步请求且不支持 SSE。为网关响应丢失或媒体读取失败保留一个按计费尝试唯一、短期私有的结果副本，
		// 后续恢复只读取 Files API，不会再次创建图片或产生第二笔生成调用。
		body["storage_options"] = map[string]interface{}{
			"filename":      storedFilename,
			"expires_after": xaiImageStoredResultExpiresAfterSeconds,
		}
	}
	var payload imageResponse
	if err := postJSON(ctx, input.Config, path, body, &payload); err != nil {
		if shouldRecoverXAIImagineCreate(err) && recoveryLocator != "" {
			recoveryCtx, checkpointErr := checkpointXAIImageRecoveryLocator(ctx, recoveryLocator)
			if checkpointErr != nil {
				return nil, xaiImageCreateRecoveryError(errors.Join(err, checkpointErr))
			}
			result, recoveryErr := recoverXAIImageStoredTask(recoveryCtx, input.Config, storedFilename)
			if recoveryErr == nil || isProviderPollDeferred(recoveryErr) {
				return result, recoveryErr
			}
			return nil, xaiImageCreateRecoveryError(errors.Join(err, recoveryErr))
		}
		return nil, err
	}
	images, err := xaiImageDataURLs(ctx, input.Config, payload)
	if err == nil {
		return map[string]interface{}{"mode": "image", "images": images}, nil
	}
	if recoveryLocator == "" {
		return nil, err
	}
	// 供应商已返回 2xx 但内联内容、file_output 或临时 URL 不可用时，仍优先读取
	// 同一付费尝试的私有 Files 副本；保存恢复标识后即使进程中断也不会重放生成。
	recoveryCtx, checkpointErr := checkpointXAIImageRecoveryLocator(ctx, recoveryLocator)
	if checkpointErr != nil {
		return nil, xaiImageCreateRecoveryError(errors.Join(err, checkpointErr))
	}
	result, recoveryErr := recoverXAIImageStoredTask(recoveryCtx, input.Config, storedFilename)
	if recoveryErr == nil || isProviderPollDeferred(recoveryErr) {
		return result, recoveryErr
	}
	return nil, xaiImageCreateRecoveryError(errors.Join(err, recoveryErr))
}

func xaiImageRecoveryLocator(ctx context.Context) (string, string) {
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
	filename := "mingwant-image-" + hex.EncodeToString(sum[:16]) + ".jpg"
	return xaiImageRecoveryLocatorPrefix + filename, filename
}

func xaiImageRecoveryFilename(locator string) (string, bool) {
	locator = strings.TrimSpace(locator)
	if !strings.HasPrefix(locator, xaiImageRecoveryLocatorPrefix) {
		return "", false
	}
	filename := strings.TrimSpace(strings.TrimPrefix(locator, xaiImageRecoveryLocatorPrefix))
	if filename == "" || strings.ContainsAny(filename, `/\\?&#`) || !strings.HasPrefix(filename, "mingwant-image-") || !strings.HasSuffix(filename, ".jpg") {
		return "", false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(filename, "mingwant-image-"), ".jpg")
	if len(token) != 32 {
		return "", false
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", false
	}
	return filename, true
}

func isXAIImageRecoveryTask(task *model.Task) bool {
	if task == nil || !strings.HasPrefix(task.Type, "canvas_image") {
		return false
	}
	_, ok := xaiImageRecoveryFilename(task.ProviderRequestID)
	return ok
}

func checkpointXAIImageRecoveryLocator(ctx context.Context, locator string) (context.Context, error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || strings.TrimSpace(metadata.TaskID) == "" || strings.TrimSpace(metadata.LeaseOwner) == "" {
		return withProviderRequestID(ctx, locator), nil
	}
	if err := metadata.Service.repo.UpdateClaimedTaskProviderState(metadata.TaskID, metadata.LeaseOwner, locator, metadata.PollStage, nil); err != nil {
		return ctx, fmt.Errorf("保存 xAI 图片恢复标识失败：%w", err)
	}
	metadata.Service.logTaskEventBestEffort(metadata.UserID, metadata.TaskID, "warn", "xAI 图片响应或媒体读取未完成，开始查询原结果", "恢复过程只读取 xAI Files，不会重新发起图片生成")
	return withProviderRequestID(ctx, locator), nil
}

func recoverXAIImageStoredTask(ctx context.Context, config providerConfig, filename string) (map[string]interface{}, error) {
	images, err := xaiImageStoredResult(ctx, config, filename)
	if err == nil {
		if metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext); ok && metadata.Service != nil {
			metadata.Service.logTaskEventBestEffort(metadata.UserID, metadata.TaskID, "info", "已从 xAI Files 恢复图片", "未创建新的图片生成请求")
		}
		return map[string]interface{}{"mode": "image", "images": images}, nil
	}
	if shouldDeferXAIImageRecovery(err) {
		retry := providerPollRetryCount(ctx, xaiImageRecoveryPollStagePrefix) + 1
		if retry <= xaiImageRecoveryMaxPolls {
			return nil, deferProviderPoll(ctx, "等待 xAI Files 中的原图片结果", xaiImageRecoveryPollStagePrefix+strconv.Itoa(retry), xaiImageRecoveryPollInterval)
		}
	}
	return nil, xaiImageCreateRecoveryError(err)
}

func shouldDeferXAIImageRecovery(err error) bool {
	if errors.Is(err, errXAIStoredResultPending) {
		return true
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == 404 || httpErr.StatusCode == 501) {
		// Files 列表本身不存在或未实现表示当前中转不支持恢复，不应反复轮询同一错误端点。
		return false
	}
	return isTransientProviderPollError(err)
}

func xaiImageStoredResult(ctx context.Context, config providerConfig, filename string) ([]map[string]string, error) {
	fileID, err := xaiStoredFileID(ctx, config, filename)
	if err != nil {
		return nil, err
	}
	raw, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), config, "/files/"+url.PathEscape(fileID)+"/content")
	if err != nil {
		return nil, err
	}
	mimeType = normalizedMediaMimeType(mimeType, raw)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, errors.New("xAI Files 返回的内容不是图片")
	}
	return []map[string]string{{"dataUrl": dataURL(mimeType, raw)}}, nil
}

func isProviderPollDeferred(err error) bool {
	var deferred *providerPollDeferredError
	return errors.As(err, &deferred)
}

func xaiImageCreateRecoveryError(cause error) error {
	return providerBillingReviewError{
		reason: "xAI Imagine 图片创建响应或结果读取未完整结束；系统没有重新生图，供应商可能已经完成并计费，请勿直接重试。若任务详情显示“手动查询任务”，可稍后用它从 xAI Files 取回原结果；否则请到供应商后台下载原图或改用官方 xAI 直连渠道。图片接口不使用 SSE",
		cause:  cause,
	}
}

// 保留旧名称，避免把既有 524 诊断与测试误解为另一套处理分支。
func xaiImageGatewayTimeoutError(cause error) error {
	return xaiImageCreateRecoveryError(cause)
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
	// 官方单图编辑始终沿用输入图比例；只有多图编辑允许显式覆盖画幅。
	if len(images) > 1 && aspectRatio != "auto" {
		body["aspect_ratio"] = aspectRatio
	}
	return "/images/edits", body, nil
}

// xAI 默认返回临时 URL。任务必须立刻把结果物化为内联媒体，后续资源落盘才不会
// 依赖会过期的供应商地址；这个下载不会再次触发图片生成或编辑计费。
func xaiImageDataURLs(ctx context.Context, config providerConfig, payload imageResponse) ([]map[string]string, error) {
	if len(payload.Data) == 0 {
		return nil, errors.New("xAI Imagine 接口没有返回图片")
	}
	images := make([]map[string]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		var storedFileErr error
		if encoded, ok := item["b64_json"].(string); ok && strings.TrimSpace(encoded) != "" {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil {
				mimeType := normalizedMediaMimeType(defaultString(stringField(item, "mime_type"), "image/jpeg"), raw)
				if strings.HasPrefix(mimeType, "image/") {
					images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
					continue
				}
				err = errors.New("上游返回的 base64 不是图片")
			}
			storedFileErr = err
		}
		if output, ok := item["file_output"].(map[string]interface{}); ok {
			fileID := firstNonEmpty(strings.TrimSpace(stringField(output, "file_id")), strings.TrimSpace(stringField(output, "id")))
			if fileID != "" {
				raw, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), config, "/files/"+url.PathEscape(fileID)+"/content")
				if err == nil {
					mimeType = normalizedMediaMimeType(mimeType, raw)
					if strings.HasPrefix(mimeType, "image/") {
						images = append(images, map[string]string{"dataUrl": dataURL(mimeType, raw)})
						continue
					}
					err = errors.New("xAI Files 返回的内容不是图片")
				}
				storedFileErr = errors.Join(storedFileErr, err)
			}
		}
		url := strings.TrimSpace(stringField(item, "url"))
		if url == "" {
			if storedFileErr != nil {
				return nil, xaiImageResultError(storedFileErr)
			}
			continue
		}
		raw, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), url)
		if err != nil {
			if storedFileErr != nil {
				err = errors.Join(storedFileErr, err)
			}
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
		message: "xAI Imagine 已返回图片结果，但本系统读取上游媒体失败；费用可能已经产生，请勿立即重试",
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
