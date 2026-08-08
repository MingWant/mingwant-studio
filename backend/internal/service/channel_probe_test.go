package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestValidateChannelProbeResponseAcceptsWrappedJSON(t *testing.T) {
	raw := "结果如下：\n```json\n{\"site\":\"蓝桥社区服务站\",\"checked\":\"3\",\"normal\":2,\"replace\":1,\"verificationCode\":\"probe123\"}\n```"
	if err := validateChannelProbeResponse(raw, "probe123"); err != nil {
		t.Fatalf("validateChannelProbeResponse() error = %v", err)
	}
}

func TestValidateChannelProbeResponseRejectsWrongCode(t *testing.T) {
	raw := `{"site":"蓝桥社区服务站","checked":3,"normal":2,"replace":1,"verificationCode":"other"}`
	if err := validateChannelProbeResponse(raw, "probe123"); err == nil {
		t.Fatal("validateChannelProbeResponse() error = nil")
	}
}

func TestValidateChannelProbeResponseRejectsPlainText(t *testing.T) {
	if err := validateChannelProbeResponse("三台灯中两台正常。", "probe123"); err == nil {
		t.Fatal("validateChannelProbeResponse() error = nil")
	}
}

func TestChannelProbeCapsResponsesAndChatOutput(t *testing.T) {
	input := canvasGenerationInput{Config: providerConfig{Model: "slow-model"}, MaxOutputTokens: channelProbeMaxOutputTokens}
	responsesBody := responsesTextRequestBody(input, "probe", true)
	if responsesBody["max_output_tokens"] != channelProbeMaxOutputTokens || responsesBody["stream"] != true {
		t.Fatalf("responses probe body = %#v", responsesBody)
	}
	chatBody := chatCompletionsTextRequestBody(input, []map[string]interface{}{{"role": "user", "content": "probe"}}, true)
	if chatBody["max_completion_tokens"] != channelProbeMaxOutputTokens || chatBody["stream"] != true {
		t.Fatalf("chat probe body = %#v", chatBody)
	}
	if _, exists := chatBody["max_output_tokens"]; exists {
		t.Fatalf("chat probe body used Responses field: %#v", chatBody)
	}
	if _, exists := chatBody["max_tokens"]; exists {
		t.Fatalf("chat probe body used deprecated field: %#v", chatBody)
	}
	kimiBody := chatCompletionsTextRequestBody(canvasGenerationInput{Config: providerConfig{Model: "kimi-k3-ls"}, MaxOutputTokens: channelProbeMaxOutputTokens}, nil, true)
	if kimiBody["reasoning_effort"] != "low" {
		t.Fatalf("Kimi K3 probe reasoning effort = %#v", kimiBody)
	}
	kimiStoryboardConfig := storyboardJSONProviderConfig(providerConfig{Model: "kimi-k3-ls", RequireStreaming: true})
	kimiStoryboardBody := chatCompletionsTextRequestBody(canvasGenerationInput{Config: kimiStoryboardConfig}, nil, true)
	if kimiStoryboardBody["reasoning_effort"] != "low" {
		t.Fatalf("Kimi K3 storyboard reasoning effort = %#v", kimiStoryboardBody)
	}
	if _, exists := kimiStoryboardBody["response_format"]; exists {
		t.Fatalf("Kimi K3-LS compatibility alias unexpectedly used JSON Mode: %#v", kimiStoryboardBody)
	}
	officialKimiStoryboardBody := chatCompletionsTextRequestBody(canvasGenerationInput{Config: storyboardJSONProviderConfig(providerConfig{Model: "kimi-k3"})}, nil, true)
	responseFormat, _ := officialKimiStoryboardBody["response_format"].(map[string]string)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("official Kimi storyboard JSON mode = %#v", officialKimiStoryboardBody)
	}
	if _, exists := kimiStoryboardBody["max_completion_tokens"]; exists {
		t.Fatalf("Kimi K3 storyboard unexpectedly truncated long structured output: %#v", kimiStoryboardBody)
	}
	otherBody := chatCompletionsTextRequestBody(canvasGenerationInput{Config: providerConfig{Model: "other-model"}, MaxOutputTokens: channelProbeMaxOutputTokens}, nil, true)
	if _, exists := otherBody["reasoning_effort"]; exists {
		t.Fatalf("generic probe added vendor reasoning option: %#v", otherBody)
	}
	otherStoryboardBody := chatCompletionsTextRequestBody(canvasGenerationInput{Config: storyboardJSONProviderConfig(providerConfig{Model: "other-model"})}, nil, true)
	if _, exists := otherStoryboardBody["response_format"]; exists {
		t.Fatalf("generic storyboard added Kimi JSON mode: %#v", otherStoryboardBody)
	}
	geminiBody, err := geminiTextRequestBody(canvasGenerationInput{Prompt: "probe", Config: providerConfig{Model: "gemini-test"}, MaxOutputTokens: channelProbeMaxOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	generationConfig, _ := geminiBody["generationConfig"].(map[string]interface{})
	if generationConfig["maxOutputTokens"] != channelProbeMaxOutputTokens {
		t.Fatalf("Gemini probe generation config = %#v", generationConfig)
	}
	storyboardBody, err := geminiTextRequestBody(canvasGenerationInput{
		Prompt: "storyboard",
		Config: storyboardJSONProviderConfig(providerConfig{Model: "gemini-test"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	storyboardGenerationConfig, _ := storyboardBody["generationConfig"].(map[string]interface{})
	if storyboardGenerationConfig["responseMimeType"] != "application/json" {
		t.Fatalf("Gemini storyboard generation config = %#v", storyboardGenerationConfig)
	}
}

func TestProviderToolProbeMergesResponsesItemAndArgumentEvents(t *testing.T) {
	raw := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"probe_extract_record\",\"arguments\":\"{}\"}}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"site\\\":\\\"蓝桥社区服务站\\\",\\\"checked\\\":3,\\\"normal\\\":2,\\\"replace\\\":1,\\\"verificationCode\\\":\\\"probe123\\\"}\"}\n\n" +
		"event: response.function_call_arguments.done\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":\"{\\\"site\\\":\\\"蓝桥社区服务站\\\",\\\"checked\\\":3,\\\"normal\\\":2,\\\"replace\\\":1,\\\"verificationCode\\\":\\\"probe123\\\"}\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	calls, err := providerToolCallsFromBody([]byte(raw), "text/event-stream", "openai-response")
	if err != nil {
		t.Fatal(err)
	}
	call := firstProviderToolCall(calls, channelProbeToolName)
	if call == nil || call.ID != "call_1" || call.Arguments == "{}" {
		t.Fatalf("merged Responses tool call = %#v", call)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &payload); err != nil {
		t.Fatalf("merged arguments are not JSON: %v", err)
	}
	if parsed, err := normalizeChannelProbePayload(payload); err != nil || parsed.VerificationCode != "probe123" || parsed.Checked != 3 {
		t.Fatalf("merged payload = %#v, err = %v", parsed, err)
	}
}

func TestChannelProbeChatToolMatchesKimiStrictCompatibility(t *testing.T) {
	kimi := channelProbeChatTool("kimi-k3-ls")["function"].(map[string]interface{})
	if strict, ok := kimi["strict"].(bool); !ok || strict {
		t.Fatalf("Kimi tool strict = %#v", kimi["strict"])
	}
	other := channelProbeChatTool("gpt-4o")["function"].(map[string]interface{})
	if _, ok := other["strict"]; ok {
		t.Fatalf("generic Chat tool unexpectedly contains strict: %#v", other)
	}
}

func TestCanvasTaskInputCannotOverrideInternalOutputLimit(t *testing.T) {
	var input canvasGenerationInput
	if err := json.Unmarshal([]byte(`{"mode":"text","maxOutputTokens":999999}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.MaxOutputTokens != 0 {
		t.Fatalf("user supplied internal output limit = %d", input.MaxOutputTokens)
	}
}

func TestChannelProbeRequestKeyScopesSystemGloballyAndCustomByConfig(t *testing.T) {
	system := providerConfig{ChannelID: "channel-1", BaseURL: "https://old.example.com/v1", APIKey: "old-secret", InterfaceType: "openai-chat", Model: "slow-model"}
	systemKey := channelProbeRequestKey(system)
	changedSystemConfig := system
	changedSystemConfig.BaseURL = "https://new.example.com/v1"
	changedSystemConfig.APIKey = "new-secret"
	changedSystemConfig.InterfaceType = "openai-response"
	if next := channelProbeRequestKey(changedSystemConfig); next != systemKey {
		t.Fatalf("system probe key changed with mutable config: %q != %q", next, systemKey)
	}
	if !strings.HasPrefix(systemKey, systemChannelProbeRequestKeyPrefix) || len(systemKey) != 64 {
		t.Fatalf("system probe key = %q", systemKey)
	}
	otherModel := system
	otherModel.Model = "other-model"
	if channelProbeRequestKey(otherModel) == systemKey {
		t.Fatal("different system model reused probe key")
	}

	custom := providerConfig{BaseURL: "https://custom.example.com/v1", APIKey: "custom-secret", APIFormat: "openai", InterfaceType: "openai-chat", Model: "slow-model"}
	customKey := channelProbeRequestKey(custom)
	if !strings.HasPrefix(customKey, userChannelProbeRequestKeyPrefix) || len(customKey) != 64 || strings.Contains(customKey, custom.APIKey) {
		t.Fatalf("custom probe key leaked config or has wrong shape: %q", customKey)
	}
	changedCustom := custom
	changedCustom.APIKey = "rotated-secret"
	if channelProbeRequestKey(changedCustom) == customKey {
		t.Fatal("changed custom credential reused probe key")
	}
}

func TestChannelProbeActiveTaskMustMatchCurrentConfig(t *testing.T) {
	config := providerConfig{ChannelID: "channel-1", BaseURL: "https://gateway.example.com/v1", APIKey: "secret", APIFormat: "openai", InterfaceType: "openai-chat", Model: "slow-model"}
	raw, err := json.Marshal(channelProbeTaskInput{Kind: "text", Mode: "text", ConfigHash: channelProbeConfigHash(config), Config: config})
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{InputJSON: string(raw)}
	if !channelProbeTaskUsesConfig(task, config) {
		t.Fatal("active probe with the same configuration was not reusable")
	}
	changed := config
	changed.BaseURL = "https://new-gateway.example.com/v1"
	if channelProbeTaskUsesConfig(task, changed) {
		t.Fatal("active probe from an old configuration was reused")
	}
	if channelProbeTaskUsesConfig(model.Task{InputJSON: "{}"}, config) {
		t.Fatal("probe without a configuration hash was reused")
	}
}

func TestNormalizeChannelProbeSubmissionKey(t *testing.T) {
	if key, err := normalizeChannelProbeSubmissionKey(" probe_submit_123 "); err != nil || key != "probe_submit_123" {
		t.Fatalf("normalizeChannelProbeSubmissionKey() = %q, %v", key, err)
	}
	for _, value := range []string{"", "short", "contains space", strings.Repeat("a", 65)} {
		if _, err := normalizeChannelProbeSubmissionKey(value); err == nil {
			t.Fatalf("normalizeChannelProbeSubmissionKey(%q) error = nil", value)
		}
	}
}

func TestSystemChannelProbeOutputRedactsResponsePreview(t *testing.T) {
	input, _ := json.Marshal(channelProbeTaskInput{Mode: "text", Config: providerConfig{ChannelID: "channel-1", Model: "slow-model"}})
	result, _ := json.Marshal(map[string]any{
		"mode": "text",
		"probe": ChannelProbeResult{
			OK: true, Transport: "stream", DurationMs: 1234, ResponsePreview: `{"verificationCode":"secret-preview"}`,
			CheckedAt: time.Now(), VerifierVersion: channelProbeVerifierVersion,
		},
	})
	task := model.Task{
		ID: "system-probe", Type: channelProbeTaskType, Status: model.TaskStatusSucceeded,
		RequestKey: systemChannelProbeRequestKeyPrefix + "key", InputJSON: string(input), ResultJSON: string(result),
		Error: "上游已响应，但测活校验未通过；响应摘要：secret-preview",
	}
	status := channelProbeStatusFromTask(task)
	if status.Result == nil || status.Result.ResponsePreview != "" || strings.Contains(status.Error, "secret-preview") {
		t.Fatalf("system probe status leaked preview: %#v", status)
	}
	publicTask := taskForOutput(task)
	if strings.Contains(publicTask.ResultJSON, "secret-preview") || strings.Contains(publicTask.Error, "secret-preview") {
		t.Fatalf("system task leaked preview: %#v", publicTask)
	}

	customInput, _ := json.Marshal(channelProbeTaskInput{Mode: "text", Config: providerConfig{BaseURL: "https://custom.example.com", Model: "slow-model"}})
	customTask := task
	customTask.RequestKey = userChannelProbeRequestKeyPrefix + "key"
	customTask.InputJSON = string(customInput)
	customStatus := channelProbeStatusFromTask(customTask)
	if customStatus.Result == nil || !strings.Contains(customStatus.Result.ResponsePreview, "secret-preview") {
		t.Fatalf("custom probe preview unexpectedly removed: %#v", customStatus)
	}
}

func TestChannelProbeAllowsAdminToReadSharedSystemProbeOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	systemConfig := providerConfig{ChannelID: "channel-1", Model: "slow-model"}
	systemInput, _ := json.Marshal(channelProbeTaskInput{Mode: "text", Config: systemConfig})
	systemTask := model.Task{
		ID: "system-probe", UserID: "admin-1", Type: channelProbeTaskType, Status: model.TaskStatusQueued,
		RequestKey: channelProbeRequestKey(systemConfig), InputJSON: string(systemInput), Model: systemConfig.Model,
	}
	customConfig := providerConfig{BaseURL: "https://custom.example.com/v1", APIKey: "secret", InterfaceType: "openai-chat", Model: "slow-model"}
	customInput, _ := json.Marshal(channelProbeTaskInput{Mode: "text", Config: customConfig})
	customTask := model.Task{
		ID: "custom-probe", UserID: "admin-1", Type: channelProbeTaskType, Status: model.TaskStatusQueued,
		RequestKey: channelProbeRequestKey(customConfig), InputJSON: string(customInput), Model: customConfig.Model,
	}
	if err := db.Create(&systemTask).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&customTask).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db)}
	otherAdmin := &model.User{ID: "admin-2", Role: model.UserRoleAdmin}
	if probe, err := svc.ChannelProbe(otherAdmin, systemTask.ID); err != nil || probe.ID != systemTask.ID {
		t.Fatalf("shared system probe = %#v, %v", probe, err)
	}
	if _, err := svc.ChannelProbe(otherAdmin, customTask.ID); err == nil {
		t.Fatal("admin read another user's custom probe")
	}
	if _, err := svc.ChannelProbe(&model.User{ID: "user-2", Role: model.UserRoleUser}, systemTask.ID); err == nil {
		t.Fatal("regular user read another user's system probe")
	}
}

func TestLongStoryboardAllowsCallsWithoutRecentProbe(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ModelChannel{}, &model.ChannelModel{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	channel := model.ModelChannel{
		ID: "channel-1", Scope: model.ChannelScopeSystem, Enabled: true, BaseURL: "https://example.com/v1",
		APIKey: "secret", APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion,
	}
	item := model.ChannelModel{
		ID: "model-1", ChannelID: channel.ID, ModelKey: "slow-model", Capability: "text",
		Protocol: model.ChannelInterfaceChatCompletion, Enabled: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	svc := &Service{repo: repository.New(db)}
	if protocol, err := svc.SystemTextModelProtocol(channel.ID, item.ModelKey); err != nil || protocol != item.Protocol {
		t.Fatalf("SystemTextModelProtocol() = %q, %v", protocol, err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", map[string]any{}); err == nil || !strings.Contains(err.Error(), "缺少文本模型配置") {
		t.Fatalf("missing config error = %v", err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard_rows", map[string]any{"config": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "缺少文本模型名称") {
		t.Fatalf("missing model error = %v", err)
	}
	input := map[string]any{"config": map[string]any{"channelId": channel.ID, "model": item.ModelKey}}
	if err := validateLongTextTaskConfig("agent_storyboard", input); err != nil {
		t.Fatalf("unverified readiness should remain callable: %v", err)
	}
	proxyInput := map[string]any{"config": map[string]any{"baseUrl": "/api/ai/system/" + channel.ID, "model": item.ModelKey}}
	if err := validateLongTextTaskConfig("agent_storyboard_rows", proxyInput); err != nil {
		t.Fatalf("unverified proxy readiness should remain callable: %v", err)
	}
	customInput := map[string]any{"config": map[string]any{"baseUrl": "https://custom.example.com/v1", "model": item.ModelKey}}
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("custom channel without proof should remain callable: %v", err)
	}

	checkedAt := time.Now()
	hash := channelProbeConfigHash(providerConfig{
		ChannelID: channel.ID, BaseURL: channel.BaseURL, APIKey: channel.APIKey, APIFormat: channel.APIFormat,
		InterfaceType: string(item.Protocol), Model: item.ModelKey,
	})
	if err := db.Model(&model.ChannelModel{}).Where("id = ?", item.ID).Updates(map[string]any{
		"probe_status": "succeeded", "probe_transport": "stream", "probe_checked_at": checkedAt, "probe_config_hash": hash,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard_rows", input); err != nil {
		t.Fatalf("recent streaming readiness error = %v", err)
	}

	if err := db.Model(&model.ChannelModel{}).Where("id = ?", item.ID).Update("probe_checked_at", checkedAt.Add(-channelProbeMaxAge-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", input); err != nil {
		t.Fatalf("stale readiness should remain callable: %v", err)
	}
	if err := db.Model(&model.ChannelModel{}).Where("id = ?", item.ID).Updates(map[string]any{
		"probe_checked_at": checkedAt, "probe_transport": "stream-unverified",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", input); err != nil {
		t.Fatalf("unverified progressive readiness should remain callable: %v", err)
	}
	if err := db.Model(&model.ChannelModel{}).Where("id = ?", item.ID).Updates(map[string]any{
		"probe_checked_at": checkedAt, "probe_transport": "non-stream-compatible",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", input); err != nil {
		t.Fatalf("non-stream readiness should remain callable: %v", err)
	}

	customConfig := providerConfig{
		BaseURL: "https://custom.example.com/v1", APIKey: "custom-secret", APIFormat: "openai",
		InterfaceType: string(model.ChannelInterfaceChatCompletion), Model: item.ModelKey,
	}
	probeInputJSON, _ := json.Marshal(channelProbeTaskInput{Kind: "text", Mode: "text", Config: customConfig})
	probeResultJSON, _ := json.Marshal(map[string]any{"probe": ChannelProbeResult{OK: true, Transport: "stream", DeliverySpanMs: 100, LongestChunkWaitMs: 90, TotalChunkWaitMs: 90, StreamReadCount: 2, Progressive: true, CheckedAt: checkedAt, VerifierVersion: channelProbeVerifierVersion}})
	probeTask := model.Task{
		ID: "probe-1", UserID: "user-1", Type: channelProbeTaskType, Status: model.TaskStatusSucceeded,
		InputJSON: string(probeInputJSON), ResultJSON: string(probeResultJSON), CreatedAt: checkedAt, UpdatedAt: checkedAt,
	}
	if err := db.Create(&probeTask).Error; err != nil {
		t.Fatal(err)
	}
	toolProbeInputJSON, _ := json.Marshal(channelProbeTaskInput{Kind: "tool", Mode: "text", Config: customConfig})
	toolProbeResultJSON, _ := json.Marshal(map[string]any{"toolProbe": ChannelProbeResult{OK: true, ToolCalling: "supported", ToolName: channelProbeToolName, CheckedAt: checkedAt, VerifierVersion: channelProbeVerifierVersion}})
	toolProbeTask := model.Task{
		ID: "probe-tool-1", UserID: "user-1", Type: channelProbeTaskType, Status: model.TaskStatusSucceeded,
		InputJSON: string(toolProbeInputJSON), ResultJSON: string(toolProbeResultJSON), CreatedAt: checkedAt, UpdatedAt: checkedAt,
	}
	if err := db.Create(&toolProbeTask).Error; err != nil {
		t.Fatal(err)
	}
	customInput = map[string]any{
		"channelProbeTaskId": probeTask.ID,
		"toolProbeTaskId": toolProbeTask.ID,
		"config": map[string]any{
			"baseUrl": customConfig.BaseURL, "apiKey": customConfig.APIKey, "apiFormat": customConfig.APIFormat,
			"interfaceType": customConfig.InterfaceType, "model": customConfig.Model,
		},
	}
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("custom streaming proof error = %v", err)
	}
	missingObservationJSON, _ := json.Marshal(map[string]any{"probe": ChannelProbeResult{OK: true, Transport: "stream", CheckedAt: checkedAt, VerifierVersion: channelProbeVerifierVersion}})
	if err := db.Model(&model.Task{}).Where("id = ?", probeTask.ID).Update("result_json", string(missingObservationJSON)).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("missing progressive observation should remain callable: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", probeTask.ID).Update("result_json", string(probeResultJSON)).Error; err != nil {
		t.Fatal(err)
	}
	customInput["config"].(map[string]any)["apiKey"] = "changed-secret"
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("changed custom config should remain callable: %v", err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("cross-user custom proof should remain callable: %v", err)
	}
	customInput["config"].(map[string]any)["apiKey"] = customConfig.APIKey
	oldResultJSON, _ := json.Marshal(map[string]any{"probe": ChannelProbeResult{OK: true, Transport: "stream", CheckedAt: checkedAt}})
	if err := db.Model(&model.Task{}).Where("id = ?", probeTask.ID).Update("result_json", string(oldResultJSON)).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateLongTextTaskConfig("agent_storyboard", customInput); err != nil {
		t.Fatalf("legacy custom proof should remain callable: %v", err)
	}
}

func TestSystemTextProtocolDoesNotInheritVideoChannelDefault(t *testing.T) {
	channel := model.ModelChannel{
		ID: "mixed-channel", Scope: model.ChannelScopeSystem, APIFormat: "openai",
		InterfaceType: model.ChannelInterfaceNewAPIVideo,
	}
	item := model.ChannelModel{
		ChannelID: channel.ID, ModelKey: "text-model", Capability: "text",
		Protocol: model.ChannelInterfaceNewAPIVideo,
	}
	protocol, err := resolveSystemTextModelProtocol(channel, item)
	if err != nil || protocol != model.ChannelInterfaceChatCompletion {
		t.Fatalf("resolveSystemTextModelProtocol() = %q, %v", protocol, err)
	}
	item.Protocol = ""
	protocol, err = resolveSystemTextModelProtocol(channel, item)
	if err != nil || protocol != model.ChannelInterfaceChatCompletion {
		t.Fatalf("resolveSystemTextModelProtocol() without model protocol = %q, %v", protocol, err)
	}
}
