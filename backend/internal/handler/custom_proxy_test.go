package handler

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func TestUpstreamStreamDeliveryRequiresTimeSeparatedReads(t *testing.T) {
	startedAt := time.Unix(1_700_000_000, 0)
	observation := &upstreamStreamDeliveryObservation{}
	observation.observeReadAt(32, startedAt, startedAt.Add(time.Millisecond))
	observation.observeReadAt(64, startedAt.Add(2*time.Millisecond), startedAt.Add(minimumProgressiveStreamSpan))
	if observation.Progressive() {
		t.Fatal("closely coalesced reads must not prove progressive delivery")
	}
	observation.observeReadAt(64, startedAt.Add(minimumProgressiveStreamSpan), startedAt.Add(2*minimumProgressiveStreamSpan+time.Millisecond))
	if !observation.Progressive() {
		t.Fatal("time-separated upstream reads must prove progressive delivery")
	}
}

func TestCustomRelayForwardsOpenAIRequestWithoutBrowserHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const apiKey = "relay-secret-key"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		for _, name := range []string{"Cookie", "Origin", "Referer", "X-Canvas-Upstream-URL", "X-Forwarded-For"} {
			if value := r.Header.Get(name); value != "" {
				t.Errorf("upstream received %s = %q", name, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "upstream=unsafe")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	useCustomRelayTestClient(t, upstream.Client())
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")

	request := httptest.NewRequest(http.MethodGet, "/api/ai/custom", nil)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("X-Canvas-Upstream-URL", upstream.URL+"/v1/models")
	request.Header.Set("X-Canvas-Upstream-Format", "openai")
	request.Header.Set("Cookie", "browser=session")
	request.Header.Set("Origin", "https://canvas.example.com")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request

	proxyCustomRelayRequest(context, defaultCustomRelayTestPolicy())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatal("upstream Set-Cookie should not be forwarded")
	}
	if strings.Contains(response.Body.String(), apiKey) {
		t.Fatal("response leaked API key")
	}
}

func TestCustomRelayConvertsGeminiAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const apiKey = "gemini-secret-key"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != apiKey {
			t.Errorf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization should not be forwarded, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[]}`)
	}))
	defer upstream.Close()
	useCustomRelayTestClient(t, upstream.Client())
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")

	request := httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"contents":[]}`))
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Canvas-Upstream-URL", upstream.URL+"/v1beta/models/gemini-test:generateContent")
	request.Header.Set("X-Canvas-Upstream-Format", "gemini")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request

	proxyCustomRelayRequest(context, defaultCustomRelayTestPolicy())
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCustomRelaySniffsMislabeledStreamAndFlushesBeforeCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const apiKey = "stream-secret"
	release := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 一些兼容网关实际发送 SSE，却错误沿用 application/json；代理不能因此缓冲完整长响应。
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	useCustomRelayTestClient(t, upstream.Client())
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		context, _ := gin.CreateTestContext(w)
		context.Request = r
		proxyCustomRelayRequest(context, defaultCustomRelayTestPolicy())
	}))
	defer proxy.Close()
	request, _ := http.NewRequest(http.MethodPost, proxy.URL, strings.NewReader(`{"model":"test","stream":true}`))
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-Canvas-Upstream-URL", upstream.URL+"/v1/responses")
	request.Header.Set("X-Canvas-Upstream-Format", "openai")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		close(release)
		_ = response.Body.Close()
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		close(release)
		_ = response.Body.Close()
		t.Fatal(err)
	}
	if line != "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n" {
		close(release)
		_ = response.Body.Close()
		t.Fatalf("first streamed line = %q", line)
	}
	close(release)
	_ = response.Body.Close()
}

func TestCustomRelayMarksCleanEOFWithoutTerminalAsIncomplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/ai/custom", nil)

	copyCustomRelayStream(context, strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"), "secret", 1<<20)
	if !strings.Contains(response.Body.String(), "stream_incomplete") || !strings.Contains(response.Body.String(), "没有完整结束") {
		t.Fatalf("response = %q", response.Body.String())
	}
}

func TestEventStreamIntegrityAcceptsExplicitResponseIncompleteAsTerminal(t *testing.T) {
	state := &eventStreamIntegrity{}
	if err := state.Push([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
}

func TestEventStreamIntegrityAcceptsMessageStopWithoutData(t *testing.T) {
	state := &eventStreamIntegrity{}
	if err := state.Push([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if err := state.Push([]byte("event: message_stop\n\n")); err != nil {
		t.Fatalf("Push() terminal error = %v", err)
	}
	if err := state.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
}

func TestCustomRelayWarnsInsteadOfReplayingAmbiguousGatewayError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	upstream := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporary gateway failure"}}`)),
	}

	writeCustomRelayError(context, upstream, "secret", "application/json")
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "可能仍在执行并产生费用") || strings.Contains(response.Body.String(), "temporary gateway failure") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCustomRelayPreservesExplicitRateLimitError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	upstream := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}

	writeCustomRelayError(context, upstream, "secret", "application/json")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "rate limited") || strings.Contains(response.Body.String(), "费用状态") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCustomRelaySuccessfulInvalidJSONWarnsAboutBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"model":"slow-model"}`))
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"choices":[`)),
	}

	writeCustomRelayResponse(context, upstream, "secret", "https://provider.example/v1/chat/completions", []byte(`{"stream":false}`), 1<<20)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "可能已经执行并产生费用") || !strings.Contains(response.Body.String(), "请勿立即重试") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `{"choices":[`) {
		t.Fatalf("response leaked invalid provider body: %s", response.Body.String())
	}
}

func TestCustomRelaySuccessfulInvalidModelListDoesNotClaimBillingRisk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/ai/custom", nil)
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("not-json")),
	}

	writeCustomRelayResponse(context, upstream, "secret", "https://provider.example/v1/models", nil, 1<<20)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "没有返回有效 JSON") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "产生费用") || strings.Contains(response.Body.String(), "请勿立即重试") {
		t.Fatalf("model list error incorrectly claimed billing risk: %s", response.Body.String())
	}
}

func TestCustomRelaySuccessfulUnsupportedContentTypeWarnsAboutBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"model":"slow-model"}`))
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("completed but unsupported")),
	}

	writeCustomRelayResponse(context, upstream, "secret", "https://provider.example/v1/responses", []byte(`{"stream":false}`), 1<<20)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "可能已经执行并产生费用") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "completed but unsupported") {
		t.Fatalf("response leaked unsupported provider body: %s", response.Body.String())
	}
}

func TestCustomRelaySuccessfulOversizedResponseWarnsAboutBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"model":"slow-model"}`))
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"result":"already generated"}`)),
	}

	writeCustomRelayResponse(context, upstream, "secret", "https://provider.example/v1/responses", []byte(`{"stream":false}`), 8)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "可能已经执行并产生费用") || strings.Contains(response.Body.String(), "already generated") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestEventStreamIntegrityAcceptsExplicitCompletion(t *testing.T) {
	integrity := &eventStreamIntegrity{}
	if err := integrity.Push([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := integrity.Push([]byte("data: [DONE]\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := integrity.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
}

func TestEventStreamResponseReaderCorrectsJSONMislabeledAsSSE(t *testing.T) {
	reader, eventStream, err := eventStreamResponseReader(strings.NewReader(`{"choices":[]}`), "text/event-stream", true)
	if err != nil {
		t.Fatal(err)
	}
	if eventStream {
		t.Fatal("JSON body must not be treated as SSE only because Content-Type is wrong")
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != `{"choices":[]}` {
		t.Fatalf("body = %q, error = %v", body, err)
	}
}

func TestRelayStreamRedactorHandlesSplitSecret(t *testing.T) {
	redactor := newRelayStreamRedactor("split-secret")
	output := append(redactor.Push([]byte("before split-"), false), redactor.Push([]byte("secret after"), true)...)
	if bytes.Contains(output, []byte("split-secret")) || !bytes.Contains(output, []byte("[REDACTED]")) {
		t.Fatalf("redacted output = %q", output)
	}
}

func TestCustomRelayRejectsOversizedDeclaredBodyBeforeConnecting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	connected := false
	previous := customRelayClient
	customRelayClient = func(time.Duration) *http.Client {
		connected = true
		return http.DefaultClient
	}
	t.Cleanup(func() { customRelayClient = previous })

	request := httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"model":"test"}`))
	request.ContentLength = (defaultCustomRelayTestPolicy().CustomRelayRequestMB << 20) + 1
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Canvas-Upstream-URL", "https://127.0.0.1/v1/responses")
	request.Header.Set("X-Canvas-Upstream-Format", "openai")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request

	proxyCustomRelayRequest(context, defaultCustomRelayTestPolicy())
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if connected {
		t.Fatal("oversized request should not create an upstream client")
	}
}

func TestCustomRelayRejectsProviderMediaToolBeforeConnecting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	connected := false
	previous := customRelayClient
	customRelayClient = func(time.Duration) *http.Client {
		connected = true
		return http.DefaultClient
	}
	t.Cleanup(func() { customRelayClient = previous })

	request := httptest.NewRequest(http.MethodPost, "/api/ai/custom", strings.NewReader(`{"model":"gpt-image","tools":[{"type":"image_generation"}]}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Canvas-Upstream-URL", "https://127.0.0.1/v1/responses")
	request.Header.Set("X-Canvas-Upstream-Format", "openai")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request

	proxyCustomRelayRequest(context, defaultCustomRelayTestPolicy())
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "后端持久任务") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if connected {
		t.Fatal("provider-native media tool should be rejected before connecting upstream")
	}
}

func useCustomRelayTestClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := customRelayClient
	customRelayClient = func(time.Duration) *http.Client { return client }
	t.Cleanup(func() { customRelayClient = previous })
}

func defaultCustomRelayTestPolicy() service.RuntimeRequestPolicy {
	return service.RuntimeRequestPolicy{
		CustomRelayRequestMB: 32, CustomRelayResponseMB: 32, CustomRelayTimeoutMinutes: 10,
	}
}
