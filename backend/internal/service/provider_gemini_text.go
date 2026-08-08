package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Gemini 文本任务始终先走原生 streamGenerateContent；同一请求返回完整 JSON 时只记录非流式风险，绝不补发第二笔 generateContent。
func runGeminiTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	body, err := geminiTextRequestBody(input)
	if err != nil {
		return nil, err
	}
	modelName := strings.TrimPrefix(strings.TrimSpace(input.Config.Model), "models/")
	if modelName == "" {
		return nil, errors.New("Gemini 文本任务缺少模型名")
	}
	path := "/models/" + url.PathEscape(modelName) + ":streamGenerateContent?alt=sse"
	raw, mimeType, delivery, err := postStreamingGeminiJSON(ctx, input.Config, path, body)
	if err != nil {
		return nil, err
	}
	text, streamed, err := geminiTextFromProviderBody(raw, mimeType)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("Gemini 文本接口没有返回内容")
	}
	return map[string]interface{}{
		"mode": "text", "text": text,
		"transport": providerTextTransport(streamed, delivery), "streamDelivery": delivery,
	}, nil
}

func geminiTextRequestBody(input canvasGenerationInput) (map[string]interface{}, error) {
	parts := make([]map[string]interface{}, 0, 1+len(input.ReferenceImages))
	parts = append(parts, map[string]interface{}{"text": input.Prompt})
	for _, image := range input.ReferenceImages {
		part, err := geminiImagePart(image)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	body := map[string]interface{}{
		"contents": []map[string]interface{}{{"role": "user", "parts": parts}},
	}
	if systemPrompt := strings.TrimSpace(input.Config.SystemPrompt); systemPrompt != "" {
		body["systemInstruction"] = map[string]interface{}{"parts": []map[string]interface{}{{"text": systemPrompt}}}
	}
	generationConfig := map[string]interface{}{}
	if input.MaxOutputTokens > 0 {
		generationConfig["maxOutputTokens"] = input.MaxOutputTokens
	}
	if responseMIMEType := strings.TrimSpace(input.Config.ResponseMIMEType); responseMIMEType != "" {
		generationConfig["responseMimeType"] = responseMIMEType
	}
	if len(generationConfig) > 0 {
		body["generationConfig"] = generationConfig
	}
	return body, nil
}

func geminiImagePart(media providerMedia) (map[string]interface{}, error) {
	value := strings.TrimSpace(firstNonEmpty(media.DataURL, media.URL))
	if strings.HasPrefix(value, "data:") {
		raw, mimeType, err := mediaBytes(providerMedia{DataURL: value, Type: media.Type, MimeType: media.MimeType})
		if err != nil {
			return nil, fmt.Errorf("读取 Gemini 参考图片失败：%w", err)
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return nil, errors.New("Gemini 文本参考素材必须是图片")
		}
		return map[string]interface{}{"inlineData": map[string]interface{}{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(raw)}}, nil
	}
	if isPublicMediaURL(value) {
		mimeType := strings.TrimSpace(strings.Split(firstNonEmpty(media.MimeType, media.Type, "image/png"), ";")[0])
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return nil, errors.New("Gemini 文本参考素材必须是图片")
		}
		return map[string]interface{}{"fileData": map[string]interface{}{"fileUri": value, "mimeType": mimeType}}, nil
	}
	return nil, errors.New("Gemini 文本参考图片需要公网 URL 或 base64 data URL")
}

func postStreamingGeminiJSON(ctx context.Context, config providerConfig, path string, body interface{}) ([]byte, string, providerStreamDelivery, error) {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return nil, "", providerStreamDelivery{}, err
	}
	observation := &providerResponseReadObservation{}
	requestContext := context.WithValue(ctx, providerResponseReadObservationKey{}, observation)
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, geminiAPIURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return nil, "", providerStreamDelivery{}, err
	}
	req.Header.Set("x-goog-api-key", config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	raw, mimeType, err := doBinary(req)
	return raw, mimeType, observation.snapshot(), err
}

func geminiTextFromProviderBody(raw []byte, mimeType string) (string, bool, error) {
	streamed := isEventStreamBody(raw, mimeType)
	var payloads []map[string]interface{}
	var err error
	if streamed {
		payloads, err = parseSSEJSONEvents(raw)
	} else {
		payloads, err = decodeGeminiTextPayloads(raw)
	}
	if err != nil {
		return "", streamed, err
	}
	text, err := geminiTextFromPayloads(payloads, streamed)
	return text, streamed, err
}

func decodeGeminiTextPayloads(raw []byte) ([]map[string]interface{}, error) {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("Gemini 文本接口返回无法解析：%w", err)
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		return []map[string]interface{}{typed}, nil
	case []interface{}:
		payloads := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			payload, ok := item.(map[string]interface{})
			if !ok {
				return nil, errors.New("Gemini 文本接口返回了无效的响应数组")
			}
			payloads = append(payloads, payload)
		}
		if len(payloads) == 0 {
			return nil, errors.New("Gemini 文本接口返回了空响应数组")
		}
		return payloads, nil
	default:
		return nil, errors.New("Gemini 文本接口返回的不是 JSON 对象")
	}
}

func geminiTextFromPayloads(payloads []map[string]interface{}, streamed bool) (string, error) {
	var content strings.Builder
	for _, payload := range payloads {
		if message := providerStreamError(payload); message != "" {
			return "", errors.New(message)
		}
		candidates, _ := payload["candidates"].([]interface{})
		for _, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]interface{})
			finishReason := strings.ToUpper(strings.TrimSpace(firstNonEmpty(stringField(candidate, "finishReason"), stringField(candidate, "finish_reason"))))
			if finishReason != "" && finishReason != "STOP" {
				prefix := "Gemini 文本响应明确未完整完成"
				if streamed {
					prefix = "上游流式响应明确未完整结束"
				}
				return "", fmt.Errorf("%s（%s），系统未使用半截结果且不会自动重试", prefix, finishReason)
			}
			candidateContent, _ := candidate["content"].(map[string]interface{})
			parts, _ := candidateContent["parts"].([]interface{})
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]interface{})
				if thought, _ := part["thought"].(bool); thought {
					// Gemini 思考片段不是用户可见答案，混入后会破坏分镜 JSON。
					continue
				}
				content.WriteString(stringField(part, "text"))
			}
		}
	}
	return content.String(), nil
}
