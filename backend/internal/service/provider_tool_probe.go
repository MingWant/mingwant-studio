package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	channelProbeToolName           = "probe_extract_record"
	channelProbeToolMaxOutputTokens = 512
)

type providerToolProbeCall struct {
	ID        string
	ItemID    string
	Name      string
	Arguments string
}

// 工具诊断沿用真实 Agent 的协议和 SSE 入口，但固定为一个无副作用函数；
// 结果由后台任务持久化，响应丢失时不会让浏览器直接重发一笔不确定请求。
func runChannelToolProbeTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	toolChoice := channelProbeToolChoice(input.Config.Model)
	var raw []byte
	var mimeType string
	var delivery providerStreamDelivery
	var err error
	switch input.Config.InterfaceType {
	case "chat-completion":
		messages := []map[string]interface{}{{"role": "user", "content": input.Prompt}}
		body := chatCompletionsTextRequestBody(input, messages, true)
		body["tools"] = []interface{}{channelProbeChatTool(input.Config.Model)}
		body["tool_choice"] = toolChoice
		raw, mimeType, delivery, err = postStreamingJSON(ctx, input.Config, "/chat/completions", body)
	case "openai-response":
		responseInput, inputErr := textResponseInput(input)
		if inputErr != nil {
			return nil, inputErr
		}
		body := responsesTextRequestBody(input, responseInput, true)
		body["tools"] = []interface{}{channelProbeResponsesTool()}
		body["tool_choice"] = "required"
		raw, mimeType, delivery, err = postStreamingJSON(ctx, input.Config, "/responses", body)
	case "gemini-content":
		body, bodyErr := geminiTextRequestBody(input)
		if bodyErr != nil {
			return nil, bodyErr
		}
		body["tools"] = []interface{}{map[string]interface{}{"functionDeclarations": []interface{}{channelProbeGeminiTool()}}}
		body["toolConfig"] = map[string]interface{}{"functionCallingConfig": map[string]interface{}{"mode": "ANY", "allowedFunctionNames": []string{channelProbeToolName}}}
		modelName := strings.TrimPrefix(strings.TrimSpace(input.Config.Model), "models/")
		if modelName == "" {
			return nil, errors.New("Gemini 工具诊断缺少模型名")
		}
		path := "/models/" + url.PathEscape(modelName) + ":streamGenerateContent?alt=sse"
		raw, mimeType, delivery, err = postStreamingGeminiJSON(ctx, input.Config, path, body)
	default:
		return nil, fmt.Errorf("当前协议不支持工具诊断：%s", input.Config.InterfaceType)
	}
	if err != nil {
		return nil, err
	}
	calls, err := providerToolCallsFromBody(raw, mimeType, input.Config.InterfaceType)
	if err != nil {
		return nil, err
	}
	call := firstProviderToolCall(calls, channelProbeToolName)
	if call == nil {
		return nil, errors.New("模型请求已完成，但没有返回 probe_extract_record 工具调用")
	}
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return nil, errors.New("模型工具参数不是有效 JSON")
	}
	parsed, err := normalizeChannelProbePayload(arguments)
	if err != nil {
		return nil, fmt.Errorf("工具参数校验失败：%w", err)
	}
	code := probeVerificationCode(input.Prompt)
	if parsed.Site != "蓝桥社区服务站" || parsed.Checked != 3 || parsed.Normal != 2 || parsed.Replace != 1 || parsed.VerificationCode != code {
		return nil, errors.New("工具参数与固定运维记录不一致")
	}
	transport := providerTextTransport(isEventStreamBody(raw, mimeType), delivery)
	return map[string]interface{}{
		"mode": "text",
		"toolProbe": ChannelProbeResult{
			OK: true, Transport: transport, DurationMs: delivery.FirstByteMs, FirstByteMs: delivery.FirstByteMs, DeliverySpanMs: delivery.DeliverySpanMs,
			LongestChunkWaitMs: delivery.LongestReadWaitMs, TotalChunkWaitMs: delivery.TotalFollowupWaitMs, StreamReadCount: delivery.ReadCount, Progressive: delivery.Progressive,
			ToolCalling: "supported", ToolName: call.Name,
			CheckedAt: nowUTC(), VerifierVersion: channelProbeVerifierVersion,
		},
		"streamDelivery": delivery,
		"transport":      transport,
	}, nil
}

func channelProbeToolChoice(model string) interface{} {
	value := strings.ToLower(strings.TrimSpace(model))
	value = strings.NewReplacer("/", "-", ":", "-", "_", "-").Replace(value)
	// 与浏览器在线 Agent 保持同一兼容边界：这些别名明确可能拒绝 required，
	// 但返回文本时仍会由本地结果校验判定工具诊断失败。
	if strings.Contains(value, "kimi-k3-ls") || strings.Contains(value, "kimi-k2.7-code") || strings.Contains(value, "kimi-k2-7-code") || strings.Contains(value, "kimi-k27-code") || strings.Contains(value, "kimi-k2.6") || strings.Contains(value, "kimi-k2-6") || strings.Contains(value, "kimi-k26") {
		return "auto"
	}
	return "required"
}

func channelProbeToolParameters() map[string]interface{} {
	// 不能只用五个扁平字段测活：真实创作台会一次提交数组、枚举、嵌套对象
	// 和 metadata。把代表性结构放进同一个无副作用函数，才能提前发现“简单
	// Function Calling 绿灯、完整画布工具 Schema 被网关拒绝”的兼容问题。
	operation := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type":            map[string]interface{}{"type": "string", "enum": []string{"add_node", "update_node", "delete_node", "delete_connections", "connect_nodes", "set_viewport", "select_nodes", "run_generation", "run_image_annotation"}},
			"id":              map[string]interface{}{"type": "string"},
			"ids":             map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"nodeType":        map[string]interface{}{"type": "string", "enum": []string{"image", "text", "script", "skill", "config", "video", "audio"}},
			"title":           map[string]interface{}{"type": "string"},
			"x":               map[string]interface{}{"type": "number"},
			"y":               map[string]interface{}{"type": "number"},
			"width":           map[string]interface{}{"type": "number"},
			"height":          map[string]interface{}{"type": "number"},
			"position":        map[string]interface{}{"type": "object", "properties": map[string]interface{}{"x": map[string]interface{}{"type": "number"}, "y": map[string]interface{}{"type": "number"}}, "required": []string{"x", "y"}, "additionalProperties": false},
			"metadata":        map[string]interface{}{"type": "object", "additionalProperties": true},
			"patch":           map[string]interface{}{"type": "object", "additionalProperties": true},
			"all":             map[string]interface{}{"type": "boolean"},
			"fromNodeId":      map[string]interface{}{"type": "string"},
			"toNodeId":        map[string]interface{}{"type": "string"},
			"viewport":        map[string]interface{}{"type": "object", "properties": map[string]interface{}{"x": map[string]interface{}{"type": "number"}, "y": map[string]interface{}{"type": "number"}, "k": map[string]interface{}{"type": "number"}}, "required": []string{"x", "y", "k"}, "additionalProperties": false},
			"nodeId":          map[string]interface{}{"type": "string"},
			"annotationNodeId": map[string]interface{}{"type": "string"},
			"mode":             map[string]interface{}{"type": "string", "enum": []string{"text", "image", "video", "audio"}},
			"prompt":           map[string]interface{}{"type": "string"},
		},
		"required":             []string{"type"},
		"additionalProperties": false,
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"site":             map[string]interface{}{"type": "string"},
			"checked":          map[string]interface{}{"type": "integer"},
			"normal":           map[string]interface{}{"type": "integer"},
			"replace":          map[string]interface{}{"type": "integer"},
			"verificationCode": map[string]interface{}{"type": "string"},
			"ops":              map[string]interface{}{"type": "array", "items": operation},
		},
		"required":             []string{"site", "checked", "normal", "replace", "verificationCode"},
		"additionalProperties": false,
	}
}

func channelProbeChatTool(model string) map[string]interface{} {
	function := map[string]interface{}{
		"name":        channelProbeToolName,
		"description": "把固定的运维记录整理成结构化字段；仅用于校验工具调用能力，不执行外部操作。",
		"parameters":  channelProbeToolParameters(),
	}
	// 与创作台的 Chat 工具序列化保持一致：Kimi/Moonshot 兼容层常把省略
	// strict 当作严格模式，而画布工具需要允许可选字段和 metadata 扩展。
	if channelProbeKimiChatModel(model) {
		function["strict"] = false
	}
	return map[string]interface{}{
		"type": "function",
		"function": function,
	}
}

func channelProbeKimiChatModel(model string) bool {
	value := strings.ToLower(strings.TrimSpace(model))
	value = strings.NewReplacer("/", "-", ":", "-", "_", "-").Replace(value)
	return strings.Contains(value, "kimi") || strings.Contains(value, "moonshot")
}

func channelProbeResponsesTool() map[string]interface{} {
	return map[string]interface{}{
		"type":        "function",
		"name":        channelProbeToolName,
		"description": "把固定的运维记录整理成结构化字段；仅用于校验工具调用能力，不执行外部操作。",
		"parameters":  channelProbeToolParameters(),
	}
}

func channelProbeGeminiTool() map[string]interface{} {
	parameters := channelProbeToolParameters()
	// Gemini Schema 不接受 OpenAI JSON Schema 的 additionalProperties；创作台
	// 中转也会在 Gemini 分支过滤该字段，探针必须与实际 Agent 请求保持一致。
	stripChannelProbeGeminiSchema(parameters)
	return map[string]interface{}{
		"name":        channelProbeToolName,
		"description": "把固定的运维记录整理成结构化字段；仅用于校验工具调用能力，不执行外部操作。",
		"parameters":  parameters,
	}
}

func stripChannelProbeGeminiSchema(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		delete(typed, "additionalProperties")
		if properties, ok := typed["properties"].(map[string]interface{}); ok {
			for _, property := range properties {
				stripChannelProbeGeminiSchema(property)
			}
		}
		if items, ok := typed["items"]; ok {
			stripChannelProbeGeminiSchema(items)
		}
	case []interface{}:
		for _, item := range typed {
			stripChannelProbeGeminiSchema(item)
		}
	}
}

func probeVerificationCode(prompt string) string {
	marker := "verificationCode 必须原样填写为 "
	index := strings.Index(prompt, marker)
	if index < 0 {
		return ""
	}
	value := strings.TrimSpace(prompt[index+len(marker):])
	if newline := strings.IndexAny(value, "\r\n"); newline >= 0 {
		value = value[:newline]
	}
	return strings.Trim(strings.TrimSpace(value), "`。．")
}

func providerToolCallsFromBody(raw []byte, mimeType string, protocol string) ([]providerToolProbeCall, error) {
	payloads, err := providerJSONPayloads(raw, mimeType)
	if err != nil {
		return nil, err
	}
	calls := make(map[string]providerToolProbeCall)
	for _, payload := range payloads {
		switch protocol {
		case "chat-completion":
			collectChatProviderToolCalls(payload, calls)
		case "openai-response":
			collectResponseProviderToolCalls(payload, calls)
		case "gemini-content":
			collectGeminiProviderToolCalls(payload, calls)
		}
	}
	result := make([]providerToolProbeCall, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.Name) != "" && strings.TrimSpace(call.Arguments) != "" {
			result = append(result, call)
		}
	}
	return result, nil
}

func providerJSONPayloads(raw []byte, mimeType string) ([]map[string]interface{}, error) {
	if isEventStreamBody(raw, mimeType) {
		return parseSSEJSONEvents(raw)
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("工具诊断响应无法解析：%w", err)
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		return []map[string]interface{}{typed}, nil
	case []interface{}:
		payloads := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			payload, ok := item.(map[string]interface{})
			if !ok {
				return nil, errors.New("工具诊断响应数组包含无效事件")
			}
			payloads = append(payloads, payload)
		}
		return payloads, nil
	default:
		return nil, errors.New("工具诊断响应不是 JSON 对象")
	}
}

func collectChatProviderToolCalls(payload map[string]interface{}, calls map[string]providerToolProbeCall) {
	for _, item := range providerStreamPayloadVariants(payload) {
		choices, _ := item["choices"].([]interface{})
		for choiceIndex, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				collectChatProviderToolCallValue(delta["tool_calls"], choiceIndex, calls, false)
				collectChatProviderToolCallValue(delta["toolCalls"], choiceIndex, calls, false)
				collectChatProviderToolCallValue(delta["function_call"], choiceIndex, calls, false)
				collectChatProviderToolCallValue(delta["functionCall"], choiceIndex, calls, false)
			}
			if message, ok := choice["message"].(map[string]interface{}); ok {
				collectChatProviderToolCallValue(message["tool_calls"], choiceIndex, calls, true)
				collectChatProviderToolCallValue(message["toolCalls"], choiceIndex, calls, true)
				collectChatProviderToolCallValue(message["function_call"], choiceIndex, calls, true)
				collectChatProviderToolCallValue(message["functionCall"], choiceIndex, calls, true)
			}
		}
	}
}

func collectChatProviderToolCallValue(value interface{}, fallbackIndex int, calls map[string]providerToolProbeCall, replace bool) {
	items, ok := value.([]interface{})
	if !ok {
		if value == nil {
			return
		}
		items = []interface{}{value}
	}
	for itemIndex, rawItem := range items {
		item, _ := rawItem.(map[string]interface{})
		if item == nil {
			continue
		}
		index := fallbackIndex + itemIndex
		if number, ok := item["index"].(float64); ok {
			index = int(number)
		}
		key := fmt.Sprintf("chat:%d", index)
		current := calls[key]
		function, _ := item["function"].(map[string]interface{})
		name := firstNonEmptyString(stringField(function, "name"), stringField(item, "name"))
		arguments := toolArgumentText(function["arguments"])
		if arguments == "" {
			arguments = toolArgumentText(item["arguments"])
		}
		arguments = mergeToolArguments(current.Arguments, arguments, replace)
		calls[key] = providerToolProbeCall{ID: firstNonEmptyString(stringField(item, "id"), stringField(item, "call_id"), current.ID), Name: firstNonEmptyString(name, current.Name), Arguments: arguments}
	}
}

func collectResponseProviderToolCalls(payload map[string]interface{}, calls map[string]providerToolProbeCall) {
	for _, item := range providerStreamPayloadVariants(payload) {
		if output, ok := item["output"].([]interface{}); ok {
			for _, rawOutput := range output {
				if outputItem, ok := rawOutput.(map[string]interface{}); ok {
					collectResponseProviderToolCall(outputItem, calls)
				}
			}
		}
		if eventItem, ok := item["item"].(map[string]interface{}); ok {
			collectResponseProviderToolCall(eventItem, calls)
		}
		typeName := strings.ToLower(strings.TrimSpace(stringField(item, "type")))
		if strings.Contains(typeName, "function_call_arguments") {
			itemID := firstNonEmptyString(stringField(item, "item_id"), stringField(item, "itemId"), stringField(item, "id"))
			callID := firstNonEmptyString(stringField(item, "call_id"), stringField(item, "callId"))
			if itemID != "" || callID != "" {
				key, current := responseProviderToolCallForIDs(calls, itemID, callID)
				if key == "" {
					key = "response:" + firstNonEmptyString(itemID, callID)
				}
				arguments := toolArgumentText(item["delta"])
				if arguments == "" {
					arguments = toolArgumentText(item["arguments"])
				}
				if strings.HasSuffix(typeName, ".done") && len(arguments) < len(current.Arguments) {
					arguments = current.Arguments
				}
				calls[key] = providerToolProbeCall{ID: firstNonEmptyString(callID, current.ID, itemID), ItemID: firstNonEmptyString(itemID, current.ItemID), Name: firstNonEmptyString(stringField(item, "name"), current.Name), Arguments: mergeToolArguments(current.Arguments, arguments, strings.HasSuffix(typeName, ".done"))}
			}
		}
	}
}

func collectResponseProviderToolCall(item map[string]interface{}, calls map[string]providerToolProbeCall) {
	if strings.ToLower(strings.TrimSpace(stringField(item, "type"))) != "function_call" {
		return
	}
	itemID := firstNonEmptyString(stringField(item, "id"), stringField(item, "item_id"), stringField(item, "itemId"))
	callID := firstNonEmptyString(stringField(item, "call_id"), stringField(item, "callId"))
	if itemID == "" && callID == "" {
		return
	}
	key, current := responseProviderToolCallForIDs(calls, itemID, callID)
	if key == "" {
		key = "response:" + firstNonEmptyString(itemID, callID)
	}
	arguments := toolArgumentText(item["arguments"])
	calls[key] = providerToolProbeCall{ID: firstNonEmptyString(callID, current.ID, itemID), ItemID: firstNonEmptyString(itemID, current.ItemID), Name: firstNonEmptyString(stringField(item, "name"), current.Name), Arguments: mergeToolArguments(current.Arguments, arguments, true)}
}

// Responses 的 function_call 首事件通常同时有 id 与 call_id，后续 delta 只带
// item_id。以 item id 为主键、同时按两个标识查找，避免把参数分片拆成第二个无名调用。
func responseProviderToolCallForIDs(calls map[string]providerToolProbeCall, itemID string, callID string) (string, providerToolProbeCall) {
	for key, call := range calls {
		if (itemID != "" && (call.ItemID == itemID || key == "response:"+itemID)) || (callID != "" && call.ID == callID) || key == "response:"+callID {
			return key, call
		}
	}
	return "", providerToolProbeCall{}
}

func collectGeminiProviderToolCalls(payload map[string]interface{}, calls map[string]providerToolProbeCall) {
	for _, item := range providerStreamPayloadVariants(payload) {
		candidates, _ := item["candidates"].([]interface{})
		for _, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]interface{})
			content, _ := candidate["content"].(map[string]interface{})
			parts, _ := content["parts"].([]interface{})
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]interface{})
				call, _ := part["functionCall"].(map[string]interface{})
				if call == nil {
					call, _ = part["function_call"].(map[string]interface{})
				}
				if call == nil {
					continue
				}
				name := stringField(call, "name")
				if name == "" {
					continue
				}
				key := firstNonEmptyString(stringField(call, "id"), name)
				current := calls["gemini:"+key]
				arguments := toolArgumentText(call["args"])
				if arguments == "" {
					arguments = toolArgumentText(call["arguments"])
				}
				calls["gemini:"+key] = providerToolProbeCall{ID: key, Name: name, Arguments: mergeToolArguments(current.Arguments, arguments, true)}
			}
		}
	}
}

func mergeToolArguments(current string, incoming string, replace bool) string {
	if incoming == "" {
		return current
	}
	// Responses 的 output_item.added 有些网关会先放一个空对象作为占位，后续
	// arguments.delta 才开始发送正文；占位不能参与字符串拼接，否则参数会变成 `{}{...}`。
	if current == "{}" && incoming != "{}" {
		return incoming
	}
	previous := current
	if previous == "{}" {
		previous = ""
	}
	if replace {
		return longerToolArguments(previous, incoming)
	}
	if previous == "" || incoming == previous || strings.HasPrefix(incoming, previous) {
		return incoming
	}
	if strings.HasPrefix(previous, incoming) {
		return previous
	}
	return previous + incoming
}

func longerToolArguments(current string, incoming string) string {
	if len(incoming) >= len(current) {
		return incoming
	}
	return current
}

func toolArgumentText(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstProviderToolCall(calls []providerToolProbeCall, name string) *providerToolProbeCall {
	for index := range calls {
		if calls[index].Name == name {
			return &calls[index]
		}
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
