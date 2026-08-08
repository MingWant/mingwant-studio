package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func RegisterChannelProbeRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.POST("/channel-probes", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.ChannelProbeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, errors.New("测活参数格式错误"))
			return
		}
		// 一次文本测活成功后，前端会自动追加一次无副作用工具诊断；两者
		// 共用窗口会让管理员连续测两个模型时把第四个请求误判为刷接口。
		// 按诊断类型分桶仍保留低频保护，固定探针不会变成免费任意代理。
		kind := strings.ToLower(strings.TrimSpace(req.Kind))
		if kind == "" {
			kind = "text"
		} else if kind != "text" && kind != "tool" {
			// 服务层仍会返回“测活类型无效”；这里使用固定桶名，避免把
			// 客户端任意长字符串直接拼进 Redis 协调键。
			kind = "invalid"
		}
		limit := 3
		if user.Role == model.UserRoleAdmin {
			// 管理员需要逐模型确认系统渠道；同一配置每类诊断 10 次/10
			// 分钟。限流按配置分桶，不会因为刚测完另一个模型就误伤当前模型。
			limit = 10
		}
		if !enforceRateLimit(c, channelProbeRateKey(user, req, kind), limit, 10*time.Minute) {
			return
		}
		probe, err := svc.CreateChannelProbe(c.Request.Context(), user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"probe": probe})
	})

	r.GET("/channel-probes/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		// 单次文本探针可等待 15 分钟；全局限流只拦异常刷库，不能让正常慢模型在约 4 分钟时把自己锁死。
		if !enforceRateLimit(c, "channel-probe-status:"+user.ID, 3600, time.Hour) {
			return
		}
		probe, err := svc.ChannelProbe(user, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		if !enforceRateLimit(c, "channel-probe-status:"+user.ID+":"+probe.ID, 360, time.Hour) {
			return
		}
		ok(c, gin.H{"probe": probe})
	})
}

func channelProbeRateKey(user *model.User, req service.ChannelProbeRequest, kind string) string {
	// 不把 Base URL、模型或任何凭据原文放进协调器键；配置摘要只用于把不同
	// 模型/端点的管理员诊断分开计数，密钥轮换也不会把密钥写入 Redis 或日志。
	identity := strings.Join([]string{
		strings.TrimSpace(req.ChannelID),
		strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		strings.ToLower(strings.TrimSpace(req.APIFormat)),
		strings.ToLower(strings.TrimSpace(req.InterfaceType)),
		strings.TrimPrefix(strings.TrimSpace(req.Model), "models/"),
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return "channel-probe-v3:" + kind + ":" + user.ID + ":" + hex.EncodeToString(digest[:])
}
