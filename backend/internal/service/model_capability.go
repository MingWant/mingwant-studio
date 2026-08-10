package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

// ModelCapabilityConfig 是渠道模型的能力事实源；视频参数不能只由前端选项推断，
// 否则旧画布或篡改后的任务输入仍可能把供应商不支持的请求送出并产生费用。
type ModelCapabilityConfig struct {
	Version int                    `json:"version"`
	Video   *VideoCapabilityConfig `json:"video,omitempty"`
}

type VideoCapabilityConfig struct {
	References        VideoReferenceConfig `json:"references"`
	Duration          VideoDurationConfig  `json:"duration"`
	Ratios            []string             `json:"ratios"`
	DefaultRatio      string               `json:"defaultRatio"`
	Resolutions       []string             `json:"resolutions"`
	DefaultResolution string               `json:"defaultResolution"`
	GenerateAudio     VideoBooleanConfig   `json:"generateAudio"`
	Watermark         VideoBooleanConfig   `json:"watermark"`
	Operations        []string             `json:"operations"`
	DefaultOperation  string               `json:"defaultOperation"`
}

type VideoReferenceConfig struct {
	PromptMaxChars   int   `json:"promptMaxChars"`
	MaxImages        int   `json:"maxImages"`
	MaxImageBytes    int64 `json:"maxImageBytes"`
	MaxVideos        int   `json:"maxVideos"`
	MaxVideoBytes    int64 `json:"maxVideoBytes"`
	MaxVideoDuration int   `json:"maxVideoDurationSeconds"`
	MaxAudios        int   `json:"maxAudios"`
	MaxAudioBytes    int64 `json:"maxAudioBytes"`
	MaxAudioDuration int   `json:"maxAudioDurationSeconds"`
}

type VideoDurationConfig struct {
	Selection string `json:"selection"`
	Min       int    `json:"min,omitempty"`
	Max       int    `json:"max,omitempty"`
	Step      int    `json:"step,omitempty"`
	Values    []int  `json:"values,omitempty"`
	Default   int    `json:"default"`
}

type VideoBooleanConfig struct {
	Supported bool `json:"supported"`
	Default   bool `json:"default"`
}

// DefaultModelCapabilityConfig 为协议提供可审计的初始模板。管理员可以在模型管理中
// 调整模板，但没有显式配置的历史视频模型仍按其协议模板校验，不会退回无界请求。
func DefaultModelCapabilityConfig(protocol string) *ModelCapabilityConfig {
	video := &VideoCapabilityConfig{
		References:        VideoReferenceConfig{PromptMaxChars: 10_000, MaxImages: 9, MaxImageBytes: 30 * 1024 * 1024},
		Duration:          VideoDurationConfig{Selection: "range", Min: 1, Max: 15, Step: 1, Default: 6},
		Ratios:            []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9", "adaptive"},
		DefaultRatio:      "16:9",
		Resolutions:       []string{"480p", "720p", "1080p", "2160p"},
		DefaultResolution: "720p",
		GenerateAudio:     VideoBooleanConfig{Supported: false, Default: false},
		Watermark:         VideoBooleanConfig{Supported: false, Default: false},
		Operations:        []string{"text_to_video", "image_to_video"},
		DefaultOperation:  "text_to_video",
	}
	switch model.ChannelInterfaceType(protocol) {
	case model.ChannelInterfaceGeminiVeo:
		video.References.MaxImages = 1
		video.Duration = VideoDurationConfig{Selection: "enum", Values: []int{4, 6, 8}, Default: 6}
		video.Ratios = []string{"16:9", "9:16", "1:1"}
		video.Resolutions = []string{"720p", "1080p"}
	case model.ChannelInterfaceNewAPIChannel1, model.ChannelInterfaceNewAPIChannel2:
		video.References.MaxVideos, video.References.MaxAudios = 3, 3
		video.References.MaxVideoBytes, video.References.MaxAudioBytes = 200*1024*1024, 15*1024*1024
		video.References.MaxVideoDuration, video.References.MaxAudioDuration = 15, 15
		video.GenerateAudio = VideoBooleanConfig{Supported: true, Default: true}
		video.Operations = []string{"text_to_video", "image_to_video", "audio_to_video", "extend"}
		video.DefaultOperation = "text_to_video"
	case model.ChannelInterfaceXAIVideo:
		video.GenerateAudio = VideoBooleanConfig{Supported: false, Default: false}
		video.References.MaxImages = 7
		video.Ratios = []string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"}
		video.Resolutions = []string{"480p", "720p", "1080p"}
		video.DefaultResolution = "480p"
		video.Operations = []string{"text_to_video", "image_to_video", "reference_to_video"}
	case model.ChannelInterfaceNewAPIVideo:
		video.GenerateAudio = VideoBooleanConfig{Supported: false, Default: false}
		video.References.MaxImages = 1
		video.Ratios = []string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"}
	}
	return &ModelCapabilityConfig{Version: 1, Video: video}
}

func NormalizeModelCapabilityConfig(capability string, protocol string, input *ModelCapabilityConfig) (*ModelCapabilityConfig, error) {
	if capability != "video" {
		return nil, nil
	}
	value := input
	if value == nil || value.Video == nil {
		value = DefaultModelCapabilityConfig(protocol)
	}
	video := *value.Video
	video.Duration.Selection = strings.ToLower(strings.TrimSpace(video.Duration.Selection))
	video.Duration.Values = append([]int(nil), video.Duration.Values...)
	video.Ratios = normalizeCapabilityStringList(video.Ratios, normalizeCapabilityRatio)
	video.DefaultRatio = normalizeCapabilityRatio(video.DefaultRatio)
	video.Resolutions = normalizeCapabilityStringList(video.Resolutions, normalizeResolution)
	video.DefaultResolution = normalizeResolution(video.DefaultResolution)
	video.Operations = normalizeCapabilityStringList(video.Operations, normalizeCapabilityOperation)
	video.DefaultOperation = normalizeCapabilityOperation(video.DefaultOperation)
	value = &ModelCapabilityConfig{Version: 1, Video: &video}
	if err := validateVideoCapabilityConfig(value.Video); err != nil {
		return nil, err
	}
	return value, nil
}

func DecodeModelCapabilityConfig(raw string, protocol string) (*ModelCapabilityConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultModelCapabilityConfig(protocol), nil
	}
	var value ModelCapabilityConfig
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	return NormalizeModelCapabilityConfig("video", protocol, &value)
}

func capabilityConfigMap(value *ModelCapabilityConfig) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func capabilityConfigForModel(item model.ChannelModel) map[string]any {
	if item.Capability != "video" {
		return nil
	}
	value, err := DecodeModelCapabilityConfig(item.CapabilityConfigJSON, string(item.Protocol))
	if err != nil {
		return nil
	}
	return capabilityConfigMap(value)
}

func normalizeSavedChannelModelCapability(capability string, protocol string, input *ModelCapabilityConfig, item *model.ChannelModel, previousProtocol string) (*ModelCapabilityConfig, error) {
	if capability != "video" {
		return nil, nil
	}
	if input == nil && item != nil && strings.TrimSpace(item.CapabilityConfigJSON) != "" && strings.TrimSpace(previousProtocol) == strings.TrimSpace(protocol) {
		stored, err := DecodeModelCapabilityConfig(item.CapabilityConfigJSON, protocol)
		if err != nil {
			return nil, BadAuthRequest("已有模型能力参数无效，请重新填写后保存")
		}
		input = stored
	}
	value, err := NormalizeModelCapabilityConfig(capability, protocol, input)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func initializeChannelModelCapability(item *model.ChannelModel) error {
	if item == nil || item.Capability != "video" {
		return nil
	}
	value, err := NormalizeModelCapabilityConfig(item.Capability, string(item.Protocol), nil)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	item.CapabilityConfigJSON = string(encoded)
	item.CapabilityVersion = 1
	item.CapabilityConfig = capabilityConfigMap(value)
	return nil
}

// ValidateTaskCapability 必须在 taskBillingOrder 前执行，确保参数错误不会先冻结积分，
// 更不能让旧画布绕过模型能力限制直接触发供应商请求。
func (s *Service) ValidateTaskCapability(userID string, input map[string]any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return BadAuthRequest("任务输入格式无效")
	}
	var taskInput canvasGenerationInput
	if err := json.Unmarshal(encoded, &taskInput); err != nil || taskInput.Mode != "video" {
		return nil
	}
	if err := s.hydrateTaskCapabilityMedia(userID, &taskInput); err != nil {
		return err
	}
	channelID := strings.TrimSpace(taskInput.Config.ChannelID)
	if channelID == "" {
		channelID = systemChannelIDFromBaseURL(taskInput.Config.BaseURL)
	}
	var profile *VideoCapabilityConfig
	if channelID == "" {
		protocol := model.ChannelInterfaceType(strings.TrimSpace(taskInput.Config.InterfaceType))
		if protocol != "" && capabilityForProtocol(protocol) != "video" {
			return BadAuthRequest("当前自定义渠道协议不是视频协议")
		}
		if taskInput.Config.CapabilityConfig == nil || taskInput.Config.CapabilityConfig.Video == nil {
			if capabilityForProtocol(protocol) != "video" {
				return nil
			}
			profile = DefaultModelCapabilityConfig(string(protocol)).Video
		} else {
			normalized, normalizeErr := NormalizeModelCapabilityConfig("video", string(protocol), taskInput.Config.CapabilityConfig)
			if normalizeErr != nil {
				return normalizeErr
			}
			profile = normalized.Video
		}
	} else {
		item, lookupErr := s.repo.ChannelModelByKey(channelID, strings.TrimPrefix(strings.TrimSpace(taskInput.Config.Model), "models/"))
		if lookupErr != nil {
			return BadAuthRequest("当前系统渠道模型未配置或已停用")
		}
		protocol := item.Protocol
		if protocol == "" {
			channel, channelErr := s.repo.SystemChannel(channelID)
			if channelErr != nil {
				return BadAuthRequest("当前系统渠道模型未配置或已停用")
			}
			protocol = channel.InterfaceType
		}
		if !strings.EqualFold(strings.TrimSpace(item.Capability), "video") || capabilityForProtocol(protocol) != "video" {
			return BadAuthRequest("当前系统模型不是可用的视频模型")
		}
		stored, decodeErr := DecodeModelCapabilityConfig(item.CapabilityConfigJSON, string(protocol))
		if decodeErr != nil || stored == nil || stored.Video == nil {
			return BadAuthRequest("当前视频模型能力参数无效，请联系管理员重新保存模型配置")
		}
		profile = stored.Video
	}
	return validateVideoTask(profile, taskInput)
}

// 资源的大小和时长以服务端资源表为准；不能让浏览器伪造小文件元数据绕过模型限制。
// 公网 URL 没有本地资源身份时仍允许沿用请求元数据，真正下载前的 URL 校验由供应商阶段处理。
func (s *Service) hydrateTaskCapabilityMedia(userID string, input *canvasGenerationInput) error {
	groups := [][]providerMedia{input.ReferenceImages, input.ReferenceVideos, input.ReferenceAudios}
	for _, group := range groups {
		for index := range group {
			if err := s.hydrateTaskCapabilityMediaItem(userID, &group[index]); err != nil {
				return err
			}
		}
	}
	if input.Mask != nil {
		return s.hydrateTaskCapabilityMediaItem(userID, input.Mask)
	}
	return nil
}

func (s *Service) hydrateTaskCapabilityMediaItem(userID string, media *providerMedia) error {
	resourceID := strings.TrimPrefix(strings.TrimSpace(media.StorageKey), "resource:")
	if resourceID == "" || resourceID == strings.TrimSpace(media.StorageKey) {
		return nil
	}
	resource, err := s.repo.ResourceForUser(userID, resourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BadAuthRequest("参考素材不存在或无权访问")
		}
		return err
	}
	if resource.Status != model.ResourceStatusReady {
		return BadAuthRequest("参考素材尚未上传完成")
	}
	media.Bytes = resource.Size
	media.DurationMs = resource.DurationMs
	media.MimeType = firstNonEmpty(media.MimeType, resource.MimeType)
	return nil
}

func validateVideoCapabilityConfig(value *VideoCapabilityConfig) error {
	if value == nil {
		return BadAuthRequest("请配置视频模型能力参数")
	}
	if value.References.PromptMaxChars < 1 || value.References.PromptMaxChars > 1_000_000 {
		return BadAuthRequest("提示词最大字符数必须在 1-1000000 之间")
	}
	for name, number := range map[string]int{"最大图片引用数": value.References.MaxImages, "最大视频引用数": value.References.MaxVideos, "最大音频引用数": value.References.MaxAudios} {
		if number < 0 || number > 100 {
			return BadAuthRequest(name + "必须在 0-100 之间")
		}
	}
	if value.References.MaxImageBytes < 0 || value.References.MaxVideoBytes < 0 || value.References.MaxAudioBytes < 0 || value.References.MaxImageBytes > 4<<30 || value.References.MaxVideoBytes > 4<<30 || value.References.MaxAudioBytes > 4<<30 || value.References.MaxVideoDuration < 0 || value.References.MaxVideoDuration > 3600 || value.References.MaxAudioDuration < 0 || value.References.MaxAudioDuration > 3600 {
		return BadAuthRequest("引用素材大小必须在 0-4GiB，时长必须在 0-3600 秒之间")
	}
	if err := validateVideoDuration(value.Duration); err != nil {
		return err
	}
	if err := validateCapabilityStringList("画面比例", value.Ratios, value.DefaultRatio); err != nil {
		return err
	}
	for _, ratio := range value.Ratios {
		normalized := strings.ToLower(strings.TrimSpace(ratio))
		if normalized != "adaptive" && normalized != "auto" && ratioValue(normalized) <= 0 {
			return BadAuthRequest("画面比例必须使用 W:H 或 adaptive")
		}
	}
	if err := validateCapabilityStringList("输出分辨率", value.Resolutions, value.DefaultResolution); err != nil {
		return err
	}
	for _, resolution := range value.Resolutions {
		normalized := normalizeResolution(resolution)
		if normalized == "p" {
			return BadAuthRequest("输出分辨率必须使用数字或 4k")
		}
		if strings.TrimSuffix(normalized, "p") != "2160" {
			if number, err := strconv.Atoi(strings.TrimSuffix(normalized, "p")); err != nil || number < 1 || number > 10000 {
				return BadAuthRequest("输出分辨率必须使用数字或 4k")
			}
		}
	}
	if err := validateCapabilityStringList("生成模式", value.Operations, value.DefaultOperation); err != nil {
		return err
	}
	if !value.GenerateAudio.Supported && value.GenerateAudio.Default || !value.Watermark.Supported && value.Watermark.Default {
		return BadAuthRequest("不支持的音频或水印能力不能设置为默认开启")
	}
	return nil
}

func validateCapabilityStringList(name string, values []string, defaultValue string) error {
	if len(values) == 0 || len(values) > 100 || strings.TrimSpace(defaultValue) == "" {
		return BadAuthRequest("请至少配置一个" + name + "，并选择默认值")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			return BadAuthRequest(name + "不能包含空值")
		}
		if _, exists := seen[normalized]; exists {
			return BadAuthRequest(name + "不能包含重复值")
		}
		seen[normalized] = struct{}{}
	}
	if !containsCapabilityString(values, defaultValue) {
		return BadAuthRequest("默认" + name + "必须属于已配置选项")
	}
	return nil
}

func normalizeCapabilityStringList(values []string, normalize func(string) string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, normalize(value))
	}
	return result
}

func normalizeCapabilityRatio(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "×", "x")))
	if value == "auto" {
		return "adaptive"
	}
	parts := strings.SplitN(value, "x", 2)
	if len(parts) == 2 {
		width, widthErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		height, heightErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if widthErr == nil && heightErr == nil && width > 0 && height > 0 {
			return strings.TrimSpace(parts[0]) + ":" + strings.TrimSpace(parts[1])
		}
	}
	return value
}

func normalizeCapabilityOperation(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateVideoDuration(value VideoDurationConfig) error {
	switch value.Selection {
	case "range":
		if value.Min < 1 || value.Max < value.Min || value.Max > 3600 || value.Step < 1 || value.Default < value.Min || value.Default > value.Max || (value.Default-value.Min)%value.Step != 0 {
			return BadAuthRequest("视频时长范围或默认值无效")
		}
	case "enum":
		if len(value.Values) == 0 || len(value.Values) > 100 {
			return BadAuthRequest("视频固定时长至少需要一个选项")
		}
		values := append([]int(nil), value.Values...)
		sort.Ints(values)
		for index, item := range values {
			if item < 1 || item > 3600 || (index > 0 && values[index-1] == item) {
				return BadAuthRequest("视频固定时长选项无效或重复")
			}
		}
		if !containsInt(values, value.Default) {
			return BadAuthRequest("视频默认时长必须属于固定时长选项")
		}
	default:
		return BadAuthRequest("视频时长选择方式仅支持范围或固定值")
	}
	return nil
}

func validateVideoTask(profile *VideoCapabilityConfig, input canvasGenerationInput) error {
	if err := validateVideoCapabilityConfig(profile); err != nil {
		return err
	}
	if utf8.RuneCountInString(input.Prompt) > profile.References.PromptMaxChars {
		return BadAuthRequest(fmt.Sprintf("提示词超过当前模型限制（最多 %d 字）", profile.References.PromptMaxChars))
	}
	if len(input.ReferenceImages) > profile.References.MaxImages || len(input.ReferenceVideos) > profile.References.MaxVideos || len(input.ReferenceAudios) > profile.References.MaxAudios {
		return BadAuthRequest("参考素材数量超过当前模型限制")
	}
	for _, media := range input.ReferenceImages {
		if profile.References.MaxImageBytes > 0 && media.Bytes > profile.References.MaxImageBytes {
			return BadAuthRequest("参考图片文件超过当前模型大小限制")
		}
	}
	for _, media := range input.ReferenceVideos {
		if profile.References.MaxVideoBytes > 0 && media.Bytes > profile.References.MaxVideoBytes {
			return BadAuthRequest("参考视频文件超过当前模型大小限制")
		}
		if profile.References.MaxVideoDuration > 0 && media.DurationMs > int64(profile.References.MaxVideoDuration)*1000 {
			return BadAuthRequest("参考视频时长超过当前模型限制")
		}
	}
	for _, media := range input.ReferenceAudios {
		if profile.References.MaxAudioBytes > 0 && media.Bytes > profile.References.MaxAudioBytes {
			return BadAuthRequest("参考音频文件超过当前模型大小限制")
		}
		if profile.References.MaxAudioDuration > 0 && media.DurationMs > int64(profile.References.MaxAudioDuration)*1000 {
			return BadAuthRequest("参考音频时长超过当前模型限制")
		}
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(input.Config.VideoSeconds))
	if err != nil || !videoDurationAllowed(profile.Duration, seconds) {
		return BadAuthRequest("视频时长不在当前模型支持范围内")
	}
	if input.Config.Size != "" && !videoRatioAllowed(profile.Ratios, input.Config.Size) {
		return BadAuthRequest("画面比例不在当前模型支持范围内")
	}
	if quality := strings.TrimSpace(input.Config.VQuality); quality != "" && !strings.EqualFold(quality, "auto") && !containsCapabilityString(profile.Resolutions, normalizeResolution(quality)) {
		return BadAuthRequest("输出分辨率不在当前模型支持范围内")
	}
	if strings.EqualFold(strings.TrimSpace(input.Config.VideoGenerateAudio), "true") && !profile.GenerateAudio.Supported {
		return BadAuthRequest("当前视频模型不支持生成音频")
	}
	if strings.EqualFold(strings.TrimSpace(input.Config.VideoWatermark), "true") && !profile.Watermark.Supported {
		return BadAuthRequest("当前视频模型不支持水印参数")
	}
	operation := metadataString(input.Metadata, "videoEditOperation")
	if operation == "" {
		operation = defaultVideoTaskOperation(profile, input)
	}
	if !containsCapabilityString(profile.Operations, operation) {
		return BadAuthRequest("当前视频模型不支持该生成模式")
	}
	if isXAIVideoConfig(input.Config) {
		switch operation {
		case "image_to_video":
			if len(input.ReferenceImages) != 1 {
				return BadAuthRequest("xAI 图生视频必须且只能提供 1 张起始图")
			}
			if metadataString(input.Metadata, "videoEndFrameNodeId") != "" {
				return BadAuthRequest("xAI 图生视频不支持指定尾帧")
			}
		case "reference_to_video":
			if len(input.ReferenceImages) < 1 || len(input.ReferenceImages) > 7 {
				return BadAuthRequest("xAI 多参考图实验模式必须提供 1-7 张参考图")
			}
			if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
				return BadAuthRequest("xAI 多参考图实验模式只接受图片参考")
			}
			if normalizeXAIVideoResolution(input.Config.VQuality) == "1080p" {
				return BadAuthRequest("xAI 多参考图实验模式最高支持 720P")
			}
		}
	}
	return nil
}

func defaultVideoTaskOperation(profile *VideoCapabilityConfig, input canvasGenerationInput) string {
	preferred := make([]string, 0, 4)
	if len(input.ReferenceAudios) > 0 && len(input.ReferenceImages) == 0 && len(input.ReferenceVideos) == 0 {
		preferred = append(preferred, "audio_to_video")
	}
	if len(input.ReferenceVideos) > 0 {
		preferred = append(preferred, "extend")
	}
	if len(input.ReferenceImages) > 0 {
		preferred = append(preferred, "image_to_video")
	}
	preferred = append(preferred, profile.DefaultOperation)
	for _, candidate := range preferred {
		if containsCapabilityString(profile.Operations, candidate) {
			return candidate
		}
	}
	return profile.DefaultOperation
}

func videoDurationAllowed(value VideoDurationConfig, seconds int) bool {
	if value.Selection == "enum" {
		return containsInt(value.Values, seconds)
	}
	return seconds >= value.Min && seconds <= value.Max && value.Step > 0 && (seconds-value.Min)%value.Step == 0
}

func videoRatioAllowed(options []string, value string) bool {
	value = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(value, "×", "x")))
	if value == "auto" {
		value = "adaptive"
	}
	if containsCapabilityString(options, value) {
		return true
	}
	parts := strings.Split(value, "x")
	if len(parts) != 2 {
		return false
	}
	width, widthErr := strconv.ParseFloat(parts[0], 64)
	height, heightErr := strconv.ParseFloat(parts[1], 64)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return false
	}
	actual := width / height
	for _, option := range options {
		candidate := ratioValue(option)
		if candidate > 0 && absFloat(candidate-actual)/candidate < 0.01 {
			return true
		}
	}
	return false
}

func ratioValue(value string) float64 {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0
	}
	width, widthErr := strconv.ParseFloat(parts[0], 64)
	height, heightErr := strconv.ParseFloat(parts[1], 64)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0
	}
	return width / height
}

func normalizeResolution(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, "p")
	if value == "4k" {
		return "2160p"
	}
	return value + "p"
}

func containsCapabilityString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
