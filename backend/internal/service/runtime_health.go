package service

import (
	"context"
	"log"
	"time"
)

const workerHeartbeatMaxAge = 10 * time.Second
const runtimeHealthCacheDuration = 2 * time.Second

type RuntimeHealthCheck struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type RuntimeHealth struct {
	Status string                        `json:"status"`
	Checks map[string]RuntimeHealthCheck `json:"checks"`
}

// RuntimeHealth 只公开组件级状态，不回传数据库地址、Redis 地址或底层错误，
// 既让容器健康检查识别“进程还在但任务系统已失效”，也避免公开部署细节。
func (s *Service) RuntimeHealth(parent context.Context) RuntimeHealth {
	s.runtimeHealthMu.Lock()
	defer s.runtimeHealthMu.Unlock()
	if !s.runtimeHealthAt.IsZero() && time.Since(s.runtimeHealthAt) < runtimeHealthCacheDuration {
		return cloneRuntimeHealth(s.runtimeHealth)
	}

	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()

	result := RuntimeHealth{Status: "ok", Checks: map[string]RuntimeHealthCheck{}}
	setFailed := func(name string, message string) {
		result.Status = "degraded"
		result.Checks[name] = RuntimeHealthCheck{Status: "failed", Message: message}
	}

	if s.repo == nil {
		setFailed("database", "数据库未初始化")
	} else if err := s.repo.Ping(ctx); err != nil {
		setFailed("database", "数据库不可用")
	} else {
		result.Checks["database"] = RuntimeHealthCheck{Status: "ok"}
	}

	if err := s.ValidateRuntime(); err != nil {
		setFailed("coordination", "运行时协调配置无效")
	} else if err := s.coordinator.health(ctx); err != nil {
		setFailed("coordination", "Redis 或运行时协调器不可用")
	} else {
		result.Checks["coordination"] = RuntimeHealthCheck{Status: "ok"}
	}

	s.workerHealthMu.RLock()
	workerHeartbeat := s.workerHeartbeat
	workerError := s.workerError
	s.workerHealthMu.RUnlock()
	switch {
	case s.workerShutdownRequested():
		setFailed("worker", "Backend 正在排空在途请求")
	case workerHeartbeat.IsZero():
		setFailed("worker", "后台 Worker 尚未启动")
	case time.Since(workerHeartbeat) > workerHeartbeatMaxAge:
		setFailed("worker", "后台 Worker 调度心跳超时")
	case workerError != "":
		setFailed("worker", "后台 Worker 无法读取或领取任务")
	case s.hasWorkerSlotIssue():
		setFailed("worker", "后台 Worker 全局并发槽位续租异常")
	default:
		result.Checks["worker"] = RuntimeHealthCheck{Status: "ok"}
	}

	previousSignature := runtimeHealthSignature(s.runtimeHealth)
	s.runtimeHealth = cloneRuntimeHealth(result)
	s.runtimeHealthAt = time.Now()
	if signature := runtimeHealthSignature(result); signature != previousSignature {
		log.Printf("runtime health changed: %s", signature)
	}
	return result
}

func cloneRuntimeHealth(source RuntimeHealth) RuntimeHealth {
	checks := make(map[string]RuntimeHealthCheck, len(source.Checks))
	for name, check := range source.Checks {
		checks[name] = check
	}
	return RuntimeHealth{Status: source.Status, Checks: checks}
}

func runtimeHealthSignature(health RuntimeHealth) string {
	status := func(name string) string {
		if check, ok := health.Checks[name]; ok {
			return check.Status
		}
		return "unknown"
	}
	return "status=" + firstNonEmpty(health.Status, "unknown") + " database=" + status("database") + " coordination=" + status("coordination") + " worker=" + status("worker")
}
