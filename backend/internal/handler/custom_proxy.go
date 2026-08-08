package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

const maxCustomRelayErrorResponseBytes int64 = 64 << 10

var customRelayClient = service.CustomRelayHTTPClient

func RegisterCustomRelayRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.Any("/ai/custom", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "custom-relay:"+user.ID, policy.Request.CustomRelayPerMinute, time.Minute) {
			return
		}
		relayTimeout := time.Duration(policy.Request.CustomRelayTimeoutMinutes) * time.Minute
		requestCtx, cancelRequest := context.WithTimeout(c.Request.Context(), relayTimeout)
		defer cancelRequest()
		c.Request = c.Request.WithContext(requestCtx)
		release, acquired, err := svc.AcquireCustomRelaySlot(requestCtx, user.ID, policy.Request.CustomRelayConcurrency, relayTimeout+time.Minute)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				fail(c, http.StatusGatewayTimeout, errors.New("自定义渠道并发协调在总时限内未完成：本次没有调用供应商"))
				return
			}
			failInternal(c, http.StatusServiceUnavailable, "自定义渠道并发协调服务不可用，请稍后再试", err)
			return
		}
		if !acquired {
			fail(c, http.StatusTooManyRequests, errors.New("自定义渠道并发请求过多，请等待已有请求完成"))
			return
		}
		defer release()
		proxyCustomRelayRequest(c, policy.Request)
	})
}

func proxyCustomRelayRequest(c *gin.Context, policy service.RuntimeRequestPolicy) {
	target, err := service.ValidateCustomRelayURLContext(c.Request.Context(), c.GetHeader("X-Canvas-Upstream-URL"))
	if err != nil {
		failService(c, err)
		return
	}
	apiFormat := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Canvas-Upstream-Format")))
	if apiFormat == "" {
		apiFormat = "openai"
	}
	if err := authorizeCustomRelay(c.Request.Method, target, apiFormat, c.GetHeader("Content-Type")); err != nil {
		fail(c, http.StatusForbidden, err)
		return
	}
	apiKey, err := customRelayAPIKey(c.GetHeader("Authorization"))
	if err != nil {
		fail(c, http.StatusUnauthorized, err)
		return
	}
	requestLimit := policy.CustomRelayRequestMB << 20
	if c.Request.ContentLength > requestLimit {
		fail(c, http.StatusRequestEntityTooLarge, errors.New("自定义渠道请求超过配置上限"))
		return
	}
	requestDeadline, _ := c.Request.Context().Deadline()
	body, err := readProxyRequestBody(c, requestLimit, requestDeadline)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			fail(c, http.StatusRequestEntityTooLarge, errors.New("自定义渠道请求超过配置上限"))
			return
		}
		if proxyRequestReadTimedOut(err) || errors.Is(c.Request.Context().Err(), context.DeadlineExceeded) {
			fail(c, http.StatusGatewayTimeout, errors.New("自定义渠道请求在调用供应商前超时：本次没有发出上游请求，请检查上传速度或同步模型中转超时"))
			return
		}
		fail(c, http.StatusBadRequest, errors.New("读取自定义渠道请求失败"))
		return
	}
	if c.Request.Method == http.MethodGet && len(body) != 0 {
		fail(c, http.StatusBadRequest, errors.New("模型列表请求不允许携带请求体"))
		return
	}
	if c.Request.Method == http.MethodPost {
		if err := authorizeInteractiveModelBody(body); err != nil {
			fail(c, http.StatusForbidden, err)
			return
		}
	}
	upstreamReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		fail(c, http.StatusBadRequest, errors.New("构造自定义渠道请求失败"))
		return
	}
	if contentType := c.GetHeader("Content-Type"); contentType != "" {
		upstreamReq.Header.Set("Content-Type", contentType)
	}
	if requestWantsEventStream(c.GetHeader("Accept"), target.String(), body) {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	upstreamReq.Header.Set("User-Agent", "InfiniteCanvas/custom-channel-relay")
	if apiFormat == "gemini" {
		upstreamReq.Header.Set("x-goog-api-key", apiKey)
	} else {
		upstreamReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if errors.Is(c.Request.Context().Err(), context.DeadlineExceeded) {
		fail(c, http.StatusGatewayTimeout, errors.New("自定义渠道总时限在调用供应商前到期：本次没有发出上游请求，请稍后重试"))
		return
	}

	resp, err := customRelayClient(time.Duration(policy.CustomRelayTimeoutMinutes) * time.Minute).Do(upstreamReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			failInternal(c, http.StatusGatewayTimeout, "自定义渠道等待超时：模型请求可能仍在供应商服务端执行并产生费用，请勿立即重试，请先核对供应商后台或账单", err)
			return
		}
		failInternal(c, http.StatusBadGateway, "自定义渠道连接中断：请求状态不确定且可能已经计费，请勿立即重试，请先核对供应商后台或账单", err)
		return
	}
	defer resp.Body.Close()
	writeCustomRelayResponse(c, resp, apiKey, target.String(), body, policy.CustomRelayResponseMB<<20)
}

func writeCustomRelayResponse(c *gin.Context, resp *http.Response, apiKey string, target string, requestBody []byte, responseLimit int64) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		writeCustomRelayError(c, resp, apiKey, mediaType)
		return
	}
	responseReader, eventStream, err := eventStreamResponseReader(resp.Body, resp.Header.Get("Content-Type"), requestWantsEventStream(c.GetHeader("Accept"), target, requestBody))
	if err != nil {
		failCustomRelaySuccessfulResponse(c, "自定义渠道响应协议无效，请检查模型接口与协议配置", err)
		return
	}
	if eventStream {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("X-Accel-Buffering", "no")
		c.Status(resp.StatusCode)
		c.Writer.WriteHeaderNow()
		copyCustomRelayStream(c, responseReader, apiKey, responseLimit)
		return
	}
	if mediaType == "text/event-stream" {
		// 前缀确认是完整 JSON 时纠正反向误标，避免浏览器把 JSON 当 SSE 解析为空结果。
		mediaType = "application/json"
	}
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		failCustomRelaySuccessfulResponse(c, "自定义渠道上游返回了不支持的内容类型，请检查模型接口与协议配置", fmt.Errorf("unsupported successful custom relay content type %q", mediaType))
		return
	}
	limit := responseLimit
	body, err := readLimitedRelayBody(responseReader, limit)
	if err != nil {
		failCustomRelaySuccessfulResponse(c, "自定义渠道上游响应读取失败或超过限制，请检查模型接口", fmt.Errorf("read successful custom relay response: %w", err))
		return
	}
	if !json.Valid(body) {
		failCustomRelaySuccessfulResponse(c, "自定义渠道上游没有返回有效 JSON，请检查模型接口与协议配置", errors.New("successful custom relay response is not valid JSON"))
		return
	}
	body = redactRelaySecret(body, apiKey)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", body)
}

// POST 已经收到上游成功状态后再发生协议、读取或解析失败，不能伪装成安全的调用前错误；模型列表 GET 则不制造费用风险提示。
func failCustomRelaySuccessfulResponse(c *gin.Context, nonBillableMessage string, diagnostic error) {
	if c.Request != nil && c.Request.Method == http.MethodPost {
		failInternal(c, http.StatusBadGateway, "自定义渠道上游已返回成功状态，但响应无法完整交付；模型请求可能已经执行并产生费用，请勿立即重试，请先核对供应商后台或账单", diagnostic)
		return
	}
	failInternal(c, http.StatusBadGateway, nonBillableMessage, diagnostic)
}

func writeCustomRelayError(c *gin.Context, resp *http.Response, apiKey string, mediaType string) {
	body, err := readLimitedRelayBody(resp.Body, maxCustomRelayErrorResponseBytes)
	if service.ProviderHTTPStatusRequiresBillingReview(resp.StatusCode) {
		// 网关或服务端异常可能发生在模型已经开始执行之后，不能把上游普通错误正文原样呈现成可立即重试的明确失败。
		fail(c, resp.StatusCode, errors.New(service.ProviderHTTPBillingReviewMessage(resp.StatusCode)))
		return
	}
	if err != nil {
		fail(c, http.StatusBadGateway, errors.New("自定义渠道上游请求失败"))
		return
	}
	body = redactRelaySecret(body, apiKey)
	if (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")) && json.Valid(body) {
		c.Data(resp.StatusCode, "application/json; charset=utf-8", body)
		return
	}
	fail(c, resp.StatusCode, errors.New("自定义渠道上游请求失败"))
}

func copyCustomRelayStream(c *gin.Context, source io.Reader, apiKey string, maxBytes int64) {
	redactor := newRelayStreamRedactor(apiKey)
	integrity := &eventStreamIntegrity{}
	buffer := make([]byte, 32<<10)
	var written int64
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			remaining := maxBytes - written
			if int64(read) > remaining {
				logInternalError(c, http.StatusBadGateway, fmt.Errorf("custom relay stream exceeded response limit %d", maxBytes))
				if remaining > 0 {
					chunk := redactor.Push(buffer[:int(remaining)], false)
					if len(chunk) > 0 {
						if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
							logInternalError(c, 499, fmt.Errorf("write custom relay stream after limit: %w", writeErr))
							return
						}
						c.Writer.Flush()
					}
				}
				writeProxyStreamError(c, "自定义渠道流式响应超过上限，结果不完整且可能已经计费；请勿立即重试，请先核对供应商后台或账单")
				return
			}
			if err := integrity.Push(buffer[:read]); err != nil {
				logInternalError(c, http.StatusBadGateway, fmt.Errorf("validate custom relay stream: %w", err))
				writeProxyStreamError(c, "自定义渠道返回了损坏的流式事件，结果不完整且可能已经计费；请勿立即重试，请先核对供应商后台或账单")
				return
			}
			chunk := redactor.Push(buffer[:read], false)
			if len(chunk) > 0 {
				if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
					logInternalError(c, 499, fmt.Errorf("write custom relay stream: %w", writeErr))
					return
				}
				c.Writer.Flush()
			}
			written += int64(read)
			if written >= maxBytes {
				logInternalError(c, http.StatusBadGateway, fmt.Errorf("custom relay stream reached response limit %d", maxBytes))
				writeProxyStreamError(c, "自定义渠道流式响应达到上限，结果完整性无法确认且可能已经计费；请勿立即重试")
				return
			}
		}
		if readErr == io.EOF {
			if err := integrity.Finish(); err != nil {
				logInternalError(c, http.StatusBadGateway, fmt.Errorf("finish custom relay stream validation: %w", err))
				writeProxyStreamError(c, "自定义渠道流式响应没有完整结束，结果可能已经计费；请勿立即重试，请先核对供应商后台或账单")
				return
			}
			if tail := redactor.Push(nil, true); len(tail) > 0 {
				if _, writeErr := c.Writer.Write(tail); writeErr != nil {
					logInternalError(c, 499, fmt.Errorf("write custom relay stream tail: %w", writeErr))
					return
				}
				c.Writer.Flush()
			}
			return
		}
		if readErr != nil {
			logInternalError(c, http.StatusBadGateway, fmt.Errorf("read custom relay stream: %w", readErr))
			writeProxyStreamError(c, "自定义渠道流式响应中断，结果不完整且可能已经计费；请勿立即重试，请先核对供应商后台或账单")
			return
		}
	}
}

func readLimitedRelayBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response body is too large")
	}
	return data, nil
}

func customRelayAPIKey(value string) (string, error) {
	scheme, apiKey, found := strings.Cut(strings.TrimSpace(value), " ")
	apiKey = strings.TrimSpace(apiKey)
	if !found || !strings.EqualFold(scheme, "Bearer") || apiKey == "" || len(apiKey) > 512 || strings.ContainsAny(apiKey, "\r\n") {
		return "", errors.New("自定义渠道 API Key 无效")
	}
	return apiKey, nil
}

func redactRelaySecret(body []byte, apiKey string) []byte {
	if apiKey == "" {
		return body
	}
	return bytes.ReplaceAll(body, []byte(apiKey), []byte("[REDACTED]"))
}

type relayStreamRedactor struct {
	secret  []byte
	pending []byte
}

func newRelayStreamRedactor(secret string) *relayStreamRedactor {
	return &relayStreamRedactor{secret: []byte(secret)}
}

func (r *relayStreamRedactor) Push(chunk []byte, final bool) []byte {
	r.pending = append(r.pending, chunk...)
	if len(r.secret) == 0 {
		result := append([]byte(nil), r.pending...)
		r.pending = r.pending[:0]
		return result
	}
	r.pending = bytes.ReplaceAll(r.pending, r.secret, []byte("[REDACTED]"))
	if final {
		result := append([]byte(nil), r.pending...)
		r.pending = r.pending[:0]
		return result
	}
	keep := relaySecretPrefixSuffixLength(r.pending, r.secret)
	cut := len(r.pending) - keep
	result := append([]byte(nil), r.pending[:cut]...)
	r.pending = append(r.pending[:0], r.pending[cut:]...)
	return result
}

func relaySecretPrefixSuffixLength(data []byte, secret []byte) int {
	limit := len(secret) - 1
	if len(data) < limit {
		limit = len(data)
	}
	for length := limit; length > 0; length-- {
		if bytes.Equal(data[len(data)-length:], secret[:length]) {
			return length
		}
	}
	return 0
}
