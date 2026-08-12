package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	providerPollRetryStagePrefix = "poll_retry_"
	providerPollMaxRetries       = 5
)

type providerPollDeferredError struct {
	ProviderStatus string
	PollStage      string
	NextPollAt     time.Time
}

func (e *providerPollDeferredError) Error() string {
	status := strings.TrimSpace(e.ProviderStatus)
	if status == "" {
		return "上游任务仍在处理中"
	}
	return fmt.Sprintf("上游任务仍在处理中（%s）", status)
}

// 长任务每次只执行一次上游查询，再把下一次查询时间交回数据库调度，避免睡眠占住 Worker。
func deferProviderPoll(ctx context.Context, providerStatus string, pollStage string, delay time.Duration) error {
	if delay < time.Second {
		delay = time.Second
	}
	nextPollAt := time.Now().Add(delay)
	if deadline, ok := ctx.Deadline(); ok && !nextPollAt.Before(deadline) {
		return context.DeadlineExceeded
	}
	return &providerPollDeferredError{
		ProviderStatus: strings.TrimSpace(providerStatus),
		PollStage:      normalizedProviderPollStage(pollStage),
		NextPollAt:     nextPollAt,
	}
}

func normalizedProviderPollStage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "pending"
	}
	runes := []rune(value)
	if len(runes) > 32 {
		return string(runes[:32])
	}
	return value
}

func currentProviderPollStage(ctx context.Context) string {
	metadata, _ := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	return normalizedProviderPollStage(metadata.PollStage)
}

func providerPollRetryCount(ctx context.Context, prefix string) int {
	return providerPollRetryCountForStage(currentProviderPollStage(ctx), prefix)
}

func providerPollRetryCountForStage(stage string, prefix string) int {
	stage = normalizedProviderPollStage(stage)
	value := strings.TrimPrefix(stage, prefix)
	if value == stage {
		return 0
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

// 已取得上游任务 ID 后，临时查询故障只延后查询；不能回到创建路径，否则可能重复扣费。
func deferTransientProviderPoll(ctx context.Context, previousStage string, err error) error {
	if !isTransientProviderPollError(err) {
		return nil
	}
	// NewAPI Video Generations 已按协议执行三次一分钟重试，耗尽后不能再叠加通用重试预算。
	if strings.HasPrefix(normalizedProviderPollStage(previousStage), newAPIChannel2RetryStagePrefix) {
		return nil
	}
	retry := providerPollRetryCountForStage(previousStage, providerPollRetryStagePrefix) + 1
	if retry > providerPollMaxRetries {
		return nil
	}
	delay := 15 * time.Second * time.Duration(1<<(retry-1))
	if delay > 2*time.Minute {
		delay = 2 * time.Minute
	}
	return deferProviderPoll(
		ctx,
		fmt.Sprintf("上游查询暂时失败，准备第 %d/%d 次重试", retry, providerPollMaxRetries),
		providerPollRetryStagePrefix+strconv.Itoa(retry),
		delay,
	)
}

func isTransientProviderPollError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 404 || httpErr.StatusCode == 408 || httpErr.StatusCode == 425 || httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}
	var runningHubErr runningHubApplicationError
	if errors.As(err, &runningHubErr) && runningHubApplicationCodeUncertain(runningHubErr.Code) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "超时", "temporar", "connection reset", "connection refused", "unexpected eof", "broken pipe", "invalid character", "unexpected end of json", "熔断", "限流", "too many requests"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
