package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
)

const grok2APIVideoPollInterval = 2500 * time.Millisecond

func isGrok2APIVideoConfig(config providerConfig) bool {
	return strings.TrimSpace(config.InterfaceType) == string(model.ChannelInterfaceGrok2APIVideo)
}

// grok2api 的公开视频接口沿用 xAI 的创建与轮询路径，但明确拒绝 storage_options、
// output.upload_url 以及 edits/extensions。必须作为独立协议构造，不能复用官方 xAI 请求体。
func grok2APIVideoBody(input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return nil, errors.New("grok2api 公开视频接口只接受文本和图片，不能连接参考视频或音频，本次未调用供应商")
	}
	operation := metadataString(input.Metadata, "videoEditOperation")
	if operation == "" {
		if len(input.ReferenceImages) > 0 {
			operation = "image_to_video"
		} else {
			operation = "text_to_video"
		}
	}
	body := map[string]interface{}{
		"model":        input.Config.Model,
		"prompt":       strings.TrimSpace(input.Prompt),
		"duration":     normalizeXAIVideoDuration(input.Config.VideoSeconds),
		"aspect_ratio": normalizeXAIVideoAspectRatio(input.Config.Size),
		"resolution":   normalizeGrok2APIVideoResolution(input.Config.VQuality),
	}
	switch operation {
	case "text_to_video":
		if len(input.ReferenceImages) > 0 {
			return nil, errors.New("grok2api 文生视频不能同时携带图片；请切换到图生视频，本次未调用供应商")
		}
	case "image_to_video":
		if len(input.ReferenceImages) != 1 {
			return nil, fmt.Errorf("grok2api 图生视频必须且只能提供 1 张起始图，当前为 %d 张，本次未调用供应商", len(input.ReferenceImages))
		}
		if metadataString(input.Metadata, "videoEndFrameNodeId") != "" {
			return nil, errors.New("grok2api 图生视频不支持指定尾帧；可使用支持多参考图的模型并改成参考图模式做软引导，本次未调用供应商")
		}
		imageURL, err := openAIImageInputURL(input.ReferenceImages[0])
		if err != nil {
			return nil, fmt.Errorf("grok2api 起始图无效，本次未调用供应商：%w", err)
		}
		body["image"] = map[string]interface{}{"url": imageURL}
	case "reference_to_video":
		if len(input.ReferenceImages) == 0 {
			return nil, errors.New("grok2api 参考图模式至少需要 1 张图片，本次未调用供应商")
		}
		references := make([]map[string]interface{}, 0, len(input.ReferenceImages))
		for index, image := range input.ReferenceImages {
			imageURL, err := openAIImageInputURL(image)
			if err != nil {
				return nil, fmt.Errorf("grok2api 第 %d 张参考图无效，本次未调用供应商：%w", index+1, err)
			}
			references = append(references, map[string]interface{}{"url": imageURL})
		}
		body["prompt"] = grok2APIReferencePrompt(input)
		body["reference_images"] = references
	default:
		return nil, fmt.Errorf("grok2api 公开视频接口不支持生成模式 %q，本次未调用供应商", operation)
	}
	return body, nil
}

func grok2APIReferencePrompt(input canvasGenerationInput) string {
	prompt := strings.TrimSpace(input.Prompt)
	appendGuidance := func(metadataKey, instruction string) {
		imageID := metadataString(input.Metadata, metadataKey)
		if imageID == "" {
			return
		}
		for index, image := range input.ReferenceImages {
			if image.ID == imageID {
				prompt = strings.TrimSpace(prompt + "\n\n" + fmt.Sprintf(instruction, index+1))
				return
			}
		}
	}
	appendGuidance("videoStartFrameNodeId", "开场画面尽量采用第 %d 张参考图的主体、构图和氛围，但不要求锁定为精确首帧。")
	appendGuidance("videoEndFrameNodeId", "在最后 1-2 秒尽量向第 %d 张参考图的主体状态、构图和氛围过渡，但不要求锁定为精确尾帧。")
	return prompt
}

func runGrok2APIVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	if id == "" {
		body, err := grok2APIVideoBody(input)
		if err != nil {
			return nil, err
		}
		var created map[string]interface{}
		if err := postJSON(ctx, input.Config, "/videos/generations", body, &created); err != nil {
			return nil, grok2APIVideoCreateError(err)
		}
		id = firstNonEmptyString(stringField(created, "request_id"), stringField(created, "id"))
		if id == "" {
			if data, ok := created["data"].(map[string]interface{}); ok {
				id = firstNonEmptyString(stringField(data, "request_id"), stringField(data, "id"))
			}
		}
		if id == "" {
			return nil, providerBillingReviewError{
				reason: "grok2api 视频创建接口已成功返回但缺少 request_id；费用可能已经产生，请勿立即重试",
				cause:  errors.New("grok2api video create response missing request_id"),
			}
		}
	}
	id = strings.TrimSpace(id)
	ctx = withProviderRequestID(ctx, id)
	var state map[string]interface{}
	if err := getJSON(ctx, input.Config, "/videos/"+url.PathEscape(id), &state); err != nil {
		return nil, publicTaskError{message: "grok2api 视频原任务查询失败；系统没有重新创建视频，请稍后从任务详情继续查询", cause: err}
	}
	if data, ok := state["data"].(map[string]interface{}); ok {
		state = data
	}
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyString(stringField(state, "status"), stringField(state, "state"))))
	switch status {
	case "done", "completed", "succeeded", "success", "ready":
		data, mimeType, err := getBinary(withProviderRequestKind(ctx, "download"), input.Config, "/videos/"+url.PathEscape(id)+"/content")
		if err != nil {
			return nil, grok2APIVideoResultError(err)
		}
		mimeType = normalizedMediaMimeType(mimeType, data)
		if !strings.HasPrefix(mimeType, "video/") {
			return nil, grok2APIVideoResultError(errors.New("content endpoint did not return video"))
		}
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
	case "failed", "error", "cancelled", "canceled", "expired":
		return nil, publicTaskError{message: "grok2api 视频生成失败；系统不会自动重新创建任务", cause: errors.New("grok2api video job failed")}
	case "pending", "queued", "processing", "in_progress", "running":
		return nil, deferProviderPoll(ctx, status, status, grok2APIVideoPollInterval)
	default:
		return nil, publicTaskError{message: "grok2api 视频原任务返回了无法识别的状态；系统没有重新创建视频", cause: fmt.Errorf("unknown grok2api video status %q", status)}
	}
}

func normalizeGrok2APIVideoResolution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "480", "480p", "low":
		return "480p"
	case "1080", "1080p", "high":
		return "1080p"
	default:
		return "720p"
	}
}

func grok2APIVideoCreateError(err error) error {
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		return err
	}
	return publicTaskError{
		message: "已按 grok2api 原生协议请求 POST /v1/videos/generations，但当前 Base URL 没有开放该路由；若外层是 New API 中转，需要中转方同时透传创建、查询和 /content 路由。本次请求已被明确拒绝，系统不会自动换路径重发",
		cause:   err,
	}
}

func grok2APIVideoResultError(cause error) error {
	return publicTaskError{
		message: "grok2api 视频任务已完成，但鉴权内容接口读取失败；费用可能已经产生，请从原任务继续查询，勿重新生成",
		cause:   cause,
	}
}
