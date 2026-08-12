package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RunningHubWorkflowConfig 把画布的通用生成参数映射到 RHWorkspace 工作流节点。
// workflowId 由渠道模型的 modelKey 提供，避免在两处保存可能漂移的任务身份。
type RunningHubWorkflowConfig struct {
	ResultNodeID          string                       `json:"resultNodeId"`
	StopOnNodeIDs         []string                     `json:"stopOnNodeIds"`
	TerminationMode       string                       `json:"terminationMode"`
	FailureNodeID         string                       `json:"failureNodeId,omitempty"`
	FailureNodeField      string                       `json:"failureNodeField,omitempty"`
	FailureNodeValue      string                       `json:"failureNodeValue,omitempty"`
	WSSWaitSeconds        int                          `json:"wssWaitSeconds"`
	MonitorSilenceSeconds int                          `json:"monitorSilenceSeconds"`
	DimensionMultiple     int                          `json:"dimensionMultiple"`
	Parameters            []RunningHubParameterMapping `json:"parameters"`
	References            []RunningHubReferenceMapping `json:"references"`
}

type RunningHubParameterMapping struct {
	Label        string `json:"label"`
	Source       string `json:"source"`
	NodeID       string `json:"nodeId"`
	FieldName    string `json:"fieldName"`
	ValueType    string `json:"valueType"`
	DefaultValue string `json:"defaultValue,omitempty"`
	UserEditable bool   `json:"userEditable,omitempty"`
}

type RunningHubReferenceMapping struct {
	Kind      string `json:"kind"`
	Index     int    `json:"index"`
	NodeID    string `json:"nodeId"`
	FieldName string `json:"fieldName"`
	Required  bool   `json:"required,omitempty"`
}

const (
	runningHubTerminationBreakpoint = "breakpoint"
	runningHubTerminationCancel     = "cancel"
)

func defaultRunningHubWorkflowConfig() *RunningHubWorkflowConfig {
	return &RunningHubWorkflowConfig{
		ResultNodeID:          "12",
		StopOnNodeIDs:         []string{"48"},
		TerminationMode:       runningHubTerminationBreakpoint,
		FailureNodeID:         "69",
		FailureNodeField:      "width",
		FailureNodeValue:      "481",
		WSSWaitSeconds:        180,
		MonitorSilenceSeconds: 120,
		DimensionMultiple:     32,
		Parameters: []RunningHubParameterMapping{
			{Label: "提示词", Source: "prompt", NodeID: "7", FieldName: "prompt", ValueType: "string"},
			{Label: "视频时长", Source: "duration", NodeID: "6", FieldName: "duration_seconds", ValueType: "integer"},
			{Label: "画面宽度", Source: "width", NodeID: "6", FieldName: "width", ValueType: "integer"},
			{Label: "画面高度", Source: "height", NodeID: "6", FieldName: "height", ValueType: "integer"},
			{Label: "随机种子", Source: "seed", NodeID: "9", FieldName: "seed", ValueType: "integer", DefaultValue: "0", UserEditable: true},
			{Label: "帧率", Source: "fps", NodeID: "11", FieldName: "fps", ValueType: "integer", DefaultValue: "24", UserEditable: true},
		},
		References: []RunningHubReferenceMapping{
			{Kind: "image", Index: 0, NodeID: "16", FieldName: "image", Required: true},
			{Kind: "image", Index: 1, NodeID: "19", FieldName: "image", Required: true},
			{Kind: "image", Index: 2, NodeID: "22", FieldName: "image", Required: true},
			{Kind: "video", Index: 0, NodeID: "24", FieldName: "file"},
			{Kind: "audio", Index: 0, NodeID: "26", FieldName: "audio"},
		},
	}
}

func normalizeRunningHubWorkflowConfig(value RunningHubWorkflowConfig) RunningHubWorkflowConfig {
	fallback := defaultRunningHubWorkflowConfig()
	value.ResultNodeID = strings.TrimSpace(value.ResultNodeID)
	value.TerminationMode = strings.ToLower(strings.TrimSpace(value.TerminationMode))
	if value.TerminationMode == "" {
		value.TerminationMode = fallback.TerminationMode
	}
	value.FailureNodeID = strings.TrimSpace(value.FailureNodeID)
	if value.FailureNodeID == "" {
		value.FailureNodeID = fallback.FailureNodeID
	}
	value.FailureNodeField = strings.TrimSpace(value.FailureNodeField)
	if value.FailureNodeField == "" {
		value.FailureNodeField = fallback.FailureNodeField
	}
	value.FailureNodeValue = strings.TrimSpace(value.FailureNodeValue)
	if value.FailureNodeValue == "" {
		value.FailureNodeValue = fallback.FailureNodeValue
	}
	if value.DimensionMultiple <= 0 {
		value.DimensionMultiple = fallback.DimensionMultiple
	}
	value.StopOnNodeIDs = append([]string{}, normalizeCapabilityStringList(value.StopOnNodeIDs, strings.TrimSpace)...)
	if value.TerminationMode == runningHubTerminationBreakpoint {
		filtered := value.StopOnNodeIDs[:0]
		for _, nodeID := range value.StopOnNodeIDs {
			if nodeID != value.FailureNodeID {
				filtered = append(filtered, nodeID)
			}
		}
		value.StopOnNodeIDs = filtered
	}
	value.Parameters = append([]RunningHubParameterMapping{}, value.Parameters...)
	for index := range value.Parameters {
		item := &value.Parameters[index]
		item.Label = strings.TrimSpace(item.Label)
		item.Source = strings.ToLower(strings.TrimSpace(item.Source))
		item.NodeID = strings.TrimSpace(item.NodeID)
		item.FieldName = strings.TrimSpace(item.FieldName)
		item.ValueType = strings.ToLower(strings.TrimSpace(item.ValueType))
		item.DefaultValue = strings.TrimSpace(item.DefaultValue)
	}
	value.References = append([]RunningHubReferenceMapping{}, value.References...)
	for index := range value.References {
		item := &value.References[index]
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.NodeID = strings.TrimSpace(item.NodeID)
		item.FieldName = strings.TrimSpace(item.FieldName)
	}
	return value
}

func validateRunningHubWorkflowConfig(value *RunningHubWorkflowConfig) error {
	if value == nil {
		return BadAuthRequest("请配置 RunningHub 工作流节点映射")
	}
	if !validRunningHubNodeID(value.ResultNodeID) {
		return BadAuthRequest("RunningHub 结果节点 ID 无效")
	}
	if value.TerminationMode != runningHubTerminationBreakpoint && value.TerminationMode != runningHubTerminationCancel {
		return BadAuthRequest("RunningHub 终止方式无效")
	}
	if value.WSSWaitSeconds < 5 || value.WSSWaitSeconds > 600 || value.MonitorSilenceSeconds < 15 || value.MonitorSilenceSeconds > 600 {
		return BadAuthRequest("RunningHub WSS 等待时间必须为 5-600 秒，静默保护必须为 15-600 秒")
	}
	if value.DimensionMultiple < 1 || value.DimensionMultiple > 1024 {
		return BadAuthRequest("RunningHub 宽高对齐倍数必须为 1-1024")
	}
	if len(value.StopOnNodeIDs) > 20 || len(value.Parameters) == 0 || len(value.Parameters) > 100 || len(value.References) > 100 {
		return BadAuthRequest("RunningHub 节点映射数量超出限制")
	}
	stopSeen := make(map[string]struct{}, len(value.StopOnNodeIDs))
	for _, nodeID := range value.StopOnNodeIDs {
		if !validRunningHubNodeID(nodeID) || nodeID == value.ResultNodeID {
			return BadAuthRequest("RunningHub 抢停后继节点 ID 无效或与结果节点重复")
		}
		if _, exists := stopSeen[nodeID]; exists {
			return BadAuthRequest("RunningHub 抢停后继节点不能重复")
		}
		stopSeen[nodeID] = struct{}{}
	}
	if value.TerminationMode == runningHubTerminationBreakpoint {
		if !validRunningHubNodeID(value.FailureNodeID) || value.FailureNodeID == value.ResultNodeID {
			return BadAuthRequest("RunningHub 断点失败节点 ID 无效或与结果节点重复")
		}
		if _, exists := stopSeen[value.FailureNodeID]; exists {
			return BadAuthRequest("RunningHub 断点失败节点不能同时配置为取消兜底节点")
		}
		if !validRunningHubFieldName(value.FailureNodeField) || value.FailureNodeValue == "" || utf8.RuneCountInString(value.FailureNodeValue) > 1_000 {
			return BadAuthRequest("RunningHub 断点失败字段或字段值无效")
		}
	}
	allowedSources := map[string]bool{"prompt": true, "duration": true, "width": true, "height": true, "resolution": true, "generate_audio": true, "watermark": true, "seed": true, "fps": true, "custom": true}
	allowedValueTypes := map[string]bool{"string": true, "integer": true, "number": true, "boolean": true}
	fieldSeen := make(map[string]string, len(value.Parameters)+len(value.References))
	if value.TerminationMode == runningHubTerminationBreakpoint {
		fieldSeen[runningHubMappingKey(value.FailureNodeID, value.FailureNodeField)] = "failure"
	}
	hasPrompt := false
	for _, item := range value.Parameters {
		if utf8.RuneCountInString(item.Label) < 1 || utf8.RuneCountInString(item.Label) > 80 || !allowedSources[item.Source] || !allowedValueTypes[item.ValueType] || !validRunningHubNodeID(item.NodeID) || !validRunningHubFieldName(item.FieldName) || utf8.RuneCountInString(item.DefaultValue) > 1_000 {
			return BadAuthRequest("RunningHub 参数映射包含无效的名称、来源、类型或节点字段")
		}
		key := runningHubMappingKey(item.NodeID, item.FieldName)
		if _, exists := fieldSeen[key]; exists {
			return BadAuthRequest("RunningHub 同一节点字段只能配置一次")
		}
		fieldSeen[key] = "parameter"
		hasPrompt = hasPrompt || item.Source == "prompt"
		if item.DefaultValue != "" {
			if _, err := runningHubTypedFieldValue(item.DefaultValue, item.ValueType); err != nil {
				return BadAuthRequest("RunningHub 参数“" + item.Label + "”的默认值与字段类型不匹配")
			}
		}
	}
	if !hasPrompt {
		return BadAuthRequest("RunningHub 工作流至少需要一个提示词参数映射")
	}
	referenceSeen := make(map[string]struct{}, len(value.References))
	for _, item := range value.References {
		if (item.Kind != "image" && item.Kind != "video" && item.Kind != "audio") || item.Index < 0 || item.Index > 99 || !validRunningHubNodeID(item.NodeID) || !validRunningHubFieldName(item.FieldName) {
			return BadAuthRequest("RunningHub 素材映射包含无效的类型、序号或节点字段")
		}
		fieldKey := runningHubMappingKey(item.NodeID, item.FieldName)
		if _, exists := fieldSeen[fieldKey]; exists {
			return BadAuthRequest("RunningHub 参数和素材不能写入同一节点字段")
		}
		fieldSeen[fieldKey] = "reference"
		referenceKey := fmt.Sprintf("%s:%d", item.Kind, item.Index)
		if _, exists := referenceSeen[referenceKey]; exists {
			return BadAuthRequest("RunningHub 同一种素材的连接序号不能重复")
		}
		referenceSeen[referenceKey] = struct{}{}
	}
	return nil
}

func validRunningHubNodeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validRunningHubFieldName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 120 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func runningHubMappingKey(nodeID string, fieldName string) string {
	return strings.TrimSpace(nodeID) + ":" + strings.TrimSpace(fieldName)
}

func runningHubTypedFieldValue(value string, valueType string) (any, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(valueType)) {
	case "string":
		return value, nil
	case "integer":
		return strconv.ParseInt(value, 10, 64)
	case "number":
		return strconv.ParseFloat(value, 64)
	case "boolean":
		return strconv.ParseBool(value)
	default:
		return nil, errors.New("unsupported RunningHub field type")
	}
}

func validateRunningHubVideoTask(profile *VideoCapabilityConfig, input canvasGenerationInput) error {
	if profile == nil || profile.RunningHub == nil {
		return BadAuthRequest("RunningHub 工作流节点映射尚未配置")
	}
	if strings.TrimSpace(input.Config.ChannelID) == "" && systemChannelIDFromBaseURL(input.Config.BaseURL) == "" {
		return BadAuthRequest("RunningHub RHWorkspace 只能通过系统渠道调用")
	}
	if !validRunningHubWorkflowID(input.Config.Model) {
		return BadAuthRequest("RunningHub 模型标识必须填写 5-40 位数字的 RHWorkspace workflowId")
	}
	width, height, validSize := parseRunningHubVideoDimensions(input.Config.Size)
	if !validSize || width > 16_384 || height > 16_384 {
		return BadAuthRequest("RunningHub 工作流画面尺寸必须使用有效的宽×高像素值；本次未调用供应商")
	}
	multiple := profile.RunningHub.DimensionMultiple
	if multiple < 1 {
		return BadAuthRequest("RunningHub 工作流宽高对齐配置无效；本次未调用供应商")
	}
	if width%multiple != 0 || height%multiple != 0 {
		return BadAuthRequest(fmt.Sprintf("RunningHub 工作流要求宽高按 %d 对齐，当前为 %d×%d；本次未调用供应商", multiple, width, height))
	}
	counts := map[string]int{"image": len(input.ReferenceImages), "video": len(input.ReferenceVideos), "audio": len(input.ReferenceAudios)}
	mapped := make(map[string]struct{}, len(profile.RunningHub.References))
	for _, item := range profile.RunningHub.References {
		key := fmt.Sprintf("%s:%d", item.Kind, item.Index)
		mapped[key] = struct{}{}
		if item.Required && item.Index >= counts[item.Kind] {
			return BadAuthRequest(fmt.Sprintf("RunningHub 工作流缺少必需的第 %d 个%s参考素材", item.Index+1, runningHubReferenceKindLabel(item.Kind)))
		}
	}
	for kind, count := range counts {
		for index := 0; index < count; index++ {
			if _, exists := mapped[fmt.Sprintf("%s:%d", kind, index)]; !exists {
				return BadAuthRequest(fmt.Sprintf("RunningHub 尚未配置第 %d 个%s参考素材对应的工作流节点", index+1, runningHubReferenceKindLabel(kind)))
			}
		}
	}
	editable := make(map[string]RunningHubParameterMapping)
	for _, item := range profile.RunningHub.Parameters {
		if item.UserEditable {
			editable[runningHubMappingKey(item.NodeID, item.FieldName)] = item
		}
	}
	if raw, exists := input.Metadata["runningHubParameters"]; exists && raw != nil {
		values, ok := raw.(map[string]interface{})
		if !ok {
			return BadAuthRequest("RunningHub 画布参数格式无效")
		}
		for key, value := range values {
			mapping, allowed := editable[strings.TrimSpace(key)]
			if !allowed {
				return BadAuthRequest("画布提交了未授权的 RunningHub 工作流参数")
			}
			text, primitive := runningHubCanvasParameterText(value)
			if !primitive {
				return BadAuthRequest("RunningHub 画布参数只能使用文本、数字或开关值")
			}
			if utf8.RuneCountInString(text) > 1_000 {
				return BadAuthRequest("RunningHub 画布参数值过长")
			}
			if text != "" {
				if _, err := runningHubTypedFieldValue(text, mapping.ValueType); err != nil {
					return BadAuthRequest("RunningHub 画布参数“" + mapping.Label + "”与字段类型不匹配")
				}
			}
		}
	}
	return nil
}

func parseRunningHubVideoDimensions(value string) (int, int, bool) {
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "×", "x")))
	parts := strings.Split(normalized, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func runningHubCanvasParameterText(value any) (string, bool) {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item), true
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64), true
	case int:
		return strconv.Itoa(item), true
	case int64:
		return strconv.FormatInt(item, 10), true
	case bool:
		return strconv.FormatBool(item), true
	default:
		return "", false
	}
}

func validRunningHubWorkflowID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 5 || len(value) > 40 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func runningHubReferenceKindLabel(kind string) string {
	switch kind {
	case "image":
		return "图片"
	case "video":
		return "视频"
	case "audio":
		return "音频"
	default:
		return ""
	}
}
