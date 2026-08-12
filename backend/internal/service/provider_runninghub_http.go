package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func runningHubPostJSON(ctx context.Context, config providerConfig, path string, body interface{}, target interface{}) error {
	envelope, err := runningHubPostJSONEnvelope(ctx, config, path, body)
	if err != nil {
		return err
	}
	return runningHubDecodeEnvelope(envelope, target)
}

func runningHubPostJSONEnvelope(ctx context.Context, config providerConfig, path string, body interface{}) (runningHubEnvelope, error) {
	data, err := marshalProviderRequest(body)
	if err != nil {
		return runningHubEnvelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, runningHubEndpointURL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return runningHubEnvelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if path == "/task/openapi/create" {
		if idempotencyKey := runningHubIdempotencyKey(ctx); idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
	}
	return runningHubDoJSONEnvelope(req)
}

func runningHubIdempotencyKey(ctx context.Context) string {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || strings.TrimSpace(metadata.TaskID) == "" {
		return ""
	}
	// 同一任务的显式付费重试会增加 attempts；每次逻辑创建稳定且彼此隔离。
	return "mingwant:" + strings.TrimSpace(metadata.TaskID) + ":" + strconv.Itoa(metadata.TaskAttempts)
}

func runningHubPostForm(ctx context.Context, config providerConfig, path string, contentType string, body *bytes.Buffer, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, runningHubEndpointURL(config.BaseURL, path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", contentType)
	return runningHubDoJSON(req, target)
}

func runningHubDoJSON(req *http.Request, target interface{}) error {
	envelope, err := runningHubDoJSONEnvelope(req)
	if err != nil {
		return err
	}
	return runningHubDecodeEnvelope(envelope, target)
}

func runningHubDoJSONEnvelope(req *http.Request) (runningHubEnvelope, error) {
	data, _, err := doBinaryWithResponseLimit(req, runningHubJSONResponseLimit)
	if err != nil {
		return runningHubEnvelope{}, err
	}
	var envelope runningHubEnvelope
	if json.Unmarshal(data, &envelope) != nil {
		return runningHubEnvelope{}, errors.New("RunningHub 返回了无效 JSON")
	}
	return envelope, nil
}

func runningHubDecodeEnvelope(envelope runningHubEnvelope, target interface{}) error {
	code := rawJSONScalarString(envelope.Code)
	if code != "" && code != "0" {
		return runningHubEnvelopeError(envelope)
	}
	if target == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if raw, ok := target.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], envelope.Data...)
		return nil
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return errors.New("RunningHub data 字段格式无效")
	}
	return nil
}

func runningHubEnvelopeError(envelope runningHubEnvelope) error {
	rawCode := rawJSONScalarString(envelope.Code)
	safeCode := publicProviderErrorCode(rawCode)
	if safeCode == "" {
		safeCode = "provider_rejected"
	}
	applicationErr := runningHubApplicationError{Code: safeCode, Message: runningHubSafeMessage(envelope.Msg)}
	if runningHubApplicationCodeUncertain(rawCode) {
		return providerBillingReviewError{reason: "RunningHub 返回了状态不确定的上游业务错误，原请求可能已经执行并产生费用；请勿立即重试", cause: applicationErr}
	}
	return applicationErr
}

func runningHubApplicationCodeUncertain(code string) bool {
	code = strings.TrimSpace(code)
	// HTTP 200 信封里的服务端异常、执行超时和服务不可用同样不能证明付费 POST 未执行。
	return code == "500" || code == "1000" || code == "1005" || code == "1006" || code == "1010"
}

func runningHubEndpointURL(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(base), "/task/openapi") {
		return base + strings.TrimPrefix(path, "/task/openapi")
	}
	return base + path
}

func rawJSONScalarString(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) == nil {
			return strings.TrimSpace(decoded)
		}
	}
	return value
}

func runningHubScalarString(value interface{}) string {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case json.Number:
		return item.String()
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case int:
		return strconv.Itoa(item)
	case int64:
		return strconv.FormatInt(item, 10)
	default:
		return ""
	}
}

func runningHubSafeMessage(value string) string {
	value = strings.TrimSpace(strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return ' '
		}
		return char
	}, value))
	runes := []rune(value)
	if len(runes) > 300 {
		value = string(runes[:300])
	}
	if containsPrivateTaskDiagnostic(value) {
		return "上游拒绝了请求"
	}
	return defaultString(value, "上游拒绝了请求")
}

func runningHubFileTypeMIME(fileType string) string {
	value := strings.ToLower(strings.TrimSpace(fileType))
	if strings.Contains(value, "/") {
		return value
	}
	switch strings.TrimPrefix(value, ".") {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	default:
		return ""
	}
}
