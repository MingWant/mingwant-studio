package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
)

func TestTaskTimeoutMessageAlwaysPreservesBillingRisk(t *testing.T) {
	tests := []struct {
		taskType          string
		hasProviderTaskID bool
		want              string
	}{
		{taskType: "canvas_text", want: "供应商后台"},
		{taskType: "canvas_image", want: "图片生成等待超时"},
		{taskType: "canvas_audio", want: "音频生成等待超时"},
		{taskType: "canvas_video", want: "上游创建结果"},
		{taskType: "canvas_video", hasProviderTaskID: true, want: "手动查询任务"},
		{taskType: "unknown_task", want: "费用可能尚未确认"},
	}
	for _, test := range tests {
		message := taskTimeoutMessage(test.taskType, test.hasProviderTaskID)
		if !strings.Contains(message, test.want) || !strings.Contains(message, "请勿立即重试") {
			t.Fatalf("taskTimeoutMessage(%q, %t) = %q", test.taskType, test.hasProviderTaskID, message)
		}
	}
}

func TestTaskChannelSlotTimeoutMessageConfirmsProviderWasNotCalled(t *testing.T) {
	err := channelSlotError{scope: "channel-1", limit: 1, err: context.DeadlineExceeded}
	message := taskChannelSlotTimeoutMessage(err)
	if !strings.Contains(message, "没有发出新的供应商请求") || !strings.Contains(message, "等待已有任务完成后再重新提交") {
		t.Fatalf("taskChannelSlotTimeoutMessage() = %q", message)
	}
}

func TestProviderTaskRecoveryWaitErrorBlocksEarlySupplierQuery(t *testing.T) {
	nextPollAt := time.Now().Add(30 * time.Second)
	err := providerTaskRecoveryWaitError(&nextPollAt)
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.Status != http.StatusTooManyRequests || !strings.Contains(authErr.Message, "本次没有访问供应商") {
		t.Fatalf("providerTaskRecoveryWaitError() = %#v", err)
	}
	past := time.Now().Add(-time.Second)
	if err := providerTaskRecoveryWaitError(&past); err != nil {
		t.Fatalf("past recovery wait error = %v", err)
	}
}

func TestTaskSummaryExposesProviderQuerySchedule(t *testing.T) {
	nextPollAt := time.Now().Add(time.Minute)
	summary := taskSummaryForOutput(model.Task{ID: "task-1", Status: model.TaskStatusFailed, NextPollAt: &nextPollAt})
	if summary.NextPollAt == nil || !summary.NextPollAt.Equal(nextPollAt) {
		t.Fatalf("task summary next poll = %#v", summary.NextPollAt)
	}
}
