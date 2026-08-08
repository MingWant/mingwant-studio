package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const maxEventStreamSniffBytes = 64

// 与 Worker 探针使用相同阈值；短前缀识别产生的连续本地读取不能被误当成上游渐进分片。
const minimumProgressiveStreamSpan = 50 * time.Millisecond

type upstreamStreamDeliveryObservation struct {
	firstReadAt       time.Time
	lastReadAt        time.Time
	readCount         int
	progressive       bool
	totalFollowupWait time.Duration
}

type observedUpstreamStreamReader struct {
	source      io.Reader
	observation *upstreamStreamDeliveryObservation
}

func (reader *observedUpstreamStreamReader) Read(buffer []byte) (int, error) {
	readStartedAt := time.Now()
	read, err := reader.source.Read(buffer)
	if read > 0 {
		reader.observation.observeReadAt(read, readStartedAt, time.Now())
	}
	return read, err
}

func (observation *upstreamStreamDeliveryObservation) observeReadAt(read int, readStartedAt time.Time, observedAt time.Time) {
	if observation == nil || read <= 0 {
		return
	}
	if observation.firstReadAt.IsZero() {
		observation.firstReadAt = observedAt
	} else {
		readWait := observedAt.Sub(readStartedAt)
		if readWait > 0 {
			observation.totalFollowupWait += readWait
		}
		if observation.totalFollowupWait >= minimumProgressiveStreamSpan {
			observation.progressive = true
		}
	}
	observation.lastReadAt = observedAt
	observation.readCount++
}

func (observation *upstreamStreamDeliveryObservation) Progressive() bool {
	return observation != nil && observation.readCount > 1 && observation.progressive
}

// 同步代理不设置全局 Server.ReadTimeout；只在当前连接读取请求体时施加业务截止时间，避免慢上传占住并发槽位。
func readProxyRequestBody(c *gin.Context, limit int64, deadline time.Time) ([]byte, error) {
	controller := http.NewResponseController(c.Writer)
	deadlineSet := false
	if !deadline.IsZero() {
		if err := controller.SetReadDeadline(deadline); err == nil {
			deadlineSet = true
		} else if !errors.Is(err, http.ErrNotSupported) {
			return nil, err
		}
	}
	if deadlineSet {
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	return io.ReadAll(c.Request.Body)
}

func proxyRequestReadTimedOut(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

type eventStreamIntegrity struct {
	buffer   string
	sawData  bool
	terminal bool
}

// 请求体和目标 URL 比响应 Content-Type 更能表达流式意图；部分兼容网关会发送合法 SSE 却误标为 JSON。
func requestWantsEventStream(accept string, target string, body []byte) bool {
	if strings.Contains(strings.ToLower(accept), "text/event-stream") {
		return true
	}
	if parsed, err := url.Parse(target); err == nil {
		if strings.EqualFold(parsed.Query().Get("alt"), "sse") || strings.Contains(strings.ToLower(parsed.Path), "streamgeneratecontent") {
			return true
		}
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

func requestWantsTextEventStream(accept string, target string, body []byte) bool {
	if !requestWantsEventStream(accept, target, body) {
		return false
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
	return strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/chat/completions") || strings.Contains(path, "streamgeneratecontent")
}

// 只读取足够判断协议的极短前缀，再把前缀拼回去；不会为了识别错误 Content-Type 而缓冲完整模型响应。
func eventStreamResponseReader(source io.Reader, contentType string, requested bool) (io.Reader, bool, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	declaredEventStream := mediaType == "text/event-stream"
	if !declaredEventStream && !requested {
		return source, false, nil
	}
	prefix := make([]byte, 0, maxEventStreamSniffBytes)
	one := make([]byte, 1)
	for len(prefix) < maxEventStreamSniffBytes {
		read, readErr := source.Read(one)
		if read > 0 {
			prefix = append(prefix, one[0])
			if decided, eventStream := classifyEventStreamPrefix(prefix); decided {
				return io.MultiReader(bytes.NewReader(prefix), source), eventStream || (declaredEventStream && !prefixLooksJSON(prefix)), nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				decided, eventStream := classifyEventStreamPrefix(prefix)
				if !decided && requested && !prefixLooksJSON(prefix) {
					eventStream = true
				}
				return bytes.NewReader(prefix), eventStream || (declaredEventStream && !prefixLooksJSON(prefix)), nil
			}
			return nil, false, readErr
		}
	}
	return io.MultiReader(bytes.NewReader(prefix), source), (declaredEventStream || requested) && !prefixLooksJSON(prefix), nil
}

func prefixLooksJSON(prefix []byte) bool {
	index := 0
	for index < len(prefix) && isASCIISpace(prefix[index]) {
		index++
	}
	if len(prefix)-index >= 3 && bytes.Equal(prefix[index:index+3], []byte{0xef, 0xbb, 0xbf}) {
		index += 3
		for index < len(prefix) && isASCIISpace(prefix[index]) {
			index++
		}
	}
	return index < len(prefix) && (prefix[index] == '{' || prefix[index] == '[')
}

func classifyEventStreamPrefix(prefix []byte) (bool, bool) {
	index := 0
	for index < len(prefix) && isASCIISpace(prefix[index]) {
		index++
	}
	if index == len(prefix) {
		return false, false
	}
	if prefix[index] == 0xef {
		bom := []byte{0xef, 0xbb, 0xbf}
		remaining := prefix[index:]
		if len(remaining) < len(bom) && bytes.Equal(remaining, bom[:len(remaining)]) {
			return false, false
		}
		if bytes.HasPrefix(remaining, bom) {
			index += len(bom)
			for index < len(prefix) && isASCIISpace(prefix[index]) {
				index++
			}
			if index == len(prefix) {
				return false, false
			}
		}
	}
	value := string(prefix[index:])
	for _, marker := range []string{"data:", "event:", "id:", "retry:"} {
		if strings.HasPrefix(value, marker) {
			return true, true
		}
		if strings.HasPrefix(marker, value) {
			return false, false
		}
	}
	if strings.HasPrefix(value, ":") {
		return true, true
	}
	return true, false
}

func isASCIISpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func (state *eventStreamIntegrity) Push(chunk []byte) error {
	state.buffer += string(chunk)
	state.buffer = normalizeEventStreamLineEndings(state.buffer, false)
	for {
		index := strings.Index(state.buffer, "\n\n")
		if index < 0 {
			return nil
		}
		if err := state.consumeBlock(state.buffer[:index]); err != nil {
			return err
		}
		state.buffer = state.buffer[index+2:]
	}
}

func (state *eventStreamIntegrity) Finish() error {
	state.buffer = normalizeEventStreamLineEndings(state.buffer, true)
	if strings.TrimSpace(state.buffer) != "" {
		if err := state.consumeBlock(state.buffer); err != nil {
			return err
		}
	}
	state.buffer = ""
	if !state.sawData {
		return errors.New("流式响应没有数据事件")
	}
	if !state.terminal {
		return errors.New("流式响应缺少完成标记")
	}
	return nil
}

func normalizeEventStreamLineEndings(value string, final bool) string {
	pendingCR := !final && strings.HasSuffix(value, "\r")
	if pendingCR {
		value = strings.TrimSuffix(value, "\r")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if pendingCR {
		value += "\r"
	}
	return value
}

func (state *eventStreamIntegrity) consumeBlock(block string) error {
	dataLines := make([]string, 0, 1)
	eventName := ""
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	// 部分兼容网关用不带 data 的 `event: message_stop` 表示正常结束。
	// 终态事件本身就是协议元数据，不能因为没有正文而漏记，否则代理会把
	// 已完整返回的工具调用误判为截断并把费用置为待核对。
	switch strings.ToLower(strings.TrimSpace(eventName)) {
	case "done", "message_stop", "response.completed", "response.incomplete":
		state.terminal = true
	}
	if len(dataLines) == 0 {
		return nil
	}
	data := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if data == "" {
		return nil
	}
	state.sawData = true
	if data == "[DONE]" {
		state.terminal = true
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return errors.New("流式响应包含无效 JSON 事件")
	}
	if eventPayloadHasError(payload) {
		return errors.New("流式响应包含上游错误事件")
	}
	if strings.EqualFold(eventName, "done") || eventPayloadIsTerminal(payload) {
		state.terminal = true
	}
	return nil
}

func eventPayloadHasError(payload map[string]any) bool {
	for _, item := range streamPayloadVariants(payload) {
		if value, exists := item["error"]; exists && value != nil {
			return true
		}
		typeName := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
		if typeName == "error" || typeName == "response.failed" || strings.HasSuffix(typeName, ".error") {
			return true
		}
		feedback, _ := item["promptFeedback"].(map[string]any)
		if strings.TrimSpace(stringValue(feedback["blockReason"])) != "" {
			return true
		}
	}
	return false
}

func eventPayloadIsTerminal(payload map[string]any) bool {
	for _, item := range streamPayloadVariants(payload) {
		if done, _ := item["done"].(bool); done {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(stringValue(item["type"]))) {
		case "response.completed", "response.incomplete", "message_stop":
			return true
		}
		for _, key := range []string{"choices", "candidates"} {
			items, _ := item[key].([]any)
			for _, raw := range items {
				choice, _ := raw.(map[string]any)
				if stringValue(choice["finish_reason"]) != "" || stringValue(choice["finishReason"]) != "" {
					return true
				}
			}
		}
	}
	return false
}

// 兼容网关有时会把标准 SSE JSON 再包进 data；完整性校验必须与浏览器解析使用同一层级，
// 否则上游已经带 finish_reason/完成事件，代理仍会追加 stream_incomplete。
func streamPayloadVariants(payload map[string]any) []map[string]any {
	variants := []map[string]any{payload}
	current := payload
	for depth := 0; depth < 2; depth++ {
		nested, ok := current["data"].(map[string]any)
		if !ok || nested == nil {
			break
		}
		variants = append(variants, nested)
		current = nested
	}
	return variants
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// HTTP 200 已经发出后无法改状态码，只能在同一 SSE 中追加标准错误事件，阻止浏览器把半截结果当成功。
func writeProxyStreamError(c *gin.Context, message string) {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    "stream_incomplete",
			"message": message,
		},
	})
	_, _ = c.Writer.Write(append(append([]byte("\n\ndata: "), payload...), []byte("\n\n")...))
	c.Writer.Flush()
}
