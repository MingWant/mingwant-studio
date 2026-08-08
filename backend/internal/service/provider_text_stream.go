package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 50ms 远高于本机读取几百 token 缓冲正文的时间，又能让高速模型的短探针保留通过机会。
const minimumProgressiveStreamSpan = 50 * time.Millisecond

type providerStreamDelivery struct {
	FirstByteMs         int64 `json:"firstByteMs,omitempty"`
	DeliverySpanMs      int64 `json:"deliverySpanMs,omitempty"`
	LongestReadWaitMs   int64 `json:"longestReadWaitMs,omitempty"`
	TotalFollowupWaitMs int64 `json:"totalFollowupWaitMs,omitempty"`
	ReadCount           int   `json:"readCount,omitempty"`
	Progressive         bool  `json:"progressive"`
}

type providerResponseReadObservationKey struct{}

type providerResponseReadObservation struct {
	requestStartedAt  time.Time
	firstReadAt       time.Time
	lastReadAt        time.Time
	readCount         int
	progressive       bool
	longestReadWait   time.Duration
	totalFollowupWait time.Duration
}

type observedProviderResponseReader struct {
	source      io.Reader
	observation *providerResponseReadObservation
}

func (reader *observedProviderResponseReader) Read(buffer []byte) (int, error) {
	readStartedAt := time.Now()
	read, err := reader.source.Read(buffer)
	if read > 0 {
		reader.observation.observeReadAt(read, readStartedAt, time.Now())
	}
	return read, err
}

func (observation *providerResponseReadObservation) begin(startedAt time.Time) {
	observation.requestStartedAt = startedAt
}

func (observation *providerResponseReadObservation) observeReadAt(read int, readStartedAt time.Time, observedAt time.Time) {
	if observation == nil || read <= 0 {
		return
	}
	if observation.firstReadAt.IsZero() {
		observation.firstReadAt = observedAt
	} else {
		readWait := observedAt.Sub(readStartedAt)
		if readWait > observation.longestReadWait {
			observation.longestReadWait = readWait
		}
		if readWait > 0 {
			observation.totalFollowupWait += readWait
		}
		if observation.totalFollowupWait >= minimumProgressiveStreamSpan {
			// 只认后续上游 Read 自身的等待时间；本地解析、浏览器背压或调度停顿不能把一次性缓冲正文误判成渐进分片。
			observation.progressive = true
		}
	}
	observation.lastReadAt = observedAt
	observation.readCount++
}

func (observation *providerResponseReadObservation) snapshot() providerStreamDelivery {
	if observation == nil || observation.firstReadAt.IsZero() {
		return providerStreamDelivery{}
	}
	firstByte := observation.firstReadAt.Sub(observation.requestStartedAt)
	if observation.requestStartedAt.IsZero() || firstByte < 0 {
		firstByte = 0
	}
	span := observation.lastReadAt.Sub(observation.firstReadAt)
	if span < 0 {
		span = 0
	}
	return providerStreamDelivery{
		FirstByteMs:         firstByte.Milliseconds(),
		DeliverySpanMs:      span.Milliseconds(),
		LongestReadWaitMs:   observation.longestReadWait.Milliseconds(),
		TotalFollowupWaitMs: observation.totalFollowupWait.Milliseconds(),
		ReadCount:           observation.readCount,
		Progressive:         observation.readCount > 1 && observation.progressive,
	}
}

func providerTextTransport(streamed bool, delivery providerStreamDelivery) string {
	if !streamed {
		return "non-stream-compatible"
	}
	// 合法 SSE 只证明协议格式；正文首尾在可观测时间上分开到达，才能证明网关没有把整个短响应攒到最后一次性吐出。
	if delivery.Progressive {
		return "stream"
	}
	return "stream-unverified"
}

// 文本任务在 Worker 内消费 SSE，不把半成品直接暴露给页面；持续读取上游数据可降低长回答被空闲网关截断的概率。
func runStreamingResponsesTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	responseInput, err := textResponseInput(input)
	if err != nil {
		return nil, err
	}
	body := responsesTextRequestBody(input, responseInput, true)
	raw, mimeType, delivery, err := postStreamingJSON(ctx, input.Config, "/responses", body)
	if err != nil {
		return nil, err
	}
	text, streamed, err := responsesTextFromProviderBody(raw, mimeType)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("文本接口没有返回内容")
	}
	return map[string]interface{}{"mode": "text", "text": text, "transport": providerTextTransport(streamed, delivery), "streamDelivery": delivery}, nil
}

func runStreamingChatCompletionsTextTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	messages := []map[string]interface{}{}
	if systemPrompt := strings.TrimSpace(input.Config.SystemPrompt); systemPrompt != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": systemPrompt})
	}
	userContent, err := textChatContent(input)
	if err != nil {
		return nil, err
	}
	messages = append(messages, map[string]interface{}{"role": "user", "content": userContent})
	body := chatCompletionsTextRequestBody(input, messages, true)
	raw, mimeType, delivery, err := postStreamingJSON(ctx, input.Config, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	text, streamed, err := chatTextFromProviderBody(raw, mimeType)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("文本接口没有返回内容")
	}
	return map[string]interface{}{"mode": "text", "text": text, "transport": providerTextTransport(streamed, delivery), "streamDelivery": delivery}, nil
}

func responsesTextRequestBody(input canvasGenerationInput, responseInput interface{}, stream bool) map[string]interface{} {
	body := map[string]interface{}{"model": input.Config.Model, "input": responseInput}
	if stream {
		body["stream"] = true
	}
	if input.MaxOutputTokens > 0 {
		body["max_output_tokens"] = input.MaxOutputTokens
	}
	return body
}

func chatCompletionsTextRequestBody(input canvasGenerationInput, messages []map[string]interface{}, stream bool) map[string]interface{} {
	body := map[string]interface{}{"model": input.Config.Model, "messages": messages}
	if stream {
		body["stream"] = true
	}
	if strings.EqualFold(strings.TrimSpace(input.Config.ResponseMIMEType), "application/json") && kimiJSONModeModel(input.Config.Model) {
		// Kimi 的 JSON Mode 能从协议层约束对象输出；只对明确识别的 Kimi/Moonshot
		// 模型启用，避免把 response_format 发送给不支持该扩展的普通兼容网关。
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if input.MaxOutputTokens > 0 {
		// 使用当前 Chat Completions 字段；Kimi K3 等推理模型默认输出上限很大，探针不能无界等待。
		body["max_completion_tokens"] = input.MaxOutputTokens
	}
	if kimiK3Model(input.Config.Model) && (input.MaxOutputTokens > 0 || input.Config.RequireStreaming) {
		// Kimi K3 的最高推理档会显著拉长首包等待；低成本探针与要求长连接的分镜都显式使用 low，避免默认 max 把请求拖到网关上限。
		body["reasoning_effort"] = "low"
	}
	return body
}

func kimiK3Model(model string) bool {
	value := strings.ToLower(strings.TrimSpace(model))
	for _, separator := range []string{"/", ":", "_"} {
		value = strings.ReplaceAll(value, separator, "-")
	}
	return value == "kimi-k3" || strings.HasPrefix(value, "kimi-k3-") || strings.Contains(value, "-kimi-k3-") || strings.HasSuffix(value, "-kimi-k3")
}

func kimiJSONModeModel(model string) bool {
	value := strings.ToLower(strings.TrimSpace(model))
	for _, separator := range []string{"/", ":", "_"} {
		value = strings.ReplaceAll(value, separator, "-")
	}
	// K3-LS、K2.7 Code/K2.6 等第三方兼容别名可能只实现基础 Chat
	// Completions。文本测活不带 response_format 仍可通过，但把 JSON Mode
	// 扩展发给这些网关会直接得到 400；分镜提示词本身已经要求只返回 JSON，
	// 因此对已知别名关闭扩展，保留官方 Kimi/Moonshot 的协议级约束。
	for _, alias := range []string{
		"kimi-k3-ls",
		"kimi-k2.7-code",
		"kimi-k2-7-code",
		"kimi-k27-code",
		"kimi-k2.6",
		"kimi-k2-6",
		"kimi-k26",
	} {
		if strings.Contains(value, alias) {
			return false
		}
	}
	return strings.Contains(value, "kimi") || strings.Contains(value, "moonshot")
}

func postStreamingJSON(ctx context.Context, config providerConfig, path string, body interface{}) ([]byte, string, providerStreamDelivery, error) {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return nil, "", providerStreamDelivery{}, err
	}
	observation := &providerResponseReadObservation{}
	requestContext := context.WithValue(ctx, providerResponseReadObservationKey{}, observation)
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, apiURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return nil, "", providerStreamDelivery{}, err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	// 明确只协商 SSE，避免部分网关因 Accept 同时包含 JSON 而选择缓冲到最终结果；若其仍返回完整 JSON，解析层会记录非流式风险。
	req.Header.Set("Accept", "text/event-stream")
	raw, mimeType, err := doBinary(req)
	return raw, mimeType, observation.snapshot(), err
}

func responsesTextFromProviderBody(raw []byte, mimeType string) (string, bool, error) {
	if !isEventStreamBody(raw, mimeType) {
		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return "", false, fmt.Errorf("文本接口返回无法解析：%w", err)
		}
		if err := validateResponsesCompletion(payload); err != nil {
			return "", false, err
		}
		text := stringField(payload, "output_text")
		if text == "" {
			text = extractResponseText(payload)
		}
		return text, false, nil
	}
	events, err := parseSSEJSONEvents(raw)
	if err != nil {
		return "", true, err
	}
	var deltas strings.Builder
	completedText := ""
	for _, event := range events {
		if message := providerStreamError(event); message != "" {
			return "", true, errors.New(message)
		}
		if err := validateResponsesCompletion(event); err != nil {
			return "", true, err
		}
		typeName := stringField(event, "type")
		event = unwrapProviderStreamPayload(event)
		if nestedType := stringField(event, "type"); nestedType != "" {
			typeName = nestedType
		}
		if typeName == "response.output_text.delta" {
			deltas.WriteString(streamContentText(event["delta"]))
		}
		if typeName == "response.output_text.done" {
			completedText = firstNonEmpty(stringField(event, "text"), stringField(event, "output_text"))
		}
		if response, ok := event["response"].(map[string]interface{}); ok {
			if text := firstNonEmpty(stringField(response, "output_text"), extractResponseText(response)); text != "" {
				completedText = text
			}
		}
		if text := stringField(event, "output_text"); text != "" {
			completedText = text
		}
	}
	if deltas.Len() > 0 {
		return deltas.String(), true, nil
	}
	return completedText, true, nil
}

func chatTextFromProviderBody(raw []byte, mimeType string) (string, bool, error) {
	if !isEventStreamBody(raw, mimeType) {
		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return "", false, fmt.Errorf("文本接口返回无法解析：%w", err)
		}
		if err := validateChatCompletionFinishReasons(payload); err != nil {
			return "", false, err
		}
		return extractChatCompletionText(payload), false, nil
	}
	events, err := parseSSEJSONEvents(raw)
	if err != nil {
		return "", true, err
	}
	var content strings.Builder
	for _, event := range events {
		if message := providerStreamError(event); message != "" {
			return "", true, errors.New(message)
		}
		event = unwrapProviderStreamPayload(event)
		choices, _ := event["choices"].([]interface{})
		for _, item := range choices {
			choice, _ := item.(map[string]interface{})
			if err := validateChatCompletionFinishReason(choice); err != nil {
				return "", true, err
			}
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				content.WriteString(streamContentText(delta["content"]))
				continue
			}
			if message, ok := choice["message"].(map[string]interface{}); ok {
				content.WriteString(streamContentText(message["content"]))
				continue
			}
			content.WriteString(streamContentText(choice["text"]))
		}
	}
	return content.String(), true, nil
}

// Chat 兼容网关可能在有正文后以 length/max_tokens 结束；只检查 SSE 终态会把半截
// JSON 交给分镜解析并触发第二次付费修复。显式截断必须在供应商调用边界内失败，
// 由任务账务转入待核对，而不是把部分文本当作成功结果。
func validateChatCompletionFinishReasons(payload map[string]interface{}) error {
	payload = unwrapProviderStreamPayload(payload)
	choices, _ := payload["choices"].([]interface{})
	for _, item := range choices {
		choice, _ := item.(map[string]interface{})
		if err := validateChatCompletionFinishReason(choice); err != nil {
			return err
		}
	}
	return nil
}

// Responses 兼容网关可能在返回 output_text 后仍把整体状态标为 incomplete；不能
// 只看正文是否存在，否则分镜会把达到 max_output_tokens 的半截 JSON 当作成功。
func validateResponsesCompletion(payload map[string]interface{}) error {
	candidates := providerStreamPayloadVariants(payload)
	for _, candidate := range append([]map[string]interface{}{}, candidates...) {
		if response, ok := candidate["response"].(map[string]interface{}); ok && response != nil {
			candidates = append(candidates, response)
		}
	}
	for _, candidate := range candidates {
		status := strings.ToLower(strings.TrimSpace(stringField(candidate, "status")))
		if status != "incomplete" && status != "failed" && status != "cancelled" {
			if details, ok := candidate["incomplete_details"].(map[string]interface{}); !ok || strings.TrimSpace(stringField(details, "reason")) == "" {
				continue
			}
		}
		return providerBillingReviewError{reason: "上游 Responses 响应未完整结束，结果可能不完整且费用状态待核对；系统未使用半截结果且不会自动重试"}
	}
	return nil
}

func validateChatCompletionFinishReason(choice map[string]interface{}) error {
	if choice == nil {
		return nil
	}
	reason := strings.ToLower(strings.TrimSpace(firstNonEmpty(stringField(choice, "finish_reason"), stringField(choice, "finishReason"))))
	if reason == "" || reason == "stop" || reason == "tool_calls" || reason == "function_call" || reason == "completed" {
		return nil
	}
	if reason == "length" || reason == "max_tokens" || reason == "max_completion_tokens" || reason == "max_output_tokens" {
		return providerBillingReviewError{reason: "上游 Chat 响应达到输出上限，结果可能不完整且费用状态待核对；系统未使用半截结果且不会自动重试"}
	}
	return providerBillingReviewError{reason: "上游 Chat 响应以未完整状态结束，结果可能不完整且费用状态待核对；系统未使用半截结果且不会自动重试"}
}

func isEventStreamBody(raw []byte, mimeType string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))
	// 兼容网关可能把完整 JSON 反向误标为 SSE；正文前缀比响应头更可靠。
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}
	if strings.Contains(strings.ToLower(mimeType), "text/event-stream") {
		return true
	}
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "id:") || strings.HasPrefix(trimmed, "retry:") || strings.HasPrefix(trimmed, ":")
}

func parseSSEJSONEvents(raw []byte) ([]map[string]interface{}, error) {
	normalized := strings.TrimPrefix(string(raw), "\ufeff")
	normalized = strings.ReplaceAll(strings.ReplaceAll(normalized, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	events := make([]map[string]interface{}, 0, len(lines)/2)
	eventName := ""
	dataLines := make([]string, 0, 1)
	sawData := false
	terminal := false
	flush := func() error {
		if len(dataLines) == 0 {
			if providerStreamIncompleteEventName(eventName) {
				return errors.New("上游流式响应明确未完整结束：本次请求可能已经计费，系统未使用半截结果且不会自动重试")
			}
			// 兼容网关会用不带 data 的 `event: message_stop` 表示正常结束；
			// 终态事件本身就是协议元数据，不能把已完整返回的正文误判成截断。
			if providerStreamTerminalEventName(eventName) {
				terminal = true
			}
			eventName = ""
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "" {
			if providerStreamIncompleteEventName(eventName) {
				return errors.New("上游流式响应明确未完整结束：本次请求可能已经计费，系统未使用半截结果且不会自动重试")
			}
			if providerStreamTerminalEventName(eventName) {
				terminal = true
			}
			eventName = ""
			return nil
		}
		sawData = true
		if data == "[DONE]" {
			terminal = true
			eventName = ""
			return nil
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return fmt.Errorf("上游流式事件不是有效 JSON：%w", err)
		}
		if stringField(payload, "type") == "" && eventName != "" {
			payload["type"] = eventName
		}
		if message := providerStreamError(payload); message != "" {
			return fmt.Errorf("上游流式请求失败：%s", message)
		}
		if providerStreamTerminalEventName(eventName) || providerStreamTerminal(payload) {
			terminal = true
		}
		events = append(events, payload)
		eventName = ""
		return nil
	}
	for _, line := range lines {
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasValue := strings.Cut(line, ":")
		if hasValue {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventName = strings.TrimSpace(value)
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if !sawData || len(events) == 0 {
		return nil, errors.New("上游流式接口没有返回可解析事件")
	}
	if !terminal {
		return nil, errors.New("上游流式响应缺少完成标记，结果可能不完整且费用状态不确定")
	}
	return events, nil
}

func providerStreamTerminalEventName(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "done", "message_stop", "response.completed", "response.incomplete":
		return true
	default:
		return false
	}
}

func providerStreamIncompleteEventName(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "response.incomplete")
}

func providerStreamTerminal(event map[string]interface{}) bool {
	for _, item := range providerStreamPayloadVariants(event) {
		if done, _ := item["done"].(bool); done {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(stringField(item, "type"))) {
		case "done", "response.completed", "message_stop":
			return true
		}
		for _, key := range []string{"choices", "candidates"} {
			items, _ := item[key].([]interface{})
			for _, raw := range items {
				choice, _ := raw.(map[string]interface{})
				if stringField(choice, "finish_reason") != "" || stringField(choice, "finishReason") != "" {
					return true
				}
			}
		}
	}
	return false
}

func providerStreamPayloadVariants(payload map[string]interface{}) []map[string]interface{} {
	variants := []map[string]interface{}{payload}
	current := payload
	for depth := 0; depth < 2; depth++ {
		nested, ok := current["data"].(map[string]interface{})
		if !ok || nested == nil {
			break
		}
		variants = append(variants, nested)
		current = nested
	}
	return variants
}

func unwrapProviderStreamPayload(payload map[string]interface{}) map[string]interface{} {
	current := payload
	for depth := 0; depth < 2; depth++ {
		nested, ok := current["data"].(map[string]interface{})
		if !ok || nested == nil {
			break
		}
		current = nested
	}
	return current
}

func providerStreamError(event map[string]interface{}) string {
	for _, item := range providerStreamPayloadVariants(event) {
		if message := providerStreamErrorFields(item); message != "" {
			return message
		}
	}
	return ""
}

func providerStreamErrorFields(event map[string]interface{}) string {
	typeName := strings.ToLower(strings.TrimSpace(stringField(event, "type")))
	if errValue, ok := event["error"].(map[string]interface{}); ok {
		if message := firstNonEmpty(stringField(errValue, "message"), stringField(errValue, "detail")); message != "" {
			return message
		}
	}
	if errText, ok := event["error"].(string); ok && strings.TrimSpace(errText) != "" {
		return strings.TrimSpace(errText)
	}
	if feedback, ok := event["promptFeedback"].(map[string]interface{}); ok {
		if reason := strings.TrimSpace(stringField(feedback, "blockReason")); reason != "" {
			return "Gemini 已阻止本次文本请求（" + reason + "）"
		}
	}
	if typeName == "error" {
		return firstNonEmpty(stringField(event, "message"), "上游流式请求失败")
	}
	if typeName == "response.failed" {
		if response, ok := event["response"].(map[string]interface{}); ok {
			if errValue, ok := response["error"].(map[string]interface{}); ok {
				return firstNonEmpty(stringField(errValue, "message"), "上游响应生成失败")
			}
		}
		return "上游响应生成失败"
	}
	if typeName == "response.incomplete" || typeName == "response.cancelled" {
		return "上游流式响应明确未完整结束：本次请求可能已经计费，系统未使用半截结果且不会自动重试"
	}
	return ""
}

func streamContentText(value interface{}) string {
	switch item := value.(type) {
	case string:
		return item
	case []interface{}:
		var content strings.Builder
		for _, part := range item {
			content.WriteString(streamContentText(part))
		}
		return content.String()
	case map[string]interface{}:
		return firstNonEmpty(stringField(item, "text"), stringField(item, "content"))
	default:
		return ""
	}
}

// providerPayloadForAnalytics 从普通 JSON 或 SSE 完成事件中提取用量，避免流式成功后管理日志完全丢失 token 信息。
func providerPayloadForAnalytics(raw []byte) map[string]interface{} {
	if json.Valid(raw) {
		var payload map[string]interface{}
		if json.Unmarshal(raw, &payload) == nil {
			return payload
		}
		var items []map[string]interface{}
		if json.Unmarshal(raw, &items) == nil {
			for index := len(items) - 1; index >= 0; index-- {
				if _, ok := items[index]["usageMetadata"].(map[string]interface{}); ok {
					return items[index]
				}
			}
			if len(items) > 0 {
				return items[len(items)-1]
			}
		}
	}
	events, err := parseSSEJSONEvents(raw)
	if err != nil {
		return nil
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if response, ok := event["response"].(map[string]interface{}); ok {
			return response
		}
		if usage, ok := event["usage"].(map[string]interface{}); ok && usage != nil {
			return event
		}
	}
	return nil
}
