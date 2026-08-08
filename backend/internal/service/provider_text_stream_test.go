package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProviderTextTransportRequiresObservedProgressiveDelivery(t *testing.T) {
	startedAt := time.Unix(1_700_000_000, 0)
	observation := &providerResponseReadObservation{}
	observation.begin(startedAt)
	observation.observeReadAt(32, startedAt.Add(110*time.Millisecond), startedAt.Add(120*time.Millisecond))
	observation.observeReadAt(64, startedAt.Add(130*time.Millisecond), startedAt.Add(220*time.Millisecond))
	delivery := observation.snapshot()
	if delivery.FirstByteMs != 120 || delivery.DeliverySpanMs != 100 || delivery.LongestReadWaitMs != 90 || delivery.TotalFollowupWaitMs != 90 || delivery.ReadCount != 2 || !delivery.Progressive {
		t.Fatalf("progressive delivery = %#v", delivery)
	}
	if transport := providerTextTransport(true, delivery); transport != "stream" {
		t.Fatalf("progressive transport = %q", transport)
	}

	coalesced := &providerResponseReadObservation{}
	coalesced.begin(startedAt)
	coalesced.observeReadAt(32, startedAt.Add(5*time.Second-time.Millisecond), startedAt.Add(5*time.Second))
	coalesced.observeReadAt(64, startedAt.Add(5*time.Second+time.Millisecond), startedAt.Add(5*time.Second+2*time.Millisecond))
	coalescedDelivery := coalesced.snapshot()
	if coalescedDelivery.Progressive || providerTextTransport(true, coalescedDelivery) != "stream-unverified" {
		t.Fatalf("coalesced delivery = %#v", coalescedDelivery)
	}
	frequent := &providerResponseReadObservation{}
	frequent.begin(startedAt)
	frequent.observeReadAt(16, startedAt, startedAt.Add(time.Millisecond))
	for index := 0; index < 6; index++ {
		readStartedAt := startedAt.Add(time.Duration(index+1) * 20 * time.Millisecond)
		frequent.observeReadAt(16, readStartedAt, readStartedAt.Add(10*time.Millisecond))
	}
	if delivery := frequent.snapshot(); !delivery.Progressive || delivery.TotalFollowupWaitMs != 60 {
		t.Fatalf("frequent progressive delivery = %#v", delivery)
	}
	if providerTextTransport(false, delivery) != "non-stream-compatible" {
		t.Fatal("complete JSON must remain non-stream-compatible")
	}
}

func TestResponsesTextFromProviderBodyReadsSSE(t *testing.T) {
	raw := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"你\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"好\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	text, streamed, err := responsesTextFromProviderBody(raw, "text/event-stream")
	if err != nil || !streamed || text != "你好" {
		t.Fatalf("responsesTextFromProviderBody() = %q, %v, %v", text, streamed, err)
	}
	payload := providerPayloadForAnalytics(raw)
	if payload == nil || payload["usage"] == nil {
		t.Fatalf("providerPayloadForAnalytics() = %#v", payload)
	}
}

func TestChatTextFromProviderBodyReadsSSE(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"测\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"活\"}}]}\n\ndata: [DONE]\n\n")
	text, streamed, err := chatTextFromProviderBody(raw, "text/event-stream")
	if err != nil || !streamed || text != "测活" {
		t.Fatalf("chatTextFromProviderBody() = %q, %v, %v", text, streamed, err)
	}
}

func TestTextStreamAcceptsNoDataMessageStopTerminal(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"完成\"}}]}\n\nevent: message_stop\n\n")
	text, streamed, err := chatTextFromProviderBody(raw, "text/event-stream")
	if err != nil || !streamed || text != "完成" {
		t.Fatalf("chatTextFromProviderBody() with message_stop = %q, %v, %v", text, streamed, err)
	}
}

func TestTextStreamRejectsNoDataIncompleteTerminal(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}}]}\n\nevent: response.incomplete\n\n")
	_, streamed, err := chatTextFromProviderBody(raw, "text/event-stream")
	if err == nil || !streamed || !strings.Contains(err.Error(), "未完整结束") {
		t.Fatalf("chatTextFromProviderBody() with response.incomplete = %v, %v", streamed, err)
	}
}

func TestGeminiTextFromProviderBodyReadsSSEWithoutThoughts(t *testing.T) {
	raw := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"内部推理\",\"thought\":true},{\"text\":\"测\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"活\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2}}\n\n")
	text, streamed, err := geminiTextFromProviderBody(raw, "text/event-stream")
	if err != nil || !streamed || text != "测活" {
		t.Fatalf("geminiTextFromProviderBody() = %q, %v, %v", text, streamed, err)
	}
	payload := providerPayloadForAnalytics(raw)
	if payload == nil || payload["usageMetadata"] == nil {
		t.Fatalf("providerPayloadForAnalytics() = %#v", payload)
	}
}

func TestGeminiTextFromProviderBodyReadsBufferedArray(t *testing.T) {
	raw := []byte(`[{"candidates":[{"content":{"parts":[{"text":"完整"}]}}]},{"candidates":[{"content":{"parts":[{"text":"结果"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":2}}]`)
	text, streamed, err := geminiTextFromProviderBody(raw, "application/json")
	if err != nil || streamed || text != "完整结果" {
		t.Fatalf("geminiTextFromProviderBody() = %q, %v, %v", text, streamed, err)
	}
	if payload := providerPayloadForAnalytics(raw); payload == nil || payload["usageMetadata"] == nil {
		t.Fatalf("providerPayloadForAnalytics() = %#v", payload)
	}
}

func TestGeminiTextRejectsTruncatedStream(t *testing.T) {
	raw := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"半截\"}]},\"finishReason\":\"MAX_TOKENS\"}]}\n\n")
	_, streamed, err := geminiTextFromProviderBody(raw, "text/event-stream")
	if err == nil || !streamed || !strings.Contains(err.Error(), "未使用半截结果") {
		t.Fatalf("geminiTextFromProviderBody() streamed = %v, error = %v", streamed, err)
	}
}

func TestGeminiTextRejectsCleanEOFWithoutStop(t *testing.T) {
	raw := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"半截\"}]}}]}\n\n")
	_, streamed, err := geminiTextFromProviderBody(raw, "text/event-stream")
	if err == nil || !streamed || !strings.Contains(err.Error(), "缺少完成标记") {
		t.Fatalf("geminiTextFromProviderBody() streamed = %v, error = %v", streamed, err)
	}
	if !billingFailureUncertain(err) {
		t.Fatalf("incomplete Gemini stream must require billing review: %v", err)
	}
}

func TestTextStreamRejectsCleanEOFWithoutTerminal(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"半截\"}}]}\n\n")
	_, streamed, err := chatTextFromProviderBody(raw, "text/event-stream")
	if err == nil || !streamed || !strings.Contains(err.Error(), "缺少完成标记") {
		t.Fatalf("chatTextFromProviderBody() streamed = %v, error = %v", streamed, err)
	}
	if !billingFailureUncertain(err) {
		t.Fatalf("incomplete 2xx stream must require billing review: %v", err)
	}
}

func TestTextStreamRejectsExplicitIncompleteWithoutUsingProviderReason(t *testing.T) {
	raw := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"半截\"}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens private-debug\"}}}\n\n")
	_, streamed, err := responsesTextFromProviderBody(raw, "text/event-stream")
	if err == nil || !streamed || !strings.Contains(err.Error(), "未使用半截结果") || strings.Contains(err.Error(), "private-debug") {
		t.Fatalf("responsesTextFromProviderBody() streamed = %v, error = %v", streamed, err)
	}
	if !billingFailureUncertain(err) {
		t.Fatalf("explicit incomplete response must require billing review: %v", err)
	}
}

func TestChatTextRejectsExplicitOutputLimit(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{"content":"半截"},"finish_reason":"length"}]}`)
	_, streamed, err := chatTextFromProviderBody(raw, "application/json")
	if err == nil || streamed || !strings.Contains(err.Error(), "达到输出上限") {
		t.Fatalf("chatTextFromProviderBody() output limit = %v, %v", streamed, err)
	}
	if !billingFailureUncertain(err) {
		t.Fatalf("explicit Chat output limit must require billing review: %v", err)
	}
}

func TestResponsesTextRejectsExplicitIncompleteJSON(t *testing.T) {
	raw := []byte(`{"status":"incomplete","output_text":"半截","incomplete_details":{"reason":"max_output_tokens"}}`)
	_, streamed, err := responsesTextFromProviderBody(raw, "application/json")
	if err == nil || streamed || !strings.Contains(err.Error(), "未完整结束") {
		t.Fatalf("responsesTextFromProviderBody() incomplete = %v, %v", streamed, err)
	}
	if !billingFailureUncertain(err) {
		t.Fatalf("incomplete Responses JSON must require billing review: %v", err)
	}
}

func TestTextStreamCorrectsJSONMislabeledAsSSE(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{"content":"完整结果"}}]}`)
	text, streamed, err := chatTextFromProviderBody(raw, "text/event-stream")
	if err != nil || streamed || text != "完整结果" {
		t.Fatalf("chatTextFromProviderBody() = %q, %v, %v", text, streamed, err)
	}
}

func TestTextFallbackDoesNotRetryUncertainGatewayFailure(t *testing.T) {
	for _, statusCode := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 524} {
		err := providerHTTPError{StatusCode: statusCode}
		if shouldFallbackTextToChat(err) || shouldFallbackStreamToNonStream(err) {
			t.Fatalf("status %d must not trigger a second provider request", statusCode)
		}
		if !billingFailureUncertain(err) {
			t.Fatalf("status %d must require billing review even without a timeout body", statusCode)
		}
	}
}

func TestOversizedProviderResponseRequiresBillingReview(t *testing.T) {
	err := errors.New("上游响应超过 64MB 限制")
	if !billingFailureUncertain(err) {
		t.Fatalf("oversized completed response must require billing review: %v", err)
	}
}

func TestStreamFallbackRequiresExplicitUnsupportedResponse(t *testing.T) {
	err := providerHTTPError{StatusCode: http.StatusBadRequest, Body: "stream is unsupported"}
	if !shouldFallbackStreamToNonStream(err) {
		t.Fatal("explicit unsupported stream response should allow one non-stream fallback")
	}
}

func TestStreamingRequiredErrorExplainsNoSecondRequest(t *testing.T) {
	err := streamingRequiredError(providerHTTPError{StatusCode: http.StatusBadRequest, Body: "stream is unsupported"})
	message := err.Error()
	if !strings.Contains(message, "未发起第二次非流式请求") || !strings.Contains(message, "中间网关") {
		t.Fatalf("streamingRequiredError() = %q", message)
	}
	if billingFailureUncertain(err) {
		t.Fatalf("explicit stream rejection before fallback must remain refundable: %q", message)
	}
	if persisted := taskFailureMessage(err); !strings.Contains(persisted, "未发起第二次非流式请求") || strings.Contains(persisted, "stream is unsupported") {
		t.Fatalf("persisted streaming-required error = %q", persisted)
	}
}

func TestStoryboardStreamingReadinessIgnoresLocalCancellation(t *testing.T) {
	if shouldDegradeStoryboardStreamingReadiness(context.Canceled) {
		t.Fatal("local cancellation must not downgrade a shared system model")
	}
	if !shouldDegradeStoryboardStreamingReadiness(providerHTTPError{StatusCode: 524}) {
		t.Fatal("upstream 524 must downgrade storyboard streaming readiness")
	}
}
