package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/gorm"
)

const (
	maxChannelModelKeyRunes          = 120
	maxChannelModelDisplayNameRunes  = 160
	maxChannelModelsPerChannel       = 2_000
	maxChannelModelCatalogEntries    = 5_000
	maxChannelModelCatalogResponseMB = 8
)

type ChannelModelRequest struct {
	ModelKey              string `json:"modelKey"`
	DisplayName           string `json:"displayName"`
	Capability            string `json:"capability"`
	Protocol              string `json:"protocol"`
	CapabilityConfig      *ModelCapabilityConfig `json:"capabilityConfig"`
	BillingMode           string `json:"billingMode"`
	UnitPriceMicrocredits int64  `json:"unitPriceMicrocredits"`
	PriceConfigured       bool   `json:"priceConfigured"`
	Enabled               *bool  `json:"enabled"`
}

// AdminChannelModelFetchResult 是管理员从上游拉目录后的汇总：models 为去重后的标识，added 为本次新建条数。
type AdminChannelModelFetchResult struct {
	Models []string `json:"models"`
	Added  int64    `json:"added"`
}

func (s *Service) EnsureSystemChannelModels() error {
	channels, err := s.repo.SystemChannels(true)
	if err != nil {
		return err
	}
	for index := range channels {
		channelID := channels[index].ID
		if err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
			channel, err := txRepo.AdminSystemChannelForUpdate(channelID)
			if err != nil {
				return err
			}
			items, err := txRepo.ChannelModels(channel.ID, true)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				hasHistory, err := txRepo.ChannelModelHistoryExists(channel.ID)
				if err != nil {
					return err
				}
				if !hasHistory {
					if err := syncInitialChannelModelsWithRepository(txRepo, channel, channelModelNames(*channel)); err != nil {
						return err
					}
				}
			}
			// 模型表是启停状态的事实源；启动时修复历史分步写入留下的兼容清单漂移。
			return syncChannelModelNamesWithRepository(txRepo, channel)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AdminChannelModels(actor *model.User, channelID string) ([]model.ChannelModel, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	channel, err := s.repo.AdminSystemChannel(channelID)
	if err != nil {
		return nil, err
	}
	items, err := s.ensureChannelModels(channelID, true)
	if err != nil {
		return nil, err
	}
	for index := range items {
		if !channelModelProbeMatches(*channel, items[index]) {
			clearChannelModelProbeState(&items[index])
		}
		capabilitySource := items[index]
		if capabilitySource.Protocol == "" {
			capabilitySource.Protocol = channel.InterfaceType
		}
		items[index].CapabilityConfig = capabilityConfigForModel(capabilitySource)
	}
	return items, nil
}

func (s *Service) FetchAdminChannelModels(ctx context.Context, actor *model.User, channelID string) (*AdminChannelModelFetchResult, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	channel, err := s.repo.AdminSystemChannel(channelID)
	if err != nil {
		return nil, err
	}
	// 使用服务端保存的渠道密钥请求上游，避免密钥为了拉目录再次经过浏览器。
	models, err := s.FetchChannelModels(ctx, actor, ChannelModelsRequest{BaseURL: channel.BaseURL, APIKey: channel.APIKey, APIFormat: channel.APIFormat})
	if err != nil {
		return nil, err
	}
	var added int64
	err = s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		current, err := txRepo.AdminSystemChannelForUpdate(channelID)
		if err != nil {
			return err
		}
		if strings.TrimRight(strings.TrimSpace(current.BaseURL), "/") != strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/") ||
			current.APIKey != channel.APIKey || current.APIFormat != channel.APIFormat || current.InterfaceType != channel.InterfaceType {
			return Conflict("拉取期间渠道地址、密钥或协议已被修改，本次旧目录未写入，请重新拉取")
		}
		// 只按当前未删除记录去重；重新拉取已删除模型时应生成新的待配置记录。
		existing, err := txRepo.ChannelModels(channelID, true)
		if err != nil {
			return err
		}
		known := make(map[string]struct{}, len(existing))
		for _, item := range existing {
			known[item.ModelKey] = struct{}{}
		}
		missing := make([]model.ChannelModel, 0, len(models))
		for _, name := range models {
			if _, ok := known[name]; ok {
				continue
			}
			// 自动发现不能绕过定价边界；新模型由管理员定价后再手动启用。
			item := model.ChannelModel{ID: newID(), ChannelID: channelID, ModelKey: name, DisplayName: name, Capability: capabilityForChannel(*current), Protocol: current.InterfaceType, BillingMode: "fixed_request", Enabled: false, PriceVersion: 1}
			if err := initializeChannelModelCapability(&item); err != nil {
				return err
			}
			missing = append(missing, item)
		}
		added, err = txRepo.CreateMissingChannelModels(missing)
		if err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "channel_models.fetch", "model_channel", channelID, "从上游拉取模型目录", map[string]any{"discoveredCount": len(models), "addedCount": added})
	})
	if err != nil {
		return nil, err
	}
	return &AdminChannelModelFetchResult{Models: models, Added: added}, nil
}

func (s *Service) SaveAdminChannelModel(actor *model.User, channelID string, id string, req ChannelModelRequest) (*model.ChannelModel, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	var saved *model.ChannelModel
	action := "channel_model.create"
	summary := "创建渠道模型与定价"
	if id != "" {
		action = "channel_model.update"
		summary = "更新渠道模型与定价"
	}
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		channel, err := txRepo.AdminSystemChannelForUpdate(channelID)
		if err != nil {
			return err
		}
		modelKey, validationErr := normalizeChannelModelKey(req.ModelKey)
		if validationErr != nil {
			return BadAuthRequest(validationErr.Error())
		}
		capability := normalizeCapability(req.Capability)
		if capability == "" {
			capability = capabilityForChannel(*channel)
		}
		if capability == "" {
			return BadAuthRequest("请选择模型能力")
		}
		protocol := model.ChannelInterfaceType(strings.TrimSpace(req.Protocol))
		if protocol == "" {
			protocol = channel.InterfaceType
		}
		if !validChannelInterfaceType(protocol) {
			return BadAuthRequest("请选择有效的模型请求协议")
		}
		if expected := capabilityForProtocol(protocol); expected != "" && expected != capability {
			return BadAuthRequest("模型能力与请求协议不匹配")
		}
		billingMode := strings.TrimSpace(req.BillingMode)
		if billingMode == "" {
			billingMode = "fixed_request"
		}
		if billingMode != "fixed_request" && billingMode != "per_second" {
			return BadAuthRequest("模型计费方式仅支持按次或按秒")
		}
		if billingMode == "per_second" && capability != "video" {
			return BadAuthRequest("只有视频模型可以按秒计费")
		}
		if req.UnitPriceMicrocredits < 0 {
			return BadAuthRequest("模型积分价格不能小于 0")
		}
		item := &model.ChannelModel{ID: newID(), ChannelID: channelID, Enabled: true, PriceVersion: 1}
		probeConfigChanged := false
		if id != "" {
			item, err = txRepo.ChannelModelByID(channelID, id)
			if err != nil {
				return err
			}
			probeConfigChanged = item.ModelKey != modelKey || item.Protocol != protocol || item.Capability != capability
			item.PriceVersion++
		}
		previousCapabilityJSON := item.CapabilityConfigJSON
		previousCapabilityVersion := item.CapabilityVersion
		previousProtocol := item.Protocol
		conflict, conflictErr := txRepo.ChannelModelByKeyIncludingDisabled(channelID, modelKey)
		if conflictErr != nil && !errors.Is(conflictErr, gorm.ErrRecordNotFound) {
			return conflictErr
		}
		if conflict != nil && conflict.ID != item.ID {
			return BadAuthRequest("该渠道已存在模型 " + modelKey + "，请直接编辑已有模型")
		}
		item.ModelKey = modelKey
		displayName, validationErr := normalizeChannelModelDisplayName(req.DisplayName, modelKey)
		if validationErr != nil {
			return BadAuthRequest(validationErr.Error())
		}
		item.DisplayName = displayName
		item.Capability = capability
		item.Protocol = protocol
		capabilityConfig, capabilityConfigErr := normalizeSavedChannelModelCapability(capability, string(protocol), req.CapabilityConfig, item, string(previousProtocol))
		if capabilityConfigErr != nil {
			return capabilityConfigErr
		}
		if capabilityConfig == nil {
			item.CapabilityConfigJSON = ""
			item.CapabilityVersion = 0
			item.CapabilityConfig = nil
		} else {
			encodedCapabilityConfig, marshalErr := json.Marshal(capabilityConfig)
			if marshalErr != nil {
				return errors.Join(BadAuthRequest("模型能力参数无法保存"), marshalErr)
			}
			item.CapabilityConfigJSON = string(encodedCapabilityConfig)
			item.CapabilityConfig = capabilityConfigMap(capabilityConfig)
			if previousCapabilityVersion <= 0 || previousCapabilityJSON != item.CapabilityConfigJSON {
				item.CapabilityVersion = previousCapabilityVersion + 1
			}
		}
		item.BillingMode = billingMode
		item.UnitPriceMicrocredits = req.UnitPriceMicrocredits
		item.PriceConfigured = req.PriceConfigured
		if probeConfigChanged {
			clearChannelModelProbeState(item)
		}
		if req.Enabled != nil {
			item.Enabled = *req.Enabled
		}
		if err := txRepo.SaveChannelModel(item); err != nil {
			return err
		}
		if err := syncChannelModelNamesWithRepository(txRepo, channel); err != nil {
			return err
		}
		if err := appendAdminAuditWithRepository(txRepo, actor, action, "channel_model", item.ID, summary, map[string]any{
			"channelId": channelID, "modelKey": item.ModelKey, "capability": item.Capability, "protocol": item.Protocol,
			"billingMode": item.BillingMode, "unitPriceMicrocredits": item.UnitPriceMicrocredits, "priceConfigured": item.PriceConfigured,
			"enabled": item.Enabled, "priceVersion": item.PriceVersion, "capabilityVersion": item.CapabilityVersion,
		}); err != nil {
			return err
		}
		saved = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func normalizeChannelModelKey(value string) (string, error) {
	if channelModelValueHasControl(value) {
		return "", errors.New("模型标识不能包含控制字符")
	}
	value = strings.TrimPrefix(strings.TrimSpace(value), "models/")
	if value == "" {
		return "", errors.New("请填写模型标识")
	}
	if utf8.RuneCountInString(value) > maxChannelModelKeyRunes {
		return "", errors.New("模型标识不能超过 120 个字符")
	}
	return value, nil
}

func normalizeChannelModelDisplayName(value string, fallback string) (string, error) {
	if channelModelValueHasControl(value) {
		return "", errors.New("模型显示名称不能包含控制字符")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if utf8.RuneCountInString(value) > maxChannelModelDisplayNameRunes {
		return "", errors.New("模型显示名称不能超过 160 个字符")
	}
	return value, nil
}

func channelModelValueHasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validatedChannelModelNames(values []string) ([]string, error) {
	if len(values) > maxChannelModelCatalogEntries {
		return nil, errors.New("单个渠道提交的模型条目不能超过 5000 条")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimPrefix(strings.TrimSpace(value), "models/") == "" && !channelModelValueHasControl(value) {
			continue
		}
		modelKey, err := normalizeChannelModelKey(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[modelKey]; exists {
			continue
		}
		if len(result) >= maxChannelModelsPerChannel {
			return nil, errors.New("单个渠道最多保存 2000 个模型")
		}
		seen[modelKey] = struct{}{}
		result = append(result, modelKey)
	}
	return result, nil
}

func (s *Service) DeleteAdminChannelModel(actor *model.User, channelID string, id string) error {
	if err := s.RequireAdmin(actor); err != nil {
		return err
	}
	err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
		channel, err := txRepo.AdminSystemChannelForUpdate(channelID)
		if err != nil {
			return err
		}
		item, err := txRepo.ChannelModelByID(channelID, id)
		if err != nil {
			return err
		}
		if err := txRepo.DeleteChannelModel(channelID, id, time.Now()); err != nil {
			return err
		}
		if err := syncChannelModelNamesWithRepository(txRepo, channel); err != nil {
			return err
		}
		return appendAdminAuditWithRepository(txRepo, actor, "channel_model.delete", "channel_model", item.ID, "删除渠道模型", map[string]any{"channelId": channelID, "modelKey": item.ModelKey})
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BadAuthRequest("系统渠道或渠道模型不存在，可能已经删除")
	}
	return err
}

func syncInitialChannelModelsWithRepository(repo *repository.Repository, channel *model.ModelChannel, names []string) error {
	existing, err := repo.ChannelModels(channel.ID, true)
	if err != nil {
		return err
	}
	byKey := make(map[string]*model.ChannelModel, len(existing))
	for index := range existing {
		byKey[existing[index].ModelKey] = &existing[index]
	}
	desired := make(map[string]bool, len(names))
	for _, name := range normalizedChannelModelNames(names) {
		desired[name] = true
		if item := byKey[name]; item != nil {
			if !item.Enabled {
				item.Enabled = true
				item.PriceVersion++
				if err := repo.SaveChannelModel(item); err != nil {
					return err
				}
			}
			continue
		}
		item := model.ChannelModel{ID: newID(), ChannelID: channel.ID, ModelKey: name, DisplayName: name, Capability: capabilityForChannel(*channel), Protocol: channel.InterfaceType, BillingMode: "fixed_request", Enabled: true, PriceVersion: 1}
		if err := initializeChannelModelCapability(&item); err != nil {
			return err
		}
		if err := repo.SaveChannelModel(&item); err != nil {
			return err
		}
	}
	for index := range existing {
		if existing[index].Enabled && !desired[existing[index].ModelKey] {
			existing[index].Enabled = false
			existing[index].PriceVersion++
			if err := repo.SaveChannelModel(&existing[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) ensureChannelModels(channelID string, includeDisabled bool) ([]model.ChannelModel, error) {
	allItems, err := s.repo.ChannelModels(channelID, true)
	if err != nil {
		return nil, err
	}
	if len(allItems) == 0 {
		if err := s.repo.WithTransaction(func(txRepo *repository.Repository) error {
			channel, err := txRepo.AdminSystemChannelForUpdate(channelID)
			if err != nil {
				return err
			}
			current, err := txRepo.ChannelModels(channelID, true)
			if err != nil || len(current) > 0 {
				return err
			}
			hasHistory, err := txRepo.ChannelModelHistoryExists(channelID)
			if err != nil {
				return err
			}
			if !hasHistory {
				if err := syncInitialChannelModelsWithRepository(txRepo, channel, channelModelNames(*channel)); err != nil {
					return err
				}
			}
			return syncChannelModelNamesWithRepository(txRepo, channel)
		}); err != nil {
			return nil, err
		}
	}
	return s.repo.ChannelModels(channelID, includeDisabled)
}

func syncChannelModelNamesWithRepository(repo *repository.Repository, channel *model.ModelChannel) error {
	items, err := repo.ChannelModels(channel.ID, false)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ModelKey)
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return err
	}
	if channel.ModelsJSON == string(encoded) {
		return nil
	}
	channel.ModelsJSON = string(encoded)
	return repo.Save(channel)
}

func (s *Service) SystemTextModelProtocol(channelID string, modelName string) (model.ChannelInterfaceType, error) {
	channel, err := s.repo.SystemChannel(strings.TrimSpace(channelID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", BadAuthRequest("系统渠道不存在或已停用")
		}
		return "", err
	}
	item, err := s.repo.ChannelModelByKey(channel.ID, strings.TrimPrefix(strings.TrimSpace(modelName), "models/"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", BadAuthRequest("当前系统渠道模型未配置或已停用")
		}
		return "", err
	}
	return resolveSystemTextModelProtocol(*channel, *item)
}

// 文本调用必须优先服从模型的文本协议；历史混合渠道可能把视频默认协议
// 留在模型行或渠道行，不能让它污染 Chat/Gemini 文本请求路径。
func resolveSystemTextModelProtocol(channel model.ModelChannel, item model.ChannelModel) (model.ChannelInterfaceType, error) {
	if !strings.EqualFold(strings.TrimSpace(item.Capability), "text") {
		return "", BadAuthRequest("当前系统模型不是可同步调用的文本模型")
	}
	protocol := item.Protocol
	if capabilityForProtocol(protocol) != "text" {
		protocol = ""
	}
	if protocol == "" && capabilityForProtocol(channel.InterfaceType) == "text" {
		protocol = channel.InterfaceType
	}
	if protocol == "" {
		if strings.EqualFold(strings.TrimSpace(channel.APIFormat), "gemini") {
			protocol = model.ChannelInterfaceGeminiContent
		} else {
			protocol = model.ChannelInterfaceChatCompletion
		}
	}
	if capabilityForProtocol(protocol) != "text" {
		return "", BadAuthRequest("当前系统模型缺少可用的文本请求协议")
	}
	return protocol, nil
}

func capabilityForChannel(channel model.ModelChannel) string {
	return capabilityForProtocol(channel.InterfaceType)
}

func capabilityForProtocol(protocol model.ChannelInterfaceType) string {
	switch protocol {
	case model.ChannelInterfaceOpenAIImage, model.ChannelInterfaceXAIImage:
		return "image"
	case model.ChannelInterfaceOpenAIAudio:
		return "audio"
	case model.ChannelInterfaceNewAPIVideo, model.ChannelInterfaceNewAPIChannel1, model.ChannelInterfaceNewAPIChannel2, model.ChannelInterfaceXAIVideo, model.ChannelInterfaceGeminiVeo:
		return "video"
	case model.ChannelInterfaceChatCompletion, model.ChannelInterfaceOpenAIResponse, model.ChannelInterfaceGeminiContent:
		return "text"
	default:
		return ""
	}
}

// 模型协议决定真实鉴权报文族；不能只沿用渠道默认值，否则混合模型配置会用错 Bearer 或 x-goog-api-key。
func apiFormatForProtocol(protocol model.ChannelInterfaceType, fallback string) string {
	switch protocol {
	case model.ChannelInterfaceGeminiContent, model.ChannelInterfaceGeminiVeo:
		return "gemini"
	case "":
		if strings.EqualFold(strings.TrimSpace(fallback), "gemini") {
			return "gemini"
		}
		return "openai"
	default:
		return "openai"
	}
}
