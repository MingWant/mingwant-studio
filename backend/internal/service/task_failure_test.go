package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestTaskFailureMessageHidesInternalDiagnostics(t *testing.T) {
	for _, err := range []error{
		errors.New("SQLSTATE 23505 duplicate key value in postgres://db.internal/app"),
		errors.New(`open C:\\deploy\\backend\\data.db: access is denied`),
		errors.New("cipher: message authentication failed"),
	} {
		message := taskFailureMessage(err)
		if message != internalTaskFailureMessage {
			t.Fatalf("taskFailureMessage(%q) = %q", err, message)
		}
	}
}

func TestTaskFailureMessageKeepsBillingRiskWithoutNetworkDetails(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://private-gateway.example/v1/chat", Err: io.ErrUnexpectedEOF}
	message := taskFailureMessage(err)
	if message != uncertainProviderTaskFailureMessage || strings.Contains(message, "private-gateway") || strings.Contains(message, "https://") {
		t.Fatalf("taskFailureMessage() = %q", message)
	}
}

func TestProviderRequestNotSentTimeoutIsRefundable(t *testing.T) {
	err := newProviderRequestNotSentError(providerRequestPreflightFailureMessage, context.DeadlineExceeded)
	if billingFailureUncertain(err) {
		t.Fatal("preflight timeout before provider request should not require billing review")
	}
	if !providerRequestDefinitelyNotSent(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preflight error lost no-send or timeout identity: %v", err)
	}
	if message := taskFailureMessage(err); message != providerRequestPreflightFailureMessage {
		t.Fatalf("taskFailureMessage() = %q", message)
	}
}

func TestTaskFailureMessageUsesActualNoSendBillingOutcome(t *testing.T) {
	err := newProviderRequestNotSentError(providerRequestPreflightFailureMessage, context.DeadlineExceeded)
	refunded := taskFailureMessageWithBillingOutcome(err, true, "order-1", false)
	if !strings.Contains(refunded, "退回预留积分") || strings.Contains(refunded, "费用状态待核对") {
		t.Fatalf("refundable no-send message = %q", refunded)
	}
	review := taskFailureMessageWithBillingOutcome(err, true, "order-1", true)
	if !strings.Contains(review, "更早的供应商调用") || !strings.Contains(review, "费用状态待核对") || strings.Contains(review, "退回预留积分") {
		t.Fatalf("review no-send message = %q", review)
	}
	customReview := taskFailureMessageWithBillingOutcome(err, true, "", true)
	if !strings.Contains(customReview, "更早的供应商调用") || !strings.Contains(customReview, "供应商后台") || strings.Contains(customReview, "退回预留积分") {
		t.Fatalf("custom-channel no-send message = %q", customReview)
	}
}

func TestTaskFailureMessageAddsReviewWarningAfterSuccessfulProviderLog(t *testing.T) {
	parseErr := errors.New("模型响应 JSON 解析失败")
	systemMessage := taskFailureMessageWithBillingOutcome(parseErr, false, "order-1", true)
	if !strings.Contains(systemMessage, "后续处理失败") || !strings.Contains(systemMessage, "费用状态待核对") || !strings.Contains(systemMessage, "请勿立即重试") {
		t.Fatalf("system post-provider failure message = %q", systemMessage)
	}
	customMessage := taskFailureMessageWithBillingOutcome(parseErr, false, "", true)
	if !strings.Contains(customMessage, "已有供应商调用或成功响应") || !strings.Contains(customMessage, "供应商后台") {
		t.Fatalf("custom post-provider failure message = %q", customMessage)
	}
	existingWarning := taskFailureMessageWithBillingOutcome(providerHTTPError{StatusCode: 524}, false, "order-1", true)
	if strings.Count(existingWarning, "请勿立即重试") != 1 {
		t.Fatalf("existing billing warning was duplicated: %q", existingWarning)
	}
}

func TestTaskCancellationMessageMatchesProviderAndBillingBoundary(t *testing.T) {
	refunded := taskCancellationMessageWithBillingOutcome(true, "order-1", false)
	if !strings.Contains(refunded, "没有调用供应商") || !strings.Contains(refunded, "退回预留积分") || strings.Contains(refunded, "费用状态待核对") {
		t.Fatalf("pre-dispatch cancellation message = %q", refunded)
	}
	customSafe := taskCancellationMessageWithBillingOutcome(true, "", false)
	if !strings.Contains(customSafe, "没有调用供应商") || strings.Contains(customSafe, "退回预留积分") {
		t.Fatalf("custom pre-dispatch cancellation message = %q", customSafe)
	}
	priorCall := taskCancellationMessageWithBillingOutcome(true, "order-1", true)
	if !strings.Contains(priorCall, "更早的供应商调用") || !strings.Contains(priorCall, "请勿立即重试") || strings.Contains(priorCall, "退回预留积分") {
		t.Fatalf("pre-dispatch cancellation after earlier call = %q", priorCall)
	}
	risk := taskCancellationMessageWithBillingOutcome(false, "order-1", true)
	for _, expected := range []string{"可能仍在执行", "费用状态待核对", "请勿立即重试"} {
		if !strings.Contains(risk, expected) {
			t.Fatalf("post-dispatch cancellation missing %q: %q", expected, risk)
		}
	}
}

func TestBillingReviewDetectsEarlierCustomChannelCallWithoutPlatformOrder(t *testing.T) {
	svc, db := newChannelConsistencyTestService(t)
	if err := db.AutoMigrate(&model.ApiCallLog{}); err != nil {
		t.Fatal(err)
	}
	log := model.ApiCallLog{ID: "call-1", TaskID: "task-1", Billable: true, Status: model.ApiCallStatusSucceeded}
	if err := db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	err := newProviderRequestNotSentError(providerRequestPreflightFailureMessage, context.DeadlineExceeded)
	if !svc.BillingFailureRequiresReview("", "task-1", err) {
		t.Fatal("earlier custom-channel supplier call should remain visible even without a platform order")
	}
}

func TestMarkProviderPreparationFailurePreservesSafeValidationDetail(t *testing.T) {
	timeoutErr := markProviderPreparationFailure(context.DeadlineExceeded)
	if !providerRequestDefinitelyNotSent(timeoutErr) || billingFailureUncertain(timeoutErr) {
		t.Fatalf("preparation timeout classification = %v", timeoutErr)
	}
	validationErr := BadAuthRequest("模型配置无效")
	marked := markProviderPreparationFailure(validationErr)
	if !providerRequestDefinitelyNotSent(marked) || !strings.Contains(taskFailureMessage(marked), "模型配置无效") {
		t.Fatalf("ordinary validation error lost actionable detail: %v", marked)
	}
}

func TestChannelSlotTimeoutBeforeRequestIsRefundable(t *testing.T) {
	err := channelSlotError{scope: "channel-1", limit: 1, err: context.DeadlineExceeded}
	if billingFailureUncertain(err) {
		t.Fatal("channel slot timeout before provider request should not require billing review by itself")
	}
}

func TestTaskFailureMessageUsesSafeBillingReviewReason(t *testing.T) {
	err := providerBillingReviewError{reason: "上游请求可能已经执行，但调用日志写入失败，费用状态待核对且请勿立即重试", cause: errors.New("redis://secret.internal:6379 unavailable")}
	message := taskFailureMessage(err)
	if message != err.reason || strings.Contains(message, "redis://") {
		t.Fatalf("taskFailureMessage() = %q", message)
	}
}

func TestProviderHTTPErrorExtractsOnlyStructuredSafeDetail(t *testing.T) {
	err := providerHTTPError{StatusCode: http.StatusBadRequest, Body: `{"error":{"code":"invalid_parameter","message":"尺寸参数不支持"},"debug":"api_key=secret"}`}
	message := err.Error()
	for _, expected := range []string{"HTTP 400", "invalid_parameter", "尺寸参数不支持", "明确拒绝"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("providerHTTPError.Error() missing %q: %q", expected, message)
		}
	}
	if strings.Contains(message, "api_key") || strings.Contains(message, "debug") {
		t.Fatalf("providerHTTPError.Error() leaked response body: %q", message)
	}
}

func TestProviderHTTPErrorDropsUnsafeMessageAndKeepsModerationCode(t *testing.T) {
	unsafeErr := providerHTTPError{StatusCode: http.StatusUnprocessableEntity, Body: `{"error":{"code":"invalid_request","message":"call https://internal.example with api_key=secret"}}`}
	if message := unsafeErr.Error(); strings.Contains(message, "https://") || strings.Contains(message, "api_key") || !strings.Contains(message, "invalid_request") {
		t.Fatalf("unsafe provider detail = %q", message)
	}
	moderationErr := providerHTTPError{StatusCode: http.StatusBadRequest, Body: `{"error":{"code":"sensitive_words_detected","message":"提示词未通过审核"}}`}
	if message := moderationErr.Error(); !strings.Contains(message, contentModerationErrorCode) || !isContentModerationFailure(message) {
		t.Fatalf("moderation provider detail = %q", message)
	}
}

func TestProviderHTTP429RemainsExplicitRejection(t *testing.T) {
	message := taskFailureMessage(providerHTTPError{StatusCode: http.StatusTooManyRequests})
	if !strings.Contains(message, "明确拒绝") || !strings.Contains(message, "限流") || strings.Contains(message, "费用状态待核对") {
		t.Fatalf("HTTP 429 task message = %q", message)
	}
}

func TestProviderHTTP5xxKeepsBillingWarningAndDropsBody(t *testing.T) {
	message := taskFailureMessage(providerHTTPError{StatusCode: 524, Body: `{"error":{"code":"gateway_timeout","message":"upstream https://private.example used api_key=secret"}}`})
	for _, expected := range []string{"524", "可能仍在", "产生费用", "请勿立即重试"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("HTTP 524 task message missing %q: %q", expected, message)
		}
	}
	if strings.Contains(message, "private.example") || strings.Contains(message, "api_key") || strings.Contains(message, "gateway_timeout") {
		t.Fatalf("HTTP 524 task message leaked provider body: %q", message)
	}
}

func TestTaskFailureMessageKeepsPublicAuthErrorAndHidesServerError(t *testing.T) {
	clientMessage := taskFailureMessage(&AuthError{Status: http.StatusBadRequest, Message: "模型配置不完整"})
	if clientMessage != "模型配置不完整" {
		t.Fatalf("client AuthError = %q", clientMessage)
	}
	serverMessage := taskFailureMessage(&AuthError{Status: http.StatusServiceUnavailable, Message: "redis://secret.internal unavailable"})
	if serverMessage != internalTaskFailureMessage {
		t.Fatalf("server AuthError = %q", serverMessage)
	}
}

func TestEnrichAPICallLogSanitizesProviderFailure(t *testing.T) {
	log := model.ApiCallLog{Status: model.ApiCallStatusFailed, StatusCode: http.StatusBadRequest}
	(&Service{}).EnrichAPICallLog(&log, []byte(`{"error":{"code":"invalid_parameter","message":"尺寸参数不支持"},"debug":"api_key=secret"}`))
	if log.ErrorCode != "invalid_parameter" {
		t.Fatalf("ErrorCode = %q", log.ErrorCode)
	}
	for _, expected := range []string{"HTTP 400", "invalid_parameter", "尺寸参数不支持"} {
		if !strings.Contains(log.Error, expected) {
			t.Fatalf("API log error missing %q: %q", expected, log.Error)
		}
	}
	if strings.Contains(log.Error, "api_key") || strings.Contains(log.Error, "debug") {
		t.Fatalf("API log leaked response body: %q", log.Error)
	}

	plainLog := model.ApiCallLog{Status: model.ApiCallStatusFailed, StatusCode: http.StatusBadRequest}
	(&Service{}).EnrichAPICallLog(&plainLog, []byte("api_key=secret and private diagnostic"))
	if !strings.Contains(plainLog.Error, "HTTP 400") || strings.Contains(plainLog.Error, "secret") {
		t.Fatalf("plain API log error = %q", plainLog.Error)
	}
}

func TestChannelSlotFailureDoesNotExposeCoordinatorError(t *testing.T) {
	err := channelSlotError{scope: "system-channel", limit: 3, err: errors.New("redis://secret.internal:6379 refused")}
	message := taskFailureMessage(err)
	if !strings.Contains(message, "尚未调用供应商") || strings.Contains(message, "redis://") {
		t.Fatalf("channel slot task message = %q", message)
	}
	cancelled := taskFailureMessage(channelSlotError{scope: "system-channel", limit: 3, err: context.Canceled})
	if !strings.Contains(cancelled, "已取消") {
		t.Fatalf("cancelled channel slot message = %q", cancelled)
	}
}
