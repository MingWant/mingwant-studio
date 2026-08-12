package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	runningHubUploadLimitBytes       = int64(30 << 20)
	runningHubJSONResponseLimit      = int64(2 << 20)
	runningHubProviderStateLimit     = 32 << 10
	runningHubOutputRecoveryAttempts = 10
	runningHubOutputRecoveryDelay    = 2 * time.Second
	runningHubBreakpointPollDelay    = 5 * time.Second
)

type runningHubNodeInfo struct {
	NodeID     string `json:"nodeId"`
	FieldName  string `json:"fieldName"`
	FieldValue string `json:"fieldValue"`
}

type runningHubEnvelope struct {
	Code json.RawMessage `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type runningHubApplicationError struct {
	Code    string
	Message string
}

func (e runningHubApplicationError) Error() string {
	return fmt.Sprintf("RunningHub 请求失败（%s）：%s", e.Code, e.Message)
}

type runningHubCreateData struct {
	TaskID     json.RawMessage `json:"taskId"`
	TaskStatus string          `json:"taskStatus"`
	NetWSSURL  string          `json:"netWssUrl"`
}

type runningHubUploadData struct {
	FileName string `json:"fileName"`
	FileType string `json:"fileType"`
}

type runningHubOutputState struct {
	WSSURL        string
	Outputs       []map[string]interface{}
	Status        string
	FailureNodeID string
}

// runningHubProviderCheckpoint 不保存带认证信息的 WSS URL，只保存恢复同一个
// taskId 所需的结果节点证据。这样重启后只能取消/读取原任务，不能重建付费任务。
type runningHubProviderCheckpoint struct {
	Version               int      `json:"version"`
	ResultNodeID          string   `json:"resultNodeId"`
	StopOnNodeIDs         []string `json:"stopOnNodeIds,omitempty"`
	TerminationMode       string   `json:"terminationMode,omitempty"`
	FailureNodeID         string   `json:"failureNodeId,omitempty"`
	WSSWaitSeconds        int      `json:"wssWaitSeconds,omitempty"`
	MonitorSilenceSeconds int      `json:"monitorSilenceSeconds,omitempty"`
	NodeCompleted         bool     `json:"nodeCompleted"`
	ResultURL             string   `json:"resultUrl,omitempty"`
	ResultFileType        string   `json:"resultFileType,omitempty"`
	WorkflowStopConfirmed bool     `json:"workflowStopConfirmed,omitempty"`
	CancelRequested       bool     `json:"cancelRequested"`
	CancelAccepted        bool     `json:"cancelAccepted,omitempty"`
	CancelConfirmed       bool     `json:"cancelConfirmed"`
	NaturalCompletion     bool     `json:"naturalCompletion,omitempty"`
}

type runningHubNodeReady struct {
	ResultURL            string
	ResultFileType        string
	ResultNodeCompleted   bool
	WorkflowStopConfirmed bool
}

func runRunningHubWorkflowVideoTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	capacityCtx, capacityGuard, err := ensureRunningHubWorkflowSlot(ctx, input.Config)
	if err != nil {
		return nil, err
	}
	ctx = capacityCtx
	if capacityGuard != nil {
		defer capacityGuard.Release()
	}
	taskID := resumedProviderRequestID(ctx)
	if taskID != "" {
		return recoverRunningHubWorkflowVideoTask(ctx, input, taskID)
	}
	workflow, err := runningHubWorkflowFromInput(input)
	if err != nil {
		return nil, runningHubCreatePreparationError(err)
	}

	nodeInfoList, err := buildRunningHubNodeInfoList(ctx, input, workflow)
	if err != nil {
		return nil, runningHubCreatePreparationError(err)
	}
	var created runningHubCreateData
	if err := runningHubPostJSON(withProviderRequestKind(ctx, "create"), input.Config, "/task/openapi/create", map[string]any{
		"apiKey":           input.Config.APIKey,
		"workflowId":       input.Config.Model,
		"randomSeed":       false,
		"nodeInfoList":      nodeInfoList,
		"retainSeconds":    0,
		"usePersonalQueue": false,
	}, &created); err != nil {
		return nil, err
	}
	taskID = rawJSONScalarString(created.TaskID)
	if taskID == "" {
		return nil, errors.New("RunningHub 创建响应没有返回 taskId；供应商请求可能已经执行，请勿自动重试")
	}
	ctx = withProviderRequestID(ctx, taskID)
	checkpoint := runningHubProviderCheckpoint{
		Version:               2,
		ResultNodeID:          workflow.ResultNodeID,
		StopOnNodeIDs:         append([]string(nil), workflow.StopOnNodeIDs...),
		TerminationMode:       workflow.TerminationMode,
		FailureNodeID:         workflow.FailureNodeID,
		WSSWaitSeconds:        workflow.WSSWaitSeconds,
		MonitorSilenceSeconds: workflow.MonitorSilenceSeconds,
	}
	if err := checkpointRunningHubProviderState(ctx, taskID, "rh_monitor", checkpoint); err != nil {
		cancelErr := cancelRunningHubTask(ctx, input.Config, taskID)
		return nil, errors.Join(errors.New("RunningHub taskId 已返回但本地检查点保存失败，已停止继续执行"), err, cancelErr)
	}
	if strings.EqualFold(strings.TrimSpace(created.TaskStatus), "failed") {
		return nil, &runningHubWorkflowTerminalError{message: "RunningHub 工作流在创建阶段被拒绝"}
	}

	initialState, err := waitForRunningHubWSS(ctx, input.Config, taskID, strings.TrimSpace(created.NetWSSURL), time.Duration(workflow.WSSWaitSeconds)*time.Second)
	if err != nil {
		if errors.Is(err, errRunningHubNaturalCompletion) {
			checkpoint.NaturalCompletion = true
			checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", checkpoint)
			return nil, errors.Join(err, checkpointErr)
		}
		if isRunningHubWorkflowTerminalError(err) {
			return nil, err
		}
		if workflow.TerminationMode == runningHubTerminationBreakpoint {
			return waitForRunningHubBreakpointResult(ctx, input.Config, taskID, workflow, &checkpoint)
		}
		cancelErr := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint)
		return nil, errors.Join(err, cancelErr)
	}
	if len(initialState.Outputs) > 0 {
		if outputURL, fileType := runningHubResultURL(initialState.Outputs, workflow.ResultNodeID); outputURL != "" {
			checkpoint.NodeCompleted = true
			checkpoint.ResultURL = outputURL
			checkpoint.ResultFileType = fileType
		}
		if workflow.TerminationMode == runningHubTerminationBreakpoint && runningHubFailedStatus(initialState.Status) {
			if !checkpoint.NodeCompleted {
				return nil, &runningHubWorkflowTerminalError{message: "RunningHub 工作流在结果节点完成前失败；本次没有可交付视频"}
			}
			if initialState.FailureNodeID != workflow.FailureNodeID {
				return nil, &runningHubWorkflowTerminalError{message: runningHubUnexpectedFailureMessage(initialState.FailureNodeID)}
			}
			// 只有结构化失败节点与创建时注入的断点一致，才把失败终态视为节费成功。
			checkpoint.WorkflowStopConfirmed = true
			if checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_breakpoint", checkpoint); checkpointErr != nil {
				return nil, errors.Join(errors.New("RunningHub 已在结果节点后终止，但本地证据保存失败；系统不会重新创建任务"), checkpointErr)
			}
			return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, workflow.ResultNodeID, checkpoint)
		}
		if runningHubFailedStatus(initialState.Status) {
			return nil, &runningHubWorkflowTerminalError{message: runningHubUnexpectedFailureMessage(initialState.FailureNodeID)}
		}
		if runningHubCompletedStatus(initialState.Status) {
			checkpoint.NaturalCompletion = true
			checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", checkpoint)
			return nil, errors.Join(errors.New("RunningHub 工作流在 WSS 监听建立前已经自然结束；本次不把自然结束当作抢停成功，请检查结果节点和工作流拓扑"), checkpointErr)
		}
		if strings.TrimSpace(initialState.WSSURL) == "" {
			if workflow.TerminationMode == runningHubTerminationBreakpoint {
				return waitForRunningHubBreakpointResult(ctx, input.Config, taskID, workflow, &checkpoint)
			}
			if cancelErr := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint); cancelErr != nil {
				return nil, errors.Join(errors.New("RunningHub 结果节点已完成，但未能保存取消已接受证据；不能把任务标记为成功"), cancelErr)
			}
			return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, workflow.ResultNodeID, checkpoint)
		}
	}
	ready, err := monitorRunningHubResultNode(ctx, initialState.WSSURL, workflow, func(observed runningHubNodeReady) error {
		checkpoint.NodeCompleted = checkpoint.NodeCompleted || observed.ResultNodeCompleted
		if strings.TrimSpace(observed.ResultURL) != "" {
			checkpoint.ResultURL = strings.TrimSpace(observed.ResultURL)
			checkpoint.ResultFileType = observed.ResultFileType
		}
		checkpoint.WorkflowStopConfirmed = checkpoint.WorkflowStopConfirmed || observed.WorkflowStopConfirmed
		pollStage := "rh_node_ready"
		if checkpoint.WorkflowStopConfirmed {
			pollStage = "rh_breakpoint"
		}
		return checkpointRunningHubProviderState(ctx, taskID, pollStage, checkpoint)
	}, func(nodeID string) error {
		return checkpointRunningHubProviderState(ctx, taskID, "rh_running:"+nodeID, checkpoint)
	})
	if err != nil {
		if isRunningHubWorkflowTerminalError(err) {
			return nil, err
		}
		if errors.Is(err, errRunningHubNaturalCompletion) {
			checkpoint.NaturalCompletion = true
			checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", checkpoint)
			return nil, errors.Join(err, checkpointErr)
		}
		if workflow.TerminationMode == runningHubTerminationBreakpoint {
			checkpoint.NodeCompleted = checkpoint.NodeCompleted || ready.ResultNodeCompleted
			checkpoint.ResultURL = firstNonEmptyString(ready.ResultURL, checkpoint.ResultURL)
			checkpoint.ResultFileType = firstNonEmptyString(ready.ResultFileType, checkpoint.ResultFileType)
			if checkpoint.NodeCompleted {
				if checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_breakpoint_wait", checkpoint); checkpointErr != nil {
					return nil, errors.Join(errors.New("RunningHub WSS 中断前的结果节点证据保存失败；系统不会重新创建任务"), err, checkpointErr)
				}
			}
			return waitForRunningHubBreakpointResult(ctx, input.Config, taskID, workflow, &checkpoint)
		}
		cancelErr := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint)
		return nil, errors.Join(err, cancelErr)
	}

	checkpoint.NodeCompleted = checkpoint.NodeCompleted || ready.ResultNodeCompleted
	checkpoint.ResultURL = firstNonEmptyString(ready.ResultURL, checkpoint.ResultURL)
	checkpoint.ResultFileType = firstNonEmptyString(ready.ResultFileType, checkpoint.ResultFileType)
	checkpoint.WorkflowStopConfirmed = checkpoint.WorkflowStopConfirmed || ready.WorkflowStopConfirmed
	if checkpoint.WorkflowStopConfirmed {
		if err := checkpointRunningHubProviderState(ctx, taskID, "rh_breakpoint", checkpoint); err != nil {
			return nil, errors.Join(errors.New("RunningHub 已在结果节点后按预期终止，但本地证据保存失败；系统不会重新创建任务"), err)
		}
		return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, workflow.ResultNodeID, checkpoint)
	}
	if err := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint); err != nil {
		return nil, errors.Join(errors.New("RunningHub 结果节点已完成，但未能保存取消已接受证据；不能把任务标记为成功"), err)
	}
	return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, workflow.ResultNodeID, checkpoint)
}

func runningHubFailedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func runningHubCompletedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success", "succeeded":
		return true
	default:
		return false
	}
}

func runningHubUnexpectedFailureMessage(nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	if validRunningHubNodeID(nodeID) {
		return "RunningHub 工作流节点 " + nodeID + " 执行失败；未观察到本系统配置的中途终止确认"
	}
	return "RunningHub 工作流执行失败，但未能确认失败节点或本系统配置的中途终止状态"
}

func recoverRunningHubWorkflowVideoTask(ctx context.Context, input canvasGenerationInput, taskID string) (map[string]interface{}, error) {
	checkpoint, err := loadRunningHubProviderCheckpoint(ctx, taskID)
	if err != nil {
		cancelErr := cancelRunningHubTask(ctx, input.Config, taskID)
		return nil, errors.Join(errors.New("RunningHub 原任务检查点损坏，系统只终止原 taskId，不会创建新任务"), err, cancelErr)
	}
	if checkpoint.NaturalCompletion {
		return nil, errors.New("RunningHub 原任务已经自然结束，不满足结果节点中途抢停约束；系统不会把该结果作为成功交付")
	}
	workflow, workflowErr := runningHubWorkflowFromCheckpoint(checkpoint)
	if workflowErr != nil {
		cancelErr := cancelRunningHubTask(ctx, input.Config, taskID)
		return nil, errors.Join(errors.New("RunningHub 原任务控制节点检查点无效，系统只终止原 taskId，不会创建新任务"), workflowErr, cancelErr)
	}
	if checkpoint.TerminationMode == runningHubTerminationBreakpoint {
		if checkpoint.WorkflowStopConfirmed || checkpoint.CancelAccepted {
			return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, checkpoint.ResultNodeID, checkpoint)
		}
		if checkpoint.CancelRequested {
			if cancelErr := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint); cancelErr != nil {
				return nil, errors.Join(errors.New("RunningHub 取消兜底恢复未能确认原任务已接受取消；系统不会重建任务"), cancelErr)
			}
			return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, checkpoint.ResultNodeID, checkpoint)
		}
		return waitForRunningHubBreakpointResult(ctx, input.Config, taskID, workflow, &checkpoint)
	}
	// Worker/WSS 一旦中断就失去了可靠的节点时序。成本优先边界要求先取消原任务，
	// 即使结果节点尚未被本地确认，也绝不能重新连接后继续等待完整工作流。
	if !checkpoint.CancelAccepted {
		if cancelErr := emergencyCancelRunningHubTask(ctx, input.Config, taskID, &checkpoint); cancelErr != nil {
			return nil, errors.Join(errors.New("RunningHub 监控恢复时未能确认原任务已接受取消；系统不会重建任务"), cancelErr)
		}
	}
	return downloadRunningHubCheckpointResult(ctx, input.Config, taskID, checkpoint.ResultNodeID, checkpoint)
}

func runningHubWorkflowFromCheckpoint(checkpoint runningHubProviderCheckpoint) (*RunningHubWorkflowConfig, error) {
	workflow := &RunningHubWorkflowConfig{
		ResultNodeID:          strings.TrimSpace(checkpoint.ResultNodeID),
		StopOnNodeIDs:         append([]string(nil), checkpoint.StopOnNodeIDs...),
		TerminationMode:       strings.ToLower(strings.TrimSpace(checkpoint.TerminationMode)),
		FailureNodeID:         strings.TrimSpace(checkpoint.FailureNodeID),
		WSSWaitSeconds:        checkpoint.WSSWaitSeconds,
		MonitorSilenceSeconds: checkpoint.MonitorSilenceSeconds,
	}
	if checkpoint.Version != 2 || !validRunningHubNodeID(workflow.ResultNodeID) {
		return nil, errors.New("RunningHub 恢复检查点版本或结果节点无效")
	}
	if workflow.TerminationMode == runningHubTerminationBreakpoint {
		if !validRunningHubNodeID(workflow.FailureNodeID) {
			return nil, errors.New("RunningHub 恢复检查点缺少预期失败节点")
		}
	} else if workflow.TerminationMode != runningHubTerminationCancel {
		return nil, errors.New("RunningHub 恢复检查点终止方式无效")
	}
	if workflow.MonitorSilenceSeconds < 15 || workflow.MonitorSilenceSeconds > 600 || workflow.WSSWaitSeconds < 5 || workflow.WSSWaitSeconds > 600 {
		return nil, errors.New("RunningHub 恢复检查点监控时间无效")
	}
	seen := make(map[string]struct{}, len(workflow.StopOnNodeIDs))
	for _, nodeID := range workflow.StopOnNodeIDs {
		if !validRunningHubNodeID(nodeID) || nodeID == workflow.ResultNodeID || nodeID == workflow.FailureNodeID {
			return nil, errors.New("RunningHub 恢复检查点取消兜底节点无效")
		}
		if _, exists := seen[nodeID]; exists {
			return nil, errors.New("RunningHub 恢复检查点取消兜底节点重复")
		}
		seen[nodeID] = struct{}{}
	}
	return workflow, nil
}

// 断点模式把“停止昂贵后继分支”交给工作流自身完成。WSS 断开后只查询原 taskId，
// 观察到失败终态且已有结果节点证据才交付；不会因为监控断线而重建或取消任务。
func waitForRunningHubBreakpointResult(ctx context.Context, config providerConfig, taskID string, workflow *RunningHubWorkflowConfig, checkpoint *runningHubProviderCheckpoint) (map[string]interface{}, error) {
	for {
		state, err := queryRunningHubOutputs(withProviderRequestID(ctx, taskID), config, taskID)
		if err != nil {
			if !isTransientProviderPollError(err) {
				return nil, errors.Join(errors.New("RunningHub 工作流断点终态查询失败；系统不会重建任务"), err)
			}
		} else {
			if resultURL, fileType := runningHubResultURL(state.Outputs, workflow.ResultNodeID); resultURL != "" {
				changed := !checkpoint.NodeCompleted || checkpoint.ResultURL != resultURL || checkpoint.ResultFileType != fileType
				checkpoint.NodeCompleted = true
				checkpoint.ResultURL = resultURL
				checkpoint.ResultFileType = fileType
				if changed {
					if checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_breakpoint_wait", *checkpoint); checkpointErr != nil {
						return nil, errors.Join(errors.New("RunningHub 结果节点证据保存失败；系统不会重新创建任务"), checkpointErr)
					}
				}
			}
			switch strings.ToLower(strings.TrimSpace(state.Status)) {
			case "failed", "cancelled", "canceled":
				if !checkpoint.NodeCompleted {
					return nil, &runningHubWorkflowTerminalError{message: "RunningHub 工作流在结果节点完成前失败；本次没有可交付视频"}
				}
				if !checkpoint.WorkflowStopConfirmed && state.FailureNodeID != workflow.FailureNodeID {
					return nil, &runningHubWorkflowTerminalError{message: runningHubUnexpectedFailureMessage(state.FailureNodeID)}
				}
				checkpoint.WorkflowStopConfirmed = true
				if checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_breakpoint", *checkpoint); checkpointErr != nil {
					return nil, errors.Join(errors.New("RunningHub 已在结果节点后终止，但本地证据保存失败；系统不会重新创建任务"), checkpointErr)
				}
				return downloadRunningHubCheckpointResult(ctx, config, taskID, workflow.ResultNodeID, *checkpoint)
			case "completed", "success", "succeeded":
				checkpoint.NaturalCompletion = true
				checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", *checkpoint)
				return nil, errors.Join(errors.New("RunningHub 断点节点未终止工作流，原任务已经自然结束；本次不作为节费成功交付"), checkpointErr)
			}
			if strings.TrimSpace(state.WSSURL) != "" {
				ready, monitorErr := monitorRunningHubResultNode(ctx, state.WSSURL, workflow, func(observed runningHubNodeReady) error {
					checkpoint.NodeCompleted = checkpoint.NodeCompleted || observed.ResultNodeCompleted
					if strings.TrimSpace(observed.ResultURL) != "" {
						checkpoint.ResultURL = strings.TrimSpace(observed.ResultURL)
						checkpoint.ResultFileType = observed.ResultFileType
					}
					checkpoint.WorkflowStopConfirmed = checkpoint.WorkflowStopConfirmed || observed.WorkflowStopConfirmed
					pollStage := "rh_node_ready"
					if checkpoint.WorkflowStopConfirmed {
						pollStage = "rh_breakpoint"
					}
					return checkpointRunningHubProviderState(ctx, taskID, pollStage, *checkpoint)
				}, func(nodeID string) error {
					return checkpointRunningHubProviderState(ctx, taskID, "rh_running:"+nodeID, *checkpoint)
				})
				checkpoint.NodeCompleted = checkpoint.NodeCompleted || ready.ResultNodeCompleted
				if strings.TrimSpace(ready.ResultURL) != "" {
					checkpoint.ResultURL = strings.TrimSpace(ready.ResultURL)
					checkpoint.ResultFileType = ready.ResultFileType
				}
				checkpoint.WorkflowStopConfirmed = checkpoint.WorkflowStopConfirmed || ready.WorkflowStopConfirmed
				if monitorErr == nil {
					if checkpoint.WorkflowStopConfirmed {
						return downloadRunningHubCheckpointResult(ctx, config, taskID, workflow.ResultNodeID, *checkpoint)
					}
					// 断点没有先触发而二采兜底节点已经开始，只在这一异常边界调用取消。
					cancelErr := emergencyCancelRunningHubTask(ctx, config, taskID, checkpoint)
					if cancelErr != nil {
						return nil, errors.Join(errors.New("RunningHub 断点未先于二采入口触发，取消兜底请求未被确认"), cancelErr)
					}
					result, recoveryErr := downloadRunningHubCheckpointResult(ctx, config, taskID, workflow.ResultNodeID, *checkpoint)
					if recoveryErr == nil {
						return result, nil
					}
					return nil, errors.Join(errors.New("RunningHub 断点未先于二采入口触发，已调用取消兜底"), cancelErr, recoveryErr)
				}
				if isRunningHubWorkflowTerminalError(monitorErr) {
					return nil, monitorErr
				}
				if errors.Is(monitorErr, errRunningHubNaturalCompletion) {
					checkpoint.NaturalCompletion = true
					checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", *checkpoint)
					return nil, errors.Join(monitorErr, checkpointErr)
				}
			}
		}
		timer := time.NewTimer(runningHubBreakpointPollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func emergencyCancelRunningHubTask(ctx context.Context, config providerConfig, taskID string, checkpoint *runningHubProviderCheckpoint) error {
	var requestCheckpointErr error
	if checkpoint != nil {
		checkpoint.CancelRequested = true
		requestCheckpointErr = checkpointRunningHubProviderState(ctx, taskID, "rh_cancel", *checkpoint)
	}
	cancelErr := cancelRunningHubTask(ctx, config, taskID)
	if cancelErr == nil && checkpoint != nil {
		checkpoint.CancelAccepted = true
		cancelErr = checkpointRunningHubProviderState(ctx, taskID, "rh_cancel_accepted", *checkpoint)
	}
	// code=0 只证明取消已被接受，不代表工作流已经停止；先把接受证据落盘，
	// 后续仍必须由 outputs 的 805/807 终态确认没有自然结束。
	return errors.Join(requestCheckpointErr, cancelErr)
}

func cancelRunningHubTask(parent context.Context, config providerConfig, taskID string) error {
	controlCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	defer cancel()
	controlCtx = withProviderRequestID(withRunningHubControlRequest(controlCtx, "cancel"), taskID)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var ignored interface{}
		lastErr = runningHubPostJSON(controlCtx, config, "/task/openapi/cancel", map[string]any{"apiKey": config.APIKey, "taskId": taskID}, &ignored)
		if lastErr == nil {
			return nil
		}
		if attempt == 2 || !isTransientProviderPollError(lastErr) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-controlCtx.Done():
			timer.Stop()
			return errors.Join(lastErr, controlCtx.Err())
		case <-timer.C:
		}
	}
	return lastErr
}

func withRunningHubControlRequest(ctx context.Context, requestKind string) context.Context {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok {
		return ctx
	}
	metadata.RequestKind = requestKind
	metadata.NonBillable = true
	metadata.AllowWhenCircuitOpen = true
	// 取消和读取原 taskId 不会创建新的付费工作流；即使原 Worker 租约已经丢失，
	// 也必须允许成本保护请求发出，不能被“新供应商调用”的发送检查点拦住。
	metadata.DispatchCheckpointRequired = false
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata)
}

func downloadRunningHubCheckpointResult(ctx context.Context, config providerConfig, taskID string, resultNodeID string, checkpoint runningHubProviderCheckpoint) (map[string]interface{}, error) {
	resultURL := strings.TrimSpace(checkpoint.ResultURL)
	fileType := checkpoint.ResultFileType
	cancelAccepted := checkpoint.CancelAccepted
	cancelConfirmed := checkpoint.CancelConfirmed
	workflowStopConfirmed := checkpoint.WorkflowStopConfirmed
	terminationConfirmed := cancelConfirmed || workflowStopConfirmed
	purgedAfterCancel := false
	var lastQueryErr error
	for attempt := 0; !(terminationConfirmed && resultURL != "") && attempt < runningHubOutputRecoveryAttempts; attempt++ {
		state, err := queryRunningHubOutputs(withProviderRequestID(ctx, taskID), config, taskID)
		if err != nil {
			lastQueryErr = err
			if cancelAccepted && runningHubApplicationErrorCode(err) == "807" {
				// 消费级 Key 的取消接口可能在 code=0 后立即清除 taskId；807 此时能
				// 证明原任务已不存在，但不能证明结果地址仍可恢复。
				cancelConfirmed = true
				terminationConfirmed = true
				purgedAfterCancel = true
				break
			}
		} else {
			if resultURL == "" {
				if recoveredURL, recoveredType := runningHubResultURL(state.Outputs, resultNodeID); recoveredURL != "" {
					resultURL = recoveredURL
					fileType = recoveredType
					checkpoint.NodeCompleted = true
				}
			}
			switch strings.ToLower(strings.TrimSpace(state.Status)) {
			case "failed", "cancelled", "canceled":
				if workflowStopConfirmed {
					terminationConfirmed = true
				} else if cancelAccepted {
					// RunningHub 以业务码 805 表示任务被中断或取消；只有观察到该终态，
					// 才能证明取消请求没有晚于工作流自然结束。
					cancelConfirmed = true
					terminationConfirmed = true
				}
			case "completed", "success", "succeeded":
				checkpoint.NaturalCompletion = true
				checkpoint.CancelConfirmed = false
				checkpointErr := checkpointRunningHubProviderState(ctx, taskID, "rh_natural", checkpoint)
				return nil, errors.Join(errors.New("RunningHub 工作流在节费终止生效前已经自然结束；本次结果不会作为成功交付"), checkpointErr)
			}
			if terminationConfirmed && (resultURL != "" || !checkpoint.NodeCompleted) {
				break
			}
		}
		if attempt+1 < runningHubOutputRecoveryAttempts {
			timer := time.NewTimer(runningHubOutputRecoveryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if !terminationConfirmed {
		if checkpoint.TerminationMode == runningHubTerminationBreakpoint {
			return nil, errors.Join(errors.New("RunningHub 断点终态确认超时；不能把任务标记为成功"), lastQueryErr)
		}
		return nil, errors.Join(errors.New("RunningHub 取消终态确认超时，尚未观察到原 taskId 的 805/807 终态；不能把任务标记为成功"), lastQueryErr)
	}
	checkpoint.CancelConfirmed = cancelConfirmed
	checkpoint.CancelAccepted = cancelAccepted
	checkpoint.WorkflowStopConfirmed = workflowStopConfirmed
	checkpoint.ResultURL = resultURL
	checkpoint.ResultFileType = fileType
	pollStage := "rh_cancelled"
	checkpointMessage := "RunningHub 已确认中途取消，但取消与结果证据保存失败；系统不会重新创建任务"
	if workflowStopConfirmed {
		pollStage = "rh_breakpoint"
		checkpointMessage = "RunningHub 已确认工作流断点终止，但结果证据保存失败；系统不会重新创建任务"
	}
	if checkpointErr := checkpointRunningHubProviderState(ctx, taskID, pollStage, checkpoint); checkpointErr != nil {
		return nil, errors.Join(errors.New(checkpointMessage), checkpointErr)
	}
	if !checkpoint.NodeCompleted {
		return nil, errors.New("RunningHub 原任务已终止，但没有结果节点完成证据；系统不会读取结果或重建任务")
	}
	if resultURL == "" {
		if purgedAfterCancel {
			return nil, errors.Join(errors.New("RunningHub 取消成功后立即清除了原 taskId，结果节点虽已完成但视频地址未被 WSS 捕获；系统不会重新创建任务"), lastQueryErr)
		}
		return nil, errors.Join(errors.New("RunningHub 原任务已终止，但结果节点视频地址等待超时；系统只会读取原 taskId，不会创建新任务"), lastQueryErr)
	}
	data, mimeType, err := getExternalBinary(withRunningHubControlRequest(withProviderRequestID(ctx, taskID), "download"), resultURL)
	if err != nil {
		return nil, errors.Join(errors.New("RunningHub 结果视频下载失败；结果节点检查点已保留，请勿重新生成"), err)
	}
	mimeType = normalizedMediaMimeType(firstNonEmptyString(mimeType, runningHubFileTypeMIME(fileType)), data)
	return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, nil
}

func runningHubApplicationErrorCode(err error) string {
	var applicationErr runningHubApplicationError
	if errors.As(err, &applicationErr) {
		return strings.TrimSpace(applicationErr.Code)
	}
	return ""
}

func queryRunningHubOutputs(ctx context.Context, config providerConfig, taskID string) (runningHubOutputState, error) {
	pollCtx := withRunningHubControlRequest(ctx, "poll")
	envelope, err := runningHubPostJSONEnvelope(pollCtx, config, "/task/openapi/outputs", map[string]any{"apiKey": config.APIKey, "taskId": taskID})
	if err != nil {
		return runningHubOutputState{}, err
	}
	state := runningHubOutputState{}
	switch code := rawJSONScalarString(envelope.Code); code {
	case "", "0":
	case "804":
		// RunningHub 用业务码 804 表示任务仍在运行，不是本次查询被拒绝。
		state.Status = "running"
	case "813":
		// 排队态同样需要继续等待原 taskId，绝不能因此重新创建付费任务。
		state.Status = "queued"
	case "805":
		// 中途取消后供应商可能用 805 表示终态，同时仍附带已经产出的节点结果。
		// 保留 failed 状态但继续解析 data，避免丢掉取消前已完成的结果节点。
		state.Status = "failed"
	default:
		return runningHubOutputState{}, runningHubEnvelopeError(envelope)
	}
	raw := envelope.Data
	if len(raw) == 0 || string(raw) == "null" {
		return state, nil
	}
	if json.Unmarshal(raw, &state.Outputs) == nil {
		state.Status = defaultString(state.Status, "completed")
		return state, nil
	}
	var payload map[string]interface{}
	if json.Unmarshal(raw, &payload) != nil {
		if state.Status != "" {
			return state, nil
		}
		return runningHubOutputState{}, errors.New("RunningHub outputs 返回格式无效")
	}
	state.WSSURL = strings.TrimSpace(firstNonEmptyString(stringField(payload, "netWssUrl"), stringField(payload, "net_wss_url")))
	if returnedStatus := strings.ToLower(strings.TrimSpace(firstNonEmptyString(stringField(payload, "taskStatus"), stringField(payload, "status")))); returnedStatus != "" {
		state.Status = returnedStatus
	}
	state.FailureNodeID = runningHubFailureNodeID(payload)
	for _, key := range []string{"outputs", "results", "files"} {
		if nested, exists := payload[key]; exists {
			encoded, _ := json.Marshal(nested)
			if json.Unmarshal(encoded, &state.Outputs) == nil && len(state.Outputs) > 0 {
				break
			}
		}
	}
	if state.Status == "" && len(state.Outputs) > 0 {
		state.Status = "completed"
	}
	return state, nil
}

func runningHubResultURL(outputs []map[string]interface{}, resultNodeID string) (string, string) {
	var anonymous []map[string]interface{}
	for _, output := range outputs {
		nodeID := runningHubScalarString(output["nodeId"])
		if nodeID == "" {
			anonymous = append(anonymous, output)
			continue
		}
		if nodeID != resultNodeID {
			continue
		}
		if resultURL, fileType := extractRunningHubMediaURL(output, 0); resultURL != "" {
			return resultURL, fileType
		}
	}
	if len(outputs) == 1 && len(anonymous) == 1 {
		return extractRunningHubMediaURL(anonymous[0], 0)
	}
	return "", ""
}

func checkpointRunningHubProviderState(ctx context.Context, taskID string, pollStage string, checkpoint runningHubProviderCheckpoint) error {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || strings.TrimSpace(metadata.TaskID) == "" || strings.TrimSpace(metadata.LeaseOwner) == "" {
		return errors.New("RunningHub 持久任务上下文缺失")
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if len(encoded) > runningHubProviderStateLimit {
		return errors.New("RunningHub 恢复检查点超过限制")
	}
	stage, progress := runningHubCheckpointProgress(pollStage, checkpoint)
	if err := metadata.Service.repo.CheckpointTaskProviderState(metadata.TaskID, metadata.LeaseOwner, taskID, pollStage, string(encoded), stage, progress); err != nil {
		return err
	}
	if strings.TrimSpace(metadata.BillingOrderID) != "" {
		if err := metadata.Service.repo.UpdateBillingProviderRequestID(metadata.BillingOrderID, taskID); err != nil {
			return err
		}
	}
	return nil
}

func runningHubCheckpointProgress(pollStage string, checkpoint runningHubProviderCheckpoint) (string, int) {
	normalized := strings.ToLower(strings.TrimSpace(pollStage))
	if strings.HasPrefix(normalized, "rh_running:") {
		nodeID := strings.TrimSpace(strings.TrimPrefix(normalized, "rh_running:"))
		if nodeID == strings.TrimSpace(checkpoint.ResultNodeID) {
			return "RunningHub 正在保存首段视频（节点 " + nodeID + "）", 60
		}
		if checkpoint.TerminationMode == runningHubTerminationBreakpoint && nodeID == strings.TrimSpace(checkpoint.FailureNodeID) {
			return "RunningHub 正在触发二采前断点（节点 " + nodeID + "）", 74
		}
		for _, stopNodeID := range checkpoint.StopOnNodeIDs {
			if nodeID == strings.TrimSpace(stopNodeID) {
				return "RunningHub 已到达取消兜底节点（节点 " + nodeID + "）", 72
			}
		}
		if validRunningHubNodeID(nodeID) {
			return "RunningHub 正在执行工作流节点 " + nodeID, 50
		}
		return "RunningHub 正在执行工作流", 50
	}
	switch normalized {
	case "rh_node_ready", "rh_breakpoint_wait":
		return "RunningHub 首段视频已生成，等待工作流断点", 70
	case "rh_breakpoint":
		return "RunningHub 已在二采前终止，正在读取视频", 78
	case "rh_cancel":
		return "RunningHub 已到达保护节点，正在终止工作流", 72
	case "rh_cancel_accepted":
		return "RunningHub 已接受取消，正在确认原任务终态", 76
	case "rh_cancelled":
		return "RunningHub 已中途终止，正在读取视频", 78
	case "rh_natural":
		return "RunningHub 工作流未按断点终止", 75
	default:
		return "RunningHub 已创建，等待首段视频", 45
	}
}

func loadRunningHubProviderCheckpoint(ctx context.Context, taskID string) (runningHubProviderCheckpoint, error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || strings.TrimSpace(metadata.TaskID) == "" {
		return runningHubProviderCheckpoint{}, errors.New("RunningHub 持久任务上下文缺失")
	}
	task, err := metadata.Service.repo.Task(metadata.TaskID)
	if err != nil {
		return runningHubProviderCheckpoint{}, err
	}
	if storedID := strings.TrimSpace(task.ProviderRequestID); storedID != "" && storedID != strings.TrimSpace(taskID) {
		return runningHubProviderCheckpoint{}, errors.New("RunningHub taskId 与任务检查点不一致")
	}
	checkpoint := runningHubProviderCheckpoint{Version: 2}
	if strings.TrimSpace(task.ProviderStateJSON) == "" {
		return checkpoint, nil
	}
	if len(task.ProviderStateJSON) > runningHubProviderStateLimit || json.Unmarshal([]byte(task.ProviderStateJSON), &checkpoint) != nil {
		return runningHubProviderCheckpoint{}, errors.New("RunningHub 恢复检查点无法解析")
	}
	if checkpoint.NodeCompleted && !validRunningHubNodeID(checkpoint.ResultNodeID) {
		return runningHubProviderCheckpoint{}, errors.New("RunningHub 恢复检查点缺少有效的结果节点")
	}
	return checkpoint, nil
}
