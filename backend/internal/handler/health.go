package handler

import (
	"net/http"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func RegisterHealthRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.GET("/health", func(c *gin.Context) {
		// 健康状态会随数据库、Redis 和 Worker 实时变化，禁止 CDN 或反向代理缓存旧的 200。
		c.Header("Cache-Control", "no-store")
		health := svc.RuntimeHealth(c.Request.Context())
		statusCode := http.StatusOK
		responseCode := 0
		message := "ok"
		if health.Status != "ok" {
			statusCode = http.StatusServiceUnavailable
			responseCode = http.StatusServiceUnavailable
			message = "service unavailable"
		}
		c.JSON(statusCode, gin.H{"code": responseCode, "data": health, "msg": message})
	})
}
