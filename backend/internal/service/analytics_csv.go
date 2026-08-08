package service

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

func (s *Service) AdminAPICallLogsCSV(actor *model.User, query APICallLogQuery) ([]byte, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	filter := normalizeAnalyticsFilter(query.AnalyticsQuery)
	ids := uniqueNonEmpty(query.IDs)
	if len(query.IDs) > 0 && len(ids) == 0 {
		return nil, BadAuthRequest("请选择要导出的请求明细")
	}
	if len(ids) > 200 {
		return nil, BadAuthRequest("单次最多导出 200 条已选请求明细")
	}
	logs, err := s.repo.ExportAPICallLogs(repository.APICallLogFilter{AnalyticsFilter: filter, Keyword: query.Keyword, Status: query.Status, IDs: ids}, 10_000)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&buffer)
	if err := writeSafeCSVRecord(writer, []string{"时间", "用户ID", "渠道ID", "任务ID", "计费单ID", "能力", "请求阶段", "模型", "状态", "HTTP状态", "耗时毫秒", "渠道并发上限", "供应商任务ID", "错误码", "错误"}); err != nil {
		return nil, err
	}
	for _, log := range logs {
		if err := writeSafeCSVRecord(writer, []string{log.CreatedAt.UTC().Format(time.RFC3339), log.UserID, log.ChannelID, log.TaskID, log.BillingOrderID, log.Capability, log.RequestKind, log.Model, string(log.Status), strconv.Itoa(log.StatusCode), strconv.FormatInt(log.DurationMs, 10), strconv.Itoa(log.ConcurrencyLimit), log.ProviderRequestID, log.ErrorCode, log.Error}); err != nil {
			return nil, err
		}
	}
	if err := flushCSVWriter(writer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (s *Service) AdminAnalyticsCSV(actor *model.User, query AnalyticsQuery) ([]byte, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	filter := normalizeAnalyticsFilter(query)
	logs, err := s.repo.AnalyticsAPICallLogs(filter)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&buffer)
	if err := writeSafeCSVRecord(writer, []string{"时间", "用户ID", "渠道ID", "任务ID", "能力", "请求阶段", "模型", "状态", "状态码", "耗时毫秒", "输入Token", "输出Token", "缓存Token", "媒体数量", "视频秒数", "估算费用(微单位)", "币种", "错误类型"}); err != nil {
		return nil, err
	}
	for _, log := range logs {
		cost := ""
		if log.CostAvailable {
			cost = strconv.FormatInt(log.EstimatedCostMicros, 10)
		}
		inputTokens, outputTokens, cachedTokens := "", "", ""
		if log.UsageAvailable {
			inputTokens = strconv.FormatInt(log.InputTokens, 10)
			outputTokens = strconv.FormatInt(log.OutputTokens, 10)
			cachedTokens = strconv.FormatInt(log.CachedTokens, 10)
		}
		if err := writeSafeCSVRecord(writer, []string{log.CreatedAt.Format(time.RFC3339), log.UserID, log.ChannelID, log.TaskID, log.Capability, log.RequestKind, log.Model, string(log.Status), strconv.Itoa(log.StatusCode), strconv.FormatInt(log.DurationMs, 10), inputTokens, outputTokens, cachedTokens, strconv.Itoa(log.MediaCount), strconv.Itoa(log.VideoSeconds), cost, log.Currency, classifyAPICallError(log)}); err != nil {
			return nil, err
		}
	}
	if err := flushCSVWriter(writer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeSafeCSVRecord(writer *csv.Writer, record []string) error {
	for index := range record {
		record[index] = safeCSVCell(record[index])
	}
	return writer.Write(record)
}

func flushCSVWriter(writer *csv.Writer) error {
	writer.Flush()
	return writer.Error()
}

// CSV 会被管理员直接交给 Excel 等表格软件；外部字段必须强制为文本，不能让模型名或供应商错误变成公式。
func safeCSVCell(value string) string {
	if value == "" {
		return value
	}
	if !utf8.ValidString(value) {
		return "'" + strings.ToValidUTF8(value, "\uFFFD")
	}
	if isPlainNegativeNumber(value) {
		return value
	}
	leadingControl := false
	for _, character := range value {
		if character == ' ' {
			continue
		}
		if unicode.IsSpace(character) || unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			leadingControl = true
			continue
		}
		if leadingControl || strings.ContainsRune("=+-@", character) {
			return "'" + value
		}
		return value
	}
	if leadingControl {
		return "'" + value
	}
	return value
}

func isPlainNegativeNumber(value string) bool {
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	digitSeen := false
	dotSeen := false
	for _, character := range value[1:] {
		switch {
		case character >= '0' && character <= '9':
			digitSeen = true
		case character == '.' && !dotSeen:
			dotSeen = true
		default:
			return false
		}
	}
	return digitSeen
}
