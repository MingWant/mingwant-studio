package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

var errXAIStoredResultPending = errors.New("xAI Files 中尚未出现本次生成结果")

type xaiFileListResponse struct {
	Data []struct {
		ID        string `json:"id"`
		FileID    string `json:"file_id"`
		Filename  string `json:"filename"`
		CreatedAt int64  `json:"created_at"`
	} `json:"data"`
}

// 同步图片和异步视频在创建响应丢失后都只能按本次尝试的唯一文件名恢复，不能重新生成。
func xaiStoredFileID(ctx context.Context, config providerConfig, filename string) (string, error) {
	filter := url.QueryEscape(`name:"` + filename + `"`)
	var payload xaiFileListResponse
	if err := getJSON(withProviderRequestKind(ctx, "recovery"), config, "/files?limit=20&order=desc&sort_by=created_at&filter="+filter, &payload); err != nil {
		return "", err
	}
	fileID := ""
	latestCreatedAt := int64(-1)
	for _, item := range payload.Data {
		if strings.TrimSpace(item.Filename) != filename || item.CreatedAt < latestCreatedAt {
			continue
		}
		candidate := firstNonEmpty(strings.TrimSpace(item.ID), strings.TrimSpace(item.FileID))
		if candidate == "" {
			continue
		}
		fileID, latestCreatedAt = candidate, item.CreatedAt
	}
	if fileID == "" {
		return "", errXAIStoredResultPending
	}
	return fileID, nil
}

// Imagine 创建端点在除 408/499 外的明确 4xx 拒绝时不会产生结果；其余已建连错误可能已经执行。
// 恢复只查询本次尝试的 Files 副本，不会再次发送图片或视频创建请求。
func shouldRecoverXAIImagineCreate(err error) bool {
	if err == nil || providerRequestDefinitelyNotSent(err) {
		return false
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 408 || httpErr.StatusCode == 499 || (httpErr.StatusCode >= 500 && httpErr.StatusCode != 501)
	}
	return true
}
