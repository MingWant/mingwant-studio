package handler

import (
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestAuthorizeSystemProxyAllowsConfiguredTextModel(t *testing.T) {
	channel := &model.ModelChannel{APIFormat: "openai", InterfaceType: model.ChannelInterfaceOpenAIResponse, ModelsJSON: `["gpt-4.1"]`}
	body := []byte(`{"model":"gpt-4.1","input":"test"}`)
	if err := authorizeSystemProxy(channel, http.MethodPost, "/responses", "application/json", body); err != nil {
		t.Fatalf("authorizeSystemProxy() error = %v", err)
	}
}

func TestAuthorizeSystemProxyAllowsConfiguredGeminiTextModel(t *testing.T) {
	channel := &model.ModelChannel{APIFormat: "gemini", InterfaceType: model.ChannelInterfaceGeminiContent, ModelsJSON: `["gemini-2.5-pro"]`}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"test"}]}]}`)
	path := "/models/gemini-2.5-pro:streamGenerateContent"
	if err := authorizeSystemProxy(channel, http.MethodPost, path, "application/json", body); err != nil {
		t.Fatalf("authorizeSystemProxy() error = %v", err)
	}
	if got := proxyRequestModelFromPath(path); got != "gemini-2.5-pro" {
		t.Fatalf("proxyRequestModelFromPath() = %q", got)
	}
}

func TestSystemProxyUpstreamBaseAddsProtocolVersion(t *testing.T) {
	tests := []struct {
		channel model.ModelChannel
		want    string
	}{
		{channel: model.ModelChannel{BaseURL: "https://api.openai.com", APIFormat: "openai"}, want: "https://api.openai.com/v1"},
		{channel: model.ModelChannel{BaseURL: "https://api.openai.com/v1", APIFormat: "openai"}, want: "https://api.openai.com/v1"},
		{channel: model.ModelChannel{BaseURL: "https://generativelanguage.googleapis.com", APIFormat: "gemini"}, want: "https://generativelanguage.googleapis.com/v1beta"},
		{channel: model.ModelChannel{BaseURL: "https://generativelanguage.googleapis.com/v1", APIFormat: "gemini"}, want: "https://generativelanguage.googleapis.com/v1"},
	}
	for _, test := range tests {
		if got := systemProxyUpstreamBase(&test.channel, test.channel.APIFormat); got != test.want {
			t.Fatalf("systemProxyUpstreamBase(%#v) = %q, want %q", test.channel, got, test.want)
		}
	}
}

func TestAuthorizeCustomRelayAllowsModelsAndTextAgentEndpoints(t *testing.T) {
	tests := []struct {
		method      string
		target      string
		apiFormat   string
		contentType string
	}{
		{method: http.MethodGet, target: "https://api.example.com/v1/models", apiFormat: "openai"},
		{method: http.MethodPost, target: "https://api.example.com/v1/responses", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodPost, target: "https://api.example.com/v1/chat/completions", apiFormat: "openai", contentType: "application/json; charset=utf-8"},
		{method: http.MethodPost, target: "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", apiFormat: "gemini", contentType: "application/json"},
	}
	for _, test := range tests {
		target, err := url.Parse(test.target)
		if err != nil {
			t.Fatal(err)
		}
		if err := authorizeCustomRelay(test.method, target, test.apiFormat, test.contentType); err != nil {
			t.Fatalf("authorizeCustomRelay(%s %s) error = %v", test.method, test.target, err)
		}
	}
}

func TestAuthorizeCustomRelayRejectsArbitraryRequestsAndCredentialQueries(t *testing.T) {
	tests := []struct {
		method      string
		target      string
		apiFormat   string
		contentType string
	}{
		{method: http.MethodDelete, target: "https://api.example.com/v1/models", apiFormat: "openai"},
		{method: http.MethodGet, target: "https://api.example.com/account", apiFormat: "openai"},
		{method: http.MethodGet, target: "https://api.example.com/v1/models?api_key=secret", apiFormat: "openai"},
		{method: http.MethodPost, target: "https://api.example.com/v1/responses", apiFormat: "openai", contentType: "text/plain"},
		// 图片、音频和普通视频生成必须创建后端持久化任务，不能退回浏览器同步中继。
		{method: http.MethodPost, target: "https://api.example.com/v1/images/generations", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodPost, target: "https://api.example.com/v1/images/edits", apiFormat: "openai", contentType: "multipart/form-data; boundary=test"},
		{method: http.MethodPost, target: "https://api.example.com/v1/audio/speech", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodPost, target: "https://api.example.com/v1/videos", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodPost, target: "https://api.example.com/v1/video/generations", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodGet, target: "https://api.example.com/v1/video/generations/task-1", apiFormat: "openai"},
		{method: http.MethodPost, target: "https://api.x.ai/v1/videos/generations", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodGet, target: "https://api.x.ai/v1/videos/request-1", apiFormat: "openai"},
		{method: http.MethodPost, target: "https://generativelanguage.googleapis.com/v1beta/models/veo-3.0-generate-preview:predictLongRunning", apiFormat: "gemini", contentType: "application/json"},
		{method: http.MethodGet, target: "https://generativelanguage.googleapis.com/v1beta/operations/operation-1", apiFormat: "gemini"},
		{method: http.MethodPost, target: "https://api.example.com/v1/../account/chat/completions", apiFormat: "openai", contentType: "application/json"},
		{method: http.MethodPost, target: "https://api.example.com/v1/models/gemini:streamGenerateContent?alt=sse&token=secret", apiFormat: "gemini", contentType: "application/json"},
	}
	for _, test := range tests {
		target, err := url.Parse(test.target)
		if err != nil {
			t.Fatal(err)
		}
		if err := authorizeCustomRelay(test.method, target, test.apiFormat, test.contentType); err == nil {
			t.Fatalf("authorizeCustomRelay(%s %s) should fail", test.method, test.target)
		}
	}
}

func TestAuthorizeInteractiveModelBodyBlocksProviderNativeMediaOutput(t *testing.T) {
	rejected := []string{
		`{"model":"gpt-audio","modalities":["text","audio"]}`,
		`{"model":"gpt-image","tools":[{"type":"image_generation"}]}`,
		`{"contents":[],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
	}
	for _, body := range rejected {
		if err := authorizeInteractiveModelBody([]byte(body)); err == nil {
			t.Fatalf("authorizeInteractiveModelBody(%s) should fail", body)
		}
	}
	allowed := []string{
		`{"model":"gpt-4.1","tools":[{"type":"function","name":"canvas_read"}]}`,
		`{"contents":[],"tools":[{"functionDeclarations":[{"name":"canvas_read"}]}]}`,
	}
	for _, body := range allowed {
		if err := authorizeInteractiveModelBody([]byte(body)); err != nil {
			t.Fatalf("authorizeInteractiveModelBody(%s) error = %v", body, err)
		}
	}
}

func TestAuthorizeSystemProxyRejectsArbitraryPathAndMissingModel(t *testing.T) {
	channel := &model.ModelChannel{APIFormat: "openai", ModelsJSON: `["gpt-4.1"]`}
	if err := authorizeSystemProxy(channel, http.MethodDelete, "/account", "application/json", nil); err == nil {
		t.Fatal("expected arbitrary path to be rejected")
	}
	if err := authorizeSystemProxy(channel, http.MethodPost, "/responses", "application/json", []byte(`{"input":"missing model"}`)); err == nil {
		t.Fatal("expected missing model to be rejected")
	}
}

func TestAuthorizeSystemProxyBlocksBackendTaskMediaEndpoints(t *testing.T) {
	tests := []struct {
		interfaceType model.ChannelInterfaceType
		path          string
		contentType   string
	}{
		{interfaceType: model.ChannelInterfaceOpenAIImage, path: "/images/generations", contentType: "application/json"},
		{interfaceType: model.ChannelInterfaceOpenAIImage, path: "/images/edits", contentType: "multipart/form-data; boundary=test"},
		{interfaceType: model.ChannelInterfaceOpenAIAudio, path: "/audio/speech", contentType: "application/json"},
		{interfaceType: model.ChannelInterfaceNewAPIVideo, path: "/videos", contentType: "application/json"},
	}
	for _, test := range tests {
		channel := &model.ModelChannel{APIFormat: "openai", InterfaceType: test.interfaceType, ModelsJSON: `["media-model"]`}
		if err := authorizeSystemProxy(channel, http.MethodPost, test.path, test.contentType, []byte(`{"model":"media-model"}`)); err == nil {
			t.Fatalf("authorizeSystemProxy() error = nil for backend-task endpoint %q", test.path)
		}
	}
}

func TestProxyRequestModelReadsMultipartField(t *testing.T) {
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-1")
	_ = writer.Close()
	if got := proxyRequestModel(writer.FormDataContentType(), []byte(body.String())); got != "gpt-image-1" {
		t.Fatalf("proxyRequestModel() = %q", got)
	}
}

func TestAuthorizeSystemProxyAllowsModelLevelProtocolOverride(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1"}`)
	channel := &model.ModelChannel{APIFormat: "openai", InterfaceType: model.ChannelInterfaceChatCompletion, ModelsJSON: `["gpt-4.1"]`}
	if err := authorizeSystemProxy(channel, http.MethodPost, "/chat/completions", "application/json", body); err != nil {
		t.Fatalf("authorizeSystemProxy() error = %v", err)
	}
	// 基础代理只校验端点与正文形状；启停状态和真实协议由服务层按 channel_models 逐模型核对。
	if err := authorizeSystemProxy(channel, http.MethodPost, "/responses", "application/json", body); err != nil {
		t.Fatalf("authorizeSystemProxy() rejected model-level override: %v", err)
	}
}

func TestAuthorizeSystemProxyBlocksBackendOnlyVideoInterfaces(t *testing.T) {
	body := []byte(`{"model":"grok-image-video"}`)
	for _, interfaceType := range []model.ChannelInterfaceType{model.ChannelInterfaceNewAPIChannel2, model.ChannelInterfaceXAIVideo} {
		channel := &model.ModelChannel{APIFormat: "openai", InterfaceType: interfaceType, ModelsJSON: `["grok-image-video"]`}
		if err := authorizeSystemProxy(channel, http.MethodPost, "/video/generations", "application/json", body); err == nil {
			t.Fatalf("authorizeSystemProxy() error = nil for backend-only interface %q", interfaceType)
		}
	}
}
