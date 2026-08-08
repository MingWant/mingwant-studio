package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"infinite-canvas/backend/internal/model"
)

type ChannelModelsRequest struct {
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	APIFormat string `json:"apiFormat"`
}

type channelModelsPayload struct {
	Data   []channelModelItem `json:"data"`
	Models []channelModelItem `json:"models"`
	Error  *providerError     `json:"error"`
	Code   *int               `json:"code"`
	Msg    string             `json:"msg"`
}

type channelModelItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) FetchChannelModels(ctx context.Context, actor *model.User, input ChannelModelsRequest) ([]string, error) {
	if actor == nil || strings.TrimSpace(actor.ID) == "" {
		return nil, Unauthorized("请先登录")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	apiKey := strings.TrimSpace(input.APIKey)
	if baseURL == "" {
		return nil, BadAuthRequest("请填写 Base URL")
	}
	if apiKey == "" {
		return nil, BadAuthRequest("请填写 API Key")
	}
	apiFormat := strings.ToLower(strings.TrimSpace(input.APIFormat))
	if apiFormat == "" {
		apiFormat = "openai"
	}
	if apiFormat != "openai" && apiFormat != "gemini" {
		return nil, BadAuthRequest("接口协议不支持拉取模型")
	}

	target := apiURL(baseURL, "/models")
	if apiFormat == "gemini" {
		lowerBaseURL := strings.ToLower(baseURL)
		if !strings.HasSuffix(lowerBaseURL, "/v1") && !strings.HasSuffix(lowerBaseURL, "/v1beta") {
			baseURL += "/v1beta"
		}
		target = baseURL + "/models"
	}
	if _, err := ValidateOutboundURLContext(ctx, target); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, BadAuthRequest("模型服务地址无效")
	}
	if apiFormat == "gemini" {
		request.Header.Set("x-goog-api-key", apiKey)
	} else {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// 只代理固定的模型目录 GET；用户密钥仅用于本次请求，不写入数据库或日志。
	data, _, err := doBinaryWithResponseLimit(request, maxChannelModelCatalogResponseMB<<20)
	if err != nil {
		return nil, channelModelsUpstreamError(err)
	}
	var payload channelModelsPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.Join(&AuthError{Status: http.StatusBadGateway, Message: "模型服务返回的不是有效 JSON"}, fmt.Errorf("decode model catalog response: %w", err))
	}
	if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
		return nil, &AuthError{Status: http.StatusBadGateway, Message: "模型服务返回错误，请检查渠道配置和供应商状态"}
	}
	if payload.Code != nil && *payload.Code != 0 {
		return nil, &AuthError{Status: http.StatusBadGateway, Message: "模型服务返回错误，请检查渠道配置和供应商状态"}
	}
	return channelModelNamesFromPayload(payload, apiFormat)
}

func channelModelNamesFromPayload(payload channelModelsPayload, apiFormat string) ([]string, error) {
	items := payload.Data
	if apiFormat == "gemini" {
		items = payload.Models
	}
	if len(payload.Data) > maxChannelModelCatalogEntries || len(payload.Models) > maxChannelModelCatalogEntries {
		return nil, errors.Join(&AuthError{Status: http.StatusBadGateway, Message: "模型服务返回的目录条目过多，已拒绝写入"}, errors.New("model catalog exceeds entry limit"))
	}
	seen := make(map[string]bool, len(items))
	models := make([]string, 0, len(items))
	for _, item := range items {
		rawName := firstNonEmpty(item.ID, item.Name)
		if strings.TrimPrefix(strings.TrimSpace(rawName), "models/") == "" && !channelModelValueHasControl(rawName) {
			continue
		}
		name, validationErr := normalizeChannelModelKey(rawName)
		if validationErr != nil {
			return nil, errors.Join(&AuthError{Status: http.StatusBadGateway, Message: "模型服务返回了不受支持的模型标识，已拒绝写入"}, validationErr)
		}
		if seen[name] {
			continue
		}
		if len(models) >= maxChannelModelsPerChannel {
			return nil, errors.Join(&AuthError{Status: http.StatusBadGateway, Message: "模型服务返回的有效模型过多，已拒绝写入"}, errors.New("model catalog exceeds unique model limit"))
		}
		seen[name] = true
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
}

func channelModelsUpstreamError(err error) error {
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return err
	}
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) {
		return errors.Join(&AuthError{Status: http.StatusBadGateway, Message: "连接模型服务失败，请检查地址、网络和供应商状态"}, err)
	}
	switch httpErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &AuthError{Status: http.StatusBadGateway, Message: "模型服务鉴权失败，请检查 API Key"}
	case http.StatusNotFound:
		return &AuthError{Status: http.StatusBadGateway, Message: "模型服务未提供 /models 接口"}
	case http.StatusTooManyRequests:
		return &AuthError{Status: http.StatusBadGateway, Message: "模型服务请求过于频繁或额度不足"}
	default:
		return &AuthError{Status: http.StatusBadGateway, Message: fmt.Sprintf("模型服务请求失败：HTTP %d", httpErr.StatusCode)}
	}
}
