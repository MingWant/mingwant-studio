package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

var errRunningHubNaturalCompletion = errors.New("RunningHub 工作流已自然结束，未满足中途取消约束")

// 工作流自身已进入失败/取消终态时不能再次调用取消接口。除了没有节省意义，
// 部分消费级 API Key 还会返回 817，遮住真正的失败节点。
type runningHubWorkflowTerminalError struct {
	message string
}

func (e *runningHubWorkflowTerminalError) Error() string {
	return e.message
}

func isRunningHubWorkflowTerminalError(err error) bool {
	var target *runningHubWorkflowTerminalError
	return errors.As(err, &target)
}

func waitForRunningHubWSS(ctx context.Context, config providerConfig, taskID string, initialURL string, wait time.Duration) (runningHubOutputState, error) {
	if initialURL != "" {
		return runningHubOutputState{WSSURL: initialURL}, nil
	}
	deadline := time.Now().Add(wait)
	for {
		state, err := queryRunningHubOutputs(withProviderRequestID(ctx, taskID), config, taskID)
		if err == nil {
			if len(state.Outputs) > 0 {
				return state, nil
			}
			if runningHubFailedStatus(state.Status) {
				return state, &runningHubWorkflowTerminalError{message: runningHubUnexpectedFailureMessage(state.FailureNodeID)}
			}
			if runningHubCompletedStatus(state.Status) {
				return state, errRunningHubNaturalCompletion
			}
			if state.WSSURL != "" {
				return state, nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return runningHubOutputState{}, errors.Join(errors.New("RunningHub WSS 地址等待超时"), err)
			}
			return runningHubOutputState{}, errors.New("RunningHub WSS 地址等待超时")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return runningHubOutputState{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func monitorRunningHubResultNode(ctx context.Context, wssURL string, workflow *RunningHubWorkflowConfig, onResultEvidence func(runningHubNodeReady) error, onNodeExecuting func(string) error) (runningHubNodeReady, error) {
	ws, err := dialRunningHubWebSocket(ctx, wssURL)
	if err != nil {
		return runningHubNodeReady{}, errors.Join(errors.New("RunningHub WSS 连接失败"), err)
	}
	defer ws.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-stop:
		}
	}()
	stopNodes := make(map[string]struct{}, len(workflow.StopOnNodeIDs))
	for _, nodeID := range workflow.StopOnNodeIDs {
		stopNodes[nodeID] = struct{}{}
	}
	lastExecutingNode := ""
	ready := runningHubNodeReady{}
	breakpointMode := workflow.TerminationMode == runningHubTerminationBreakpoint
	notifyResultEvidence := func() error {
		if onResultEvidence == nil || !ready.ResultNodeCompleted {
			return nil
		}
		return onResultEvidence(ready)
	}
	notifyNodeExecuting := func(nodeID string) error {
		if onNodeExecuting == nil || nodeID == "" || nodeID == lastExecutingNode {
			return nil
		}
		return onNodeExecuting(nodeID)
	}
	for {
		readDeadline := time.Now().Add(time.Duration(workflow.MonitorSilenceSeconds) * time.Second)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(readDeadline) {
			readDeadline = deadline
		}
		_ = ws.SetReadDeadline(readDeadline)
		var raw []byte
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			if ctx.Err() != nil {
				return ready, ctx.Err()
			}
			return ready, errors.Join(errors.New("RunningHub WSS 监控中断或超过静默保护时间"), err)
		}
		var event map[string]interface{}
		if len(raw) == 0 || json.Unmarshal(raw, &event) != nil {
			return ready, errors.New("RunningHub WSS 返回了损坏事件，已停止读取并进入原任务恢复")
		}
		event = unwrapRunningHubWSSEvent(event)
		eventType := strings.ToLower(strings.TrimSpace(stringField(event, "type")))
		data, _ := event["data"].(map[string]interface{})
		if data == nil {
			data = event
		}
		nodeID := runningHubScalarString(data["node"])
		switch eventType {
		case "executed":
			if nodeID == workflow.ResultNodeID {
				resultURL, fileType := extractRunningHubEventResult(data)
				ready.ResultURL = resultURL
				ready.ResultFileType = fileType
				ready.ResultNodeCompleted = true
				if err := notifyResultEvidence(); err != nil {
					return ready, errors.Join(errors.New("RunningHub 结果节点证据保存失败"), err)
				}
				if !breakpointMode {
					return ready, nil
				}
			}
			if breakpointMode && nodeID == workflow.FailureNodeID {
				// 断点节点意外执行完成说明注入值没有触发预期错误；这里只触发取消兜底，
				// 不能反向伪造节点 12 的完成证据。
				return ready, nil
			}
		case "execution_cached":
			if runningHubNodeListContains(data["nodes"], workflow.ResultNodeID) {
				ready.ResultNodeCompleted = true
				if err := notifyResultEvidence(); err != nil {
					return ready, errors.Join(errors.New("RunningHub 结果节点证据保存失败"), err)
				}
				if !breakpointMode {
					return ready, nil
				}
			}
			if breakpointMode && runningHubNodeListContains(data["nodes"], workflow.FailureNodeID) {
				return ready, nil
			}
		case "executing":
			if err := notifyNodeExecuting(nodeID); err != nil {
				return ready, errors.Join(errors.New("RunningHub 可见进度保存失败"), err)
			}
			// RunningHub 官方示例用“下一个 executing 到达”表示上一个节点完成；
			// 因此即使网关不转发 ComfyUI 的 executed 事件，也能在结果节点后立刻抢停。
			if nodeID != "" && lastExecutingNode == workflow.ResultNodeID {
				ready.ResultNodeCompleted = true
				if err := notifyResultEvidence(); err != nil {
					return ready, errors.Join(errors.New("RunningHub 结果节点证据保存失败"), err)
				}
				if !breakpointMode {
					return ready, nil
				}
			}
			if breakpointMode && nodeID != "" && lastExecutingNode == workflow.FailureNodeID {
				return ready, nil
			}
			if _, stopNow := stopNodes[nodeID]; stopNow {
				return ready, nil
			}
			if _, hasNode := data["node"]; hasNode && data["node"] == nil {
				return ready, errRunningHubNaturalCompletion
			}
			if nodeID != "" {
				lastExecutingNode = nodeID
			}
		case "progress":
			if err := notifyNodeExecuting(nodeID); err != nil {
				return ready, errors.Join(errors.New("RunningHub 可见进度保存失败"), err)
			}
			// 重新连接可能错过节点的 executing 事件；进行中分片仍带 node，
			// 因此二采兜底节点一旦已有进度也要立刻进入取消保护。
			if _, stopNow := stopNodes[nodeID]; stopNow {
				return ready, nil
			}
			if nodeID != "" {
				lastExecutingNode = nodeID
			}
		case "execution_error", "error":
			if breakpointMode && runningHubExecutionFailureNodeID(data) == workflow.FailureNodeID {
				// 节点 69 只证明工作流按预期停止；它的异常正文和 current_inputs
				// 不能证明节点 12 已完成，更不能作为结果 URL 来源。
				ready.WorkflowStopConfirmed = true
				if err := notifyResultEvidence(); err != nil {
					return ready, errors.Join(errors.New("RunningHub 断点终止证据保存失败"), err)
				}
				return ready, nil
			}
			return ready, runningHubExecutionFailure(data)
		case "execution_success", "execution_complete", "completed":
			return ready, errRunningHubNaturalCompletion
		}
	}
}

func runningHubExecutionFailure(data map[string]interface{}) error {
	nodeID := runningHubExecutionFailureNodeID(data)
	// WSS 的异常正文可能包含供应商路径和底层运行环境，只向任务页暴露可核对的节点 ID。
	if validRunningHubNodeID(nodeID) {
		return &runningHubWorkflowTerminalError{message: "RunningHub 工作流节点 " + nodeID + " 执行失败"}
	}
	return &runningHubWorkflowTerminalError{message: "RunningHub 工作流执行失败；上游未返回失败节点"}
}

func runningHubExecutionFailureNodeID(data map[string]interface{}) string {
	return firstNonEmptyString(
		runningHubScalarString(data["node_id"]),
		runningHubScalarString(data["nodeId"]),
		runningHubScalarString(data["node"]),
	)
}

// outputs 的失败对象可能把 failedReason 保存成对象或 JSON 字符串。恢复路径只能
// 接受结构化节点身份，不能从异常正文猜测，以免把其他节点的真实故障当成节费断点。
func runningHubFailureNodeID(payload map[string]interface{}) string {
	if nodeID := runningHubExecutionFailureNodeID(payload); validRunningHubNodeID(nodeID) {
		return nodeID
	}
	for _, key := range []string{"failedReason", "failed_reason", "nodeErrors", "node_errors", "error"} {
		if nodeID := runningHubFailureNodeIDValue(payload[key], 0); nodeID != "" {
			return nodeID
		}
	}
	return ""
}

func runningHubFailureNodeIDValue(value interface{}, depth int) string {
	if value == nil || depth > 8 {
		return ""
	}
	switch item := value.(type) {
	case map[string]interface{}:
		if nodeID := runningHubExecutionFailureNodeID(item); validRunningHubNodeID(nodeID) {
			return nodeID
		}
		for _, key := range []string{"failedReason", "failed_reason", "nodeErrors", "node_errors", "error", "errors", "data", "details"} {
			if nodeID := runningHubFailureNodeIDValue(item[key], depth+1); nodeID != "" {
				return nodeID
			}
		}
		for key, nested := range item {
			if runningHubNumericNodeID(key) {
				switch nested.(type) {
				case map[string]interface{}, []interface{}:
					return key
				}
			}
		}
	case []interface{}:
		for _, nested := range item {
			if nodeID := runningHubFailureNodeIDValue(nested, depth+1); nodeID != "" {
				return nodeID
			}
		}
	case string:
		text := strings.TrimSpace(item)
		if json.Valid([]byte(text)) {
			var nested interface{}
			if json.Unmarshal([]byte(text), &nested) == nil {
				return runningHubFailureNodeIDValue(nested, depth+1)
			}
		}
	}
	return ""
}

func runningHubNumericNodeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func unwrapRunningHubWSSEvent(event map[string]interface{}) map[string]interface{} {
	if nested, ok := event["event"].(map[string]interface{}); ok {
		return nested
	}
	if raw, ok := event["data"].(string); ok && json.Valid([]byte(raw)) {
		var nested map[string]interface{}
		if json.Unmarshal([]byte(raw), &nested) == nil && stringField(nested, "type") != "" {
			return nested
		}
	}
	return event
}

func runningHubNodeListContains(value interface{}, nodeID string) bool {
	items, ok := value.([]interface{})
	if !ok {
		return false
	}
	for _, item := range items {
		if runningHubScalarString(item) == nodeID {
			return true
		}
	}
	return false
}

func extractRunningHubEventResult(data map[string]interface{}) (string, string) {
	for _, key := range []string{"output", "outputs", "result"} {
		if value, exists := data[key]; exists {
			if resultURL, fileType := extractRunningHubMediaURL(value, 0); resultURL != "" {
				return resultURL, fileType
			}
		}
	}
	return extractRunningHubMediaURL(data, 0)
}

func extractRunningHubMediaURL(value interface{}, depth int) (string, string) {
	if depth > 8 || value == nil {
		return "", ""
	}
	switch item := value.(type) {
	case map[string]interface{}:
		fileType := firstNonEmptyString(runningHubScalarString(item["fileType"]), runningHubScalarString(item["format"]), runningHubScalarString(item["mimeType"]))
		for _, key := range []string{"fileUrl", "file_url", "video_url", "videoUrl", "cos_url", "filename", "url"} {
			if nested, exists := item[key]; exists {
				candidate := strings.TrimSpace(runningHubScalarString(nested))
				if isPublicMediaURL(candidate) {
					return candidate, fileType
				}
				// SaveVideo 的 video_url 在部分 ComfyUI 版本中是单元素数组。
				if candidate, nestedType := extractRunningHubMediaURL(nested, depth+1); candidate != "" {
					return candidate, firstNonEmptyString(nestedType, fileType)
				}
			}
		}
		for _, key := range []string{"videos", "images", "gifs", "files", "output", "outputs", "result", "data"} {
			if nested, exists := item[key]; exists {
				if candidate, nestedType := extractRunningHubMediaURL(nested, depth+1); candidate != "" {
					return candidate, firstNonEmptyString(nestedType, fileType)
				}
			}
		}
	case []interface{}:
		for _, nested := range item {
			if candidate, fileType := extractRunningHubMediaURL(nested, depth+1); candidate != "" {
				return candidate, fileType
			}
		}
	case string:
		if isPublicMediaURL(strings.TrimSpace(item)) {
			return strings.TrimSpace(item), ""
		}
	}
	return "", ""
}

func dialRunningHubWebSocket(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	if len(strings.TrimSpace(rawURL)) > 4096 {
		return nil, errors.New("RunningHub WSS 地址过长")
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.Scheme != "wss" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("RunningHub WSS 地址无效")
	}
	dnsCtx, cancelDNS := context.WithTimeout(ctx, outboundURLValidationTimeout)
	addresses, err := resolveOutboundHost(dnsCtx, parsed.Hostname())
	cancelDNS()
	if err != nil {
		return nil, err
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(addresses[0].String(), port))
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = rawConn.Close()
		}
	}()
	connection := net.Conn(rawConn)
	if parsed.Scheme == "wss" {
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: parsed.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		connection = tlsConn
	}
	originScheme := "http"
	if parsed.Scheme == "wss" {
		originScheme = "https"
	}
	config, err := websocket.NewConfig(parsed.String(), originScheme+"://"+parsed.Host)
	if err != nil {
		return nil, err
	}
	handshakeDeadline := time.Now().Add(15 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	_ = connection.SetDeadline(handshakeDeadline)
	ws, err := websocket.NewClient(config, connection)
	if err != nil {
		return nil, err
	}
	_ = ws.SetDeadline(time.Time{})
	ws.MaxPayloadBytes = 1 << 20
	success = true
	return ws, nil
}
