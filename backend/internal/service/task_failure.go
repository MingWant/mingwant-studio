package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

const internalTaskFailureMessage = "任务处理发生内部错误，请联系管理员按任务 ID 核对服务端日志"
const uncertainProviderTaskFailureMessage = "模型连接或响应状态不确定，原请求可能仍在供应商服务端执行并产生费用；确认可能重复计费后可从原任务点击“重试”继续"
const providerRequestSerializationFailureMessage = "生成请求无法安全序列化，本次没有发出新的供应商请求；请联系管理员按任务 ID 核对"
const providerRequestPreflightFailureMessage = "供应商地址预检失败，本次没有发出新的供应商请求；请检查渠道地址和 DNS"
const providerRequestPreparationFailureMessage = "供应商调用准备阶段失败，本次没有发出新的供应商请求；请检查任务配置、参考资源和运行环境"
const providerRequestCancellationRiskMessage = "任务已取消，但供应商请求可能仍在执行并产生费用；确认可能重复计费后可从原任务点击“重试”继续"
const providerResultCancellationMessage = "任务在供应商成功返回后取消，但结果未完整交付；费用状态待核对，请勿重新调用供应商，请联系管理员按任务与订单恢复结果"
const providerResultPersistenceFailureMessage = "供应商已经返回成功，但生成结果在本系统持久化时失败；费用状态待核对，请勿立即重试，请联系管理员按任务与订单恢复结果"
const providerResultDeliveryFailureMessage = "供应商已经返回成功，结果检查点已保存在任务详情，但媒体持久化、会话或画布交付尚未完成；费用状态待核对，请勿重新调用供应商，请使用“恢复已保存结果”继续本地交付"

// 任务终态已经落库后，重试门禁只能依据已审查的展示文案判断费用边界；
// 明确写出“本次未发出”时允许继续，其他超时、连接中断和待核对文案都不能被无订单自定义渠道绕过。
func taskErrorRequiresBillingReview(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	noProviderCallMarker := false
	for _, safeMarker := range []string{"没有发出新的供应商请求", "没有调用供应商", "尚未调用供应商", "未调用供应商"} {
		if strings.Contains(message, safeMarker) {
			noProviderCallMarker = true
			break
		}
	}
	hasEarlierCallMarker := strings.Contains(message, "更早的供应商调用") || strings.Contains(message, "已有供应商调用") || strings.Contains(message, "成功返回")
	if noProviderCallMarker && !hasEarlierCallMarker {
		return false
	}
	lower := strings.ToLower(message)
	for _, marker := range []string{"费用状态待核对", "请勿立即重试", "状态不确定", "可能仍在", "可能已经", "更早的供应商调用", "已有供应商调用", "成功返回", "524", "408", "499", "连接中断", "连接已关闭", "响应未完整", "stream_incomplete", "超时"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

type publicTaskError struct {
	message string
	cause   error
}

func (e publicTaskError) Error() string { return e.message }
func (e publicTaskError) Unwrap() error { return e.cause }

type providerRequestNotSentError struct {
	message string
	cause   error
}

func (e providerRequestNotSentError) Error() string { return e.message }
func (e providerRequestNotSentError) Unwrap() error { return e.cause }

func newProviderRequestNotSentError(message string, cause error) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = providerRequestPreflightFailureMessage
	}
	return providerRequestNotSentError{message: message, cause: cause}
}

func providerRequestDefinitelyNotSent(err error) bool {
	var notSentErr providerRequestNotSentError
	return errors.As(err, &notSentErr)
}

// 只允许在已知仍处于供应商调用准备阶段的边界使用；保留安全、可操作的参数提示，同时为超时和底层故障补上“当前请求未发出”证据。
func markProviderPreparationFailure(err error) error {
	if err == nil || providerRequestDefinitelyNotSent(err) {
		return err
	}
	message := taskFailureMessage(err)
	if message == internalTaskFailureMessage || message == uncertainProviderTaskFailureMessage {
		message = providerRequestPreparationFailureMessage
	} else if !strings.Contains(message, "没有发出新的供应商请求") {
		message = strings.TrimRight(message, "。； ") + "；本次没有发出新的供应商请求"
	}
	return newProviderRequestNotSentError(message, err)
}

func taskFailureMessageWithBillingOutcome(err error, failedBeforeProviderRequest bool, billingOrderID string, billingReviewRequired bool) string {
	message := taskFailureMessage(err)
	if !billingReviewRequired {
		if failedBeforeProviderRequest && strings.TrimSpace(billingOrderID) != "" {
			return truncateRunes(message+"；本任务未发现更早的成功计费调用，系统将退回预留积分", 2_000)
		}
		return message
	}
	if !failedBeforeProviderRequest {
		if strings.Contains(message, "费用状态待核对") || strings.Contains(message, "可能仍在") || strings.Contains(message, "产生费用") {
			return message
		}
		if strings.TrimSpace(billingOrderID) == "" {
			return truncateRunes(message+"；本任务已有供应商调用或成功响应，请先核对供应商后台或账单后再决定是否重试", 2_000)
		}
		return truncateRunes(message+"；供应商请求已经成功返回或同一任务已有更早调用，但后续处理失败，费用状态待核对，请勿立即重试", 2_000)
	}
	if strings.TrimSpace(billingOrderID) == "" {
		return truncateRunes(message+"；本次虽未发出新的供应商请求，但同一任务已有更早的供应商调用，请先核对供应商后台或账单", 2_000)
	}
	return truncateRunes(message+"；本次虽未发出新的供应商请求，但同一任务已有更早的供应商调用，费用状态待核对，请勿立即重试", 2_000)
}

func taskCancellationMessageWithBillingOutcome(failedBeforeProviderRequest bool, billingOrderID string, billingReviewRequired bool) string {
	if failedBeforeProviderRequest {
		if billingReviewRequired {
			if strings.TrimSpace(billingOrderID) == "" {
				return "任务已取消；本次没有发出新的供应商请求，但同一任务已有更早的供应商调用，请先核对供应商后台或账单"
			}
			return "任务已取消；本次没有发出新的供应商请求，但同一任务已有更早的供应商调用，费用状态待核对，请勿立即重试"
		}
		if strings.TrimSpace(billingOrderID) == "" {
			return "任务已在供应商请求发出前取消，本次没有调用供应商"
		}
		return "任务已在供应商请求发出前取消，本次没有调用供应商；系统将退回预留积分"
	}
	if billingReviewRequired {
		return providerRequestCancellationRiskMessage
	}
	// 无法证明请求仍停在本地时保持保守口径，不能用普通“已取消”诱导再次提交。
	return "任务已取消，但供应商调用边界未能确认；请先核对供应商后台、任务或账单后再决定是否重试"
}

// 任务错误会持久化并直接展示给普通用户，只保留经过审查的业务语义；底层诊断由 Worker 服务端日志记录。
func taskFailureMessage(err error) string {
	if err == nil {
		return "任务处理失败"
	}
	var notSentErr providerRequestNotSentError
	if errors.As(err, &notSentErr) {
		return truncateRunes(strings.TrimSpace(notSentErr.message), 2_000)
	}
	var publicErr publicTaskError
	if errors.As(err, &publicErr) {
		return truncateRunes(strings.TrimSpace(publicErr.message), 2_000)
	}
	var reviewErr providerBillingReviewError
	if errors.As(err, &reviewErr) {
		return truncateRunes(strings.TrimSpace(reviewErr.reason), 2_000)
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		if authErr.Status >= 400 && authErr.Status <= 499 {
			return truncateRunes(strings.TrimSpace(authErr.Message), 2_000)
		}
		return internalTaskFailureMessage
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		return truncateRunes(httpErr.Error(), 2_000)
	}
	var slotErr channelSlotError
	if errors.As(err, &slotErr) {
		return truncateRunes(slotErr.Error(), 2_000)
	}
	if billingFailureUncertain(err) {
		return uncertainProviderTaskFailureMessage
	}
	message := strings.TrimSpace(err.Error())
	if message == "" || containsPrivateTaskDiagnostic(message) {
		return internalTaskFailureMessage
	}
	return truncateRunes(message, 2_000)
}

// SafeProviderLogError 用于会被管理界面展示的请求明细；原始网络、存储和密钥诊断只能写入 Backend 日志。
func SafeProviderLogError(err error) string {
	message := taskFailureMessage(err)
	if message == internalTaskFailureMessage {
		return "上游请求失败，底层诊断仅记录于 Backend 日志"
	}
	return truncateRunes(message, 500)
}

func providerHTTPErrorMessage(err providerHTTPError) string {
	if ProviderHTTPStatusRequiresBillingReview(err.StatusCode) {
		return ProviderHTTPBillingReviewMessage(err.StatusCode)
	}
	prefix := providerHTTPRejectionPrefix(err.StatusCode)
	code, detail := publicProviderFailureDetails(err.Body)
	qualifiers := make([]string, 0, 2)
	if code != "" {
		qualifiers = append(qualifiers, "错误码 "+code)
	}
	if detail != "" {
		qualifiers = append(qualifiers, detail)
	}
	if len(qualifiers) == 0 {
		return prefix
	}
	return prefix + "：" + strings.Join(qualifiers, "；")
}

func providerHTTPRejectionPrefix(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Sprintf("上游明确拒绝了请求参数（HTTP %d），本次不会按费用不确定处理", statusCode)
	case http.StatusUnauthorized:
		return "上游鉴权失败（HTTP 401），请检查渠道密钥和模型权限"
	case http.StatusForbidden:
		return "上游拒绝访问（HTTP 403），请检查渠道账号、模型权限或内容策略"
	case http.StatusNotFound:
		return "上游接口或模型不存在（HTTP 404），请检查 Base URL、协议和模型名"
	case http.StatusConflict:
		return "上游拒绝重复或冲突请求（HTTP 409），请先核对原任务"
	case http.StatusTooManyRequests:
		return "上游限流（HTTP 429），本次请求已被明确拒绝；请等待供应商限流窗口恢复"
	case http.StatusNotImplemented:
		return "上游接口未实现（HTTP 501），本次请求未进入模型执行"
	default:
		return fmt.Sprintf("上游返回非成功响应（HTTP %d），本次请求已被明确拒绝", statusCode)
	}
}

// 供应商响应只提取结构化短错误码和消息；整段正文可能回显请求、密钥或网关诊断，禁止进入任务详情。
func publicProviderFailureDetails(body string) (string, string) {
	var payload map[string]any
	if json.Unmarshal([]byte(body), &payload) != nil {
		return "", ""
	}
	return publicProviderFailureDetailsFromPayload(payload)
}

func publicProviderFailureDetailsFromPayload(payload map[string]any) (string, string) {
	code, message := providerFailureDetails(payload)
	code = publicProviderErrorCode(code)
	message = strings.Join(strings.Fields(message), " ")
	if containsPrivateTaskDiagnostic(message) {
		message = ""
	}
	return code, truncateRunes(message, 240)
}

func publicProviderErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 || containsPrivateTaskDiagnostic(value) {
		return ""
	}
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || current == '_' || current == '-' || current == '.' || current == ':' {
			continue
		}
		return ""
	}
	return value
}

func containsPrivateTaskDiagnostic(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"http://", "https://", "ws://", "wss://", "postgres://", "postgresql://", "redis://", "file://",
		"sqlstate", "sqlite", "postgres", "pq:", "redis:", "no such table", "constraint failed", "duplicate key value", "database is locked", "record not found",
		"dial tcp", "connectex", "connection refused", "no such host", "tls handshake", "x509:", "proxyconnect", "lookup ",
		"authorization:", "bearer ", "x-api-key:", "api_key=", "api_key:", "api-key=", "api-key:", "token=", "token:", "password=", "password:", "secret=", "secret:", "dsn=", "dsn:", "sk-",
		"cipher:", "decrypt", "解密失败", "open /", "mkdir /", "stat /", "read /", "write /", "/home/", "/var/", "/tmp/", "/etc/", "permission denied", "access is denied", "the system cannot find the path",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for index := 0; index+2 < len(message); index++ {
		if ((message[index] >= 'a' && message[index] <= 'z') || (message[index] >= 'A' && message[index] <= 'Z')) && message[index+1] == ':' && (message[index+2] == '\\' || message[index+2] == '/') {
			return true
		}
	}
	return false
}
