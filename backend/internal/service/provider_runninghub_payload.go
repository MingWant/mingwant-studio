package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
)

func runningHubWorkflowFromInput(input canvasGenerationInput) (*RunningHubWorkflowConfig, error) {
	if input.Config.CapabilityConfig == nil || input.Config.CapabilityConfig.Video == nil || input.Config.CapabilityConfig.Video.RunningHub == nil {
		return nil, errors.New("RunningHub 系统模型缺少工作流节点映射")
	}
	workflow := normalizeRunningHubWorkflowConfig(*input.Config.CapabilityConfig.Video.RunningHub)
	if err := validateRunningHubWorkflowConfig(&workflow); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Config.ChannelID) == "" {
		return nil, errors.New("RunningHub RHWorkspace 只允许系统渠道调用")
	}
	return &workflow, nil
}

func runningHubCreatePreparationError(err error) error {
	message := taskFailureMessage(err)
	if billingFailureUncertain(err) || message == internalTaskFailureMessage {
		message = "RunningHub 参考素材或工作流节点参数准备失败"
	}
	message = strings.TrimRight(message, "。； ") + "；RunningHub 工作流创建请求尚未发出"
	return newProviderRequestNotSentError(message, err)
}

func buildRunningHubNodeInfoList(ctx context.Context, input canvasGenerationInput, workflow *RunningHubWorkflowConfig) ([]runningHubNodeInfo, error) {
	result := make([]runningHubNodeInfo, 0, len(workflow.Parameters)+len(workflow.References))
	for _, mapping := range workflow.Parameters {
		value, include, err := runningHubParameterValue(input, mapping)
		if err != nil {
			return nil, err
		}
		if include {
			result = append(result, runningHubNodeInfo{NodeID: mapping.NodeID, FieldName: mapping.FieldName, FieldValue: value})
		}
	}
	if workflow.TerminationMode == runningHubTerminationBreakpoint {
		// 该字段只在结果节点的轻量后继节点执行时生效；它必须通过提交校验，
		// 再在运行期确定性失败，才能阻止后续昂贵分支而不影响首段视频。
		result = append(result, runningHubNodeInfo{
			NodeID: workflow.FailureNodeID, FieldName: workflow.FailureNodeField, FieldValue: workflow.FailureNodeValue,
		})
	}
	for _, mapping := range workflow.References {
		media, exists := runningHubMappedMedia(input, mapping.Kind, mapping.Index)
		if !exists {
			if mapping.Required {
				return nil, fmt.Errorf("RunningHub 缺少必需的第 %d 个%s参考素材", mapping.Index+1, runningHubReferenceKindLabel(mapping.Kind))
			}
			continue
		}
		fileName, err := uploadRunningHubReference(ctx, input.Config, media)
		if err != nil {
			return nil, fmt.Errorf("RunningHub 第 %d 个%s参考素材上传失败：%w", mapping.Index+1, runningHubReferenceKindLabel(mapping.Kind), err)
		}
		result = append(result, runningHubNodeInfo{NodeID: mapping.NodeID, FieldName: mapping.FieldName, FieldValue: fileName})
	}
	return result, nil
}

func runningHubParameterValue(input canvasGenerationInput, mapping RunningHubParameterMapping) (string, bool, error) {
	key := runningHubMappingKey(mapping.NodeID, mapping.FieldName)
	if mapping.UserEditable {
		if value, exists := runningHubCanvasParameter(input.Metadata, key); exists && strings.TrimSpace(value) != "" {
			fieldValue, err := runningHubParameterFieldValue(value, mapping.ValueType)
			return fieldValue, true, err
		}
	}
	value := ""
	switch mapping.Source {
	case "prompt":
		value = input.Prompt
	case "duration":
		value = input.Config.VideoSeconds
	case "width":
		width, _ := runningHubVideoDimensions(input.Config.Size)
		value = strconv.Itoa(width)
	case "height":
		_, height := runningHubVideoDimensions(input.Config.Size)
		value = strconv.Itoa(height)
	case "resolution":
		value = normalizeVideoResolution(input.Config.VQuality)
	case "generate_audio":
		value = defaultString(input.Config.VideoGenerateAudio, "false")
	case "watermark":
		value = defaultString(input.Config.VideoWatermark, "false")
	case "seed", "fps", "custom":
		value = mapping.DefaultValue
	default:
		return "", false, errors.New("RunningHub 参数来源无效")
	}
	if strings.TrimSpace(value) == "" && mapping.Source == "custom" {
		return "", false, nil
	}
	fieldValue, err := runningHubParameterFieldValue(value, mapping.ValueType)
	if err != nil {
		return "", false, fmt.Errorf("RunningHub 参数“%s”与字段类型不匹配", mapping.Label)
	}
	return fieldValue, true, nil
}

// RunningHub 的 nodeInfoList 契约把 fieldValue 定义为字符串；数字和开关也要先
// 做类型校验再规范化成字符串，不能把 Go 数值直接编码成 JSON number/bool。
func runningHubParameterFieldValue(value string, valueType string) (string, error) {
	typed, err := runningHubTypedFieldValue(value, valueType)
	if err != nil {
		return "", err
	}
	switch item := typed.(type) {
	case string:
		return item, nil
	case int64:
		return strconv.FormatInt(item, 10), nil
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(item), nil
	default:
		return "", errors.New("unsupported RunningHub field value")
	}
}

func runningHubCanvasParameter(metadata map[string]interface{}, key string) (string, bool) {
	if metadata == nil {
		return "", false
	}
	values, ok := metadata["runningHubParameters"].(map[string]interface{})
	if !ok {
		return "", false
	}
	value, exists := values[key]
	if !exists || value == nil {
		return "", false
	}
	return runningHubCanvasParameterText(value)
}

func runningHubVideoDimensions(size string) (int, int) {
	if width, height, ok := parseRunningHubVideoDimensions(size); ok {
		return width, height
	}
	return 1280, 720
}

func runningHubMappedMedia(input canvasGenerationInput, kind string, index int) (providerMedia, bool) {
	if index < 0 {
		return providerMedia{}, false
	}
	switch kind {
	case "image":
		if index < len(input.ReferenceImages) {
			return input.ReferenceImages[index], true
		}
	case "video":
		if index < len(input.ReferenceVideos) {
			return input.ReferenceVideos[index], true
		}
	case "audio":
		if index < len(input.ReferenceAudios) {
			return input.ReferenceAudios[index], true
		}
	}
	return providerMedia{}, false
}

func uploadRunningHubReference(ctx context.Context, config providerConfig, media providerMedia) (string, error) {
	publicURL := strings.TrimSpace(media.URL)
	if (media.Bytes <= 0 || media.Bytes > runningHubUploadLimitBytes) && isPublicMediaURL(publicURL) {
		if _, err := ValidateOutboundURLContext(ctx, publicURL); err != nil {
			return "", err
		}
		// 公网素材大小未知或超过上传上限时，按 RunningHub 文档直接写入 Load 节点。
		return publicURL, nil
	}
	raw, mimeType, err := mediaBytes(media)
	if err != nil && isPublicMediaURL(publicURL) {
		raw, mimeType, err = getExternalBinary(withNonBillableProviderRequest(withProviderRequestKind(ctx, "reference_download")), publicURL)
	}
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > runningHubUploadLimitBytes {
		if isPublicMediaURL(publicURL) {
			if _, validateErr := ValidateOutboundURLContext(ctx, publicURL); validateErr != nil {
				return "", validateErr
			}
			return publicURL, nil
		}
		return "", errors.New("单个 RunningHub 上传素材不能超过 30MB；较大素材请使用公网直链")
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writeField(writer, "apiKey", config.APIKey); err != nil {
		return "", err
	}
	if err := writeField(writer, "fileType", "input"); err != nil {
		return "", err
	}
	filename := providerMediaFilename(media, mimeType)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", newProviderRequestNotSentError(providerRequestSerializationFailureMessage, err)
	}
	if _, err := part.Write(raw); err != nil {
		return "", newProviderRequestNotSentError(providerRequestSerializationFailureMessage, err)
	}
	if err := writer.Close(); err != nil {
		return "", newProviderRequestNotSentError(providerRequestSerializationFailureMessage, err)
	}
	var uploaded runningHubUploadData
	uploadCtx := withNonBillableProviderRequest(withProviderRequestKind(ctx, "upload"))
	if err := runningHubPostForm(uploadCtx, config, "/task/openapi/upload", writer.FormDataContentType(), body, &uploaded); err != nil {
		return "", err
	}
	if strings.TrimSpace(uploaded.FileName) == "" {
		return "", errors.New("RunningHub 上传成功但没有返回 fileName")
	}
	return strings.TrimSpace(uploaded.FileName), nil
}
