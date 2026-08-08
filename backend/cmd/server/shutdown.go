package main

import (
	"context"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

type requestDrainTracker struct {
	mu       sync.Mutex
	draining bool
	active   int
	done     chan struct{}
}

func newRequestDrainTracker() *requestDrainTracker {
	return &requestDrainTracker{done: make(chan struct{})}
}

// Middleware 在停机信号后拒绝新业务请求，但继续允许健康检查返回组件级 draining 状态。
func (tracker *requestDrainTracker) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/api/health" {
			c.Next()
			return
		}
		if !tracker.beginRequest() {
			c.Header("Cache-Control", "no-store")
			c.Header("Connection", "close")
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": http.StatusServiceUnavailable, "data": nil, "msg": "Backend 正在排空在途请求，本次未进入业务处理且没有调用供应商；请等待服务恢复后重试"})
			return
		}
		defer tracker.endRequest()
		c.Next()
	}
}

func (tracker *requestDrainTracker) beginRequest() bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.draining {
		return false
	}
	tracker.active++
	return true
}

func (tracker *requestDrainTracker) endRequest() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.active--
	if tracker.draining && tracker.active == 0 {
		close(tracker.done)
	}
}

func (tracker *requestDrainTracker) BeginDrain() <-chan struct{} {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.draining {
		return tracker.done
	}
	tracker.draining = true
	if tracker.active == 0 {
		close(tracker.done)
	}
	return tracker.done
}

func waitForRequestDrain(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
