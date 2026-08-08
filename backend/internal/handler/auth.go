package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func RegisterAuthRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.GET("/auth/settings", func(c *gin.Context) {
		settings, err := svc.PublicAuthSettings()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, settings)
	})
	r.POST("/auth/register", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.RegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "register:"+c.ClientIP(), policy.Request.RegisterPerHour, time.Hour) {
			return
		}
		result, err := svc.Register(req)
		if err != nil {
			failService(c, err)
			return
		}
		setSessionCookie(c, result.Session, result.MaxAgeSecs)
		ok(c, gin.H{"user": result.User})
	})
	r.POST("/auth/email-code", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
		var req struct {
			Email string `json:"email"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "email-code:"+c.ClientIP(), policy.Request.EmailCodePerHour, time.Hour) {
			return
		}
		if !enforceRateLimit(c, "email-code-recipient:"+rateLimitSubjectHash(req.Email), 1, time.Minute) {
			return
		}
		if err := svc.SendRegistrationEmailCode(req.Email); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"sent": true})
	})
	r.POST("/auth/login", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "login-ip:"+c.ClientIP(), policy.Request.LoginIPPerTenMinutes, 10*time.Minute) {
			return
		}
		if !enforceRateLimit(c, "login-account:"+rateLimitSubjectHash(req.Username), policy.Request.LoginAccountPerTenMinutes, 10*time.Minute) {
			return
		}
		result, err := svc.Login(req)
		if err != nil {
			failService(c, err)
			return
		}
		setSessionCookie(c, result.Session, result.MaxAgeSecs)
		ok(c, gin.H{"user": result.User})
	})
	r.GET("/auth/linuxdo/start", func(c *gin.Context) {
		if !enforceRateLimit(c, "linuxdo-start:"+c.ClientIP(), 20, 10*time.Minute) {
			return
		}
		target, err := svc.BeginLinuxDOLogin(c.Query("next"))
		if err != nil {
			failService(c, err)
			return
		}
		c.Redirect(http.StatusFound, target)
	})
	r.GET("/auth/linuxdo/callback", linuxDOCallbackHandler(svc))
	r.POST("/auth/logout", func(c *gin.Context) {
		err := svc.Logout(sessionCookie(c))
		clearSessionCookie(c)
		if err != nil {
			failInternal(c, http.StatusInternalServerError, "本机登录 Cookie 已清除，但服务端会话撤销失败；请联系管理员检查数据库并撤销剩余会话", err)
			return
		}
		ok(c, gin.H{"ok": true})
	})
	r.POST("/auth/logout-others", func(c *gin.Context) {
		revoked, err := svc.RevokeOtherAuthSessions(sessionCookie(c))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"revoked": revoked})
	})
	r.GET("/auth/session", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			ok(c, gin.H{"user": nil})
			return
		}
		publicUser, err := svc.PublicAuthUser(user)
		if err != nil {
			failService(c, err)
			return
		}
		channels, err := svc.PublicSystemChannels()
		if err != nil {
			failService(c, err)
			return
		}
		limits, err := svc.PublicRuntimeLimits()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"user": publicUser, "systemChannels": channels, "runtimeLimits": limits})
	})
	r.GET("/channels/system", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		channels, err := svc.PublicSystemChannels()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"channels": channels})
	})
}

// 兼容已在 Linux.do OAuth 应用中登记的传统回调地址，处理逻辑与 /api/auth/linuxdo/callback 完全一致。
func RegisterOAuthCallbackRoutes(r gin.IRoutes, svc *service.Service) {
	r.GET("/oauth/linuxdo/callback", linuxDOCallbackHandler(svc))
}

func linuxDOCallbackHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enforceRateLimit(c, "linuxdo-callback:"+c.ClientIP(), 30, 10*time.Minute) {
			return
		}
		result, err := svc.CompleteLinuxDOLogin(c.Query("state"), c.Query("code"))
		if err != nil {
			message := publicServiceErrorMessage(c, err, "Linux.do 登录服务暂时不可用，请稍后再试")
			c.Redirect(http.StatusFound, "/login?oauth_error="+url.QueryEscape(message))
			return
		}
		setSessionCookie(c, result.Session.Session, result.Session.MaxAgeSecs)
		c.Redirect(http.StatusFound, result.Next)
	}
}

func RegisterAdminRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.GET("/admin/users", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		users, err := svc.AdminUsers(user, service.AdminListQuery{Keyword: c.Query("keyword"), Type: c.Query("role"), Status: c.Query("status"), Page: page, Limit: limit})
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, users)
	})
	r.GET("/admin/references", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		data, err := svc.AdminReferences(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, data)
	})
	r.POST("/admin/users/bulk-disable", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.BulkDisableUsersRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.BulkDisableUsers(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/users/:id/detail", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		result, err := svc.AdminUserDetail(user, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/users/:id/ledger", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		result, err := svc.AdminUserLedger(user, c.Param("id"), c.Query("type"), page, limit)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/users/:id/tasks", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		result, err := svc.AdminUserTasks(user, c.Param("id"), page, limit)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/users/:id/audit-events", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		result, err := svc.AdminUserAuditEvents(user, c.Param("id"), page, limit)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.PATCH("/admin/users/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.UpdateUserRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		updated, err := svc.UpdateUser(user, c.Param("id"), req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"user": updated})
	})
	r.DELETE("/admin/users/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if err := svc.DeleteUser(user, c.Param("id")); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})
	r.GET("/admin/channels", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		channels, err := svc.AdminSystemChannelPage(user, service.AdminListQuery{Keyword: c.Query("keyword"), Type: c.Query("interfaceType"), Status: c.Query("status"), Page: page, Limit: limit})
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, channels)
	})
	r.POST("/admin/channels", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAdminChannelRequestBytes)
		var req service.ChannelRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		channel, err := svc.CreateSystemChannel(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"channel": channel})
	})
	r.PATCH("/admin/channels/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAdminChannelRequestBytes)
		var req service.ChannelRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		channel, err := svc.UpdateSystemChannel(user, c.Param("id"), req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"channel": channel})
	})
	r.DELETE("/admin/channels/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if err := svc.DeleteSystemChannel(user, c.Param("id")); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})
	r.GET("/admin/storyboard-prompts", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		templates, variables, err := svc.AdminStoryboardPromptTemplates(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"templates": templates, "variables": variables})
	})
	r.POST("/admin/storyboard-prompts", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.StoryboardPromptTemplateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		template, err := svc.CreateStoryboardPromptTemplate(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"template": template})
	})
	r.PATCH("/admin/storyboard-prompts/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.StoryboardPromptTemplateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		template, err := svc.UpdateStoryboardPromptTemplate(user, c.Param("id"), req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"template": template})
	})
	r.DELETE("/admin/storyboard-prompts/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if err := svc.DeleteStoryboardPromptTemplate(user, c.Param("id")); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"ok": true})
	})
	r.GET("/admin/settings/oss", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminOSSSetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PATCH("/admin/settings/oss", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.OSSSettingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateOSSSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminRuntimePolicySetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/runtime-policy/self-use", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminSelfUseRuntimePolicy(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PUT("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.RuntimePolicySetting
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateRuntimePolicySetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.DELETE("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.ResetRuntimePolicySetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/api-logs", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		logs, err := svc.AdminAPICallLogs(user, service.APICallLogQuery{AnalyticsQuery: analyticsQuery(c), Keyword: c.Query("keyword"), Status: c.Query("status"), Page: page, Limit: limit})
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, logs)
	})
	r.GET("/admin/api-logs/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		log, err := svc.AdminAPICallLog(user, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"log": log})
	})
	r.POST("/admin/api-logs/:id/query-task", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		result, err := svc.AdminQueryFailedVideoTask(c.Request.Context(), user, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/api-logs-export.csv", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		selectedIDs := []string(nil)
		if value := strings.TrimSpace(c.Query("ids")); value != "" {
			selectedIDs = strings.Split(value, ",")
		}
		data, err := svc.AdminAPICallLogsCSV(user, service.APICallLogQuery{AnalyticsQuery: analyticsQuery(c), Keyword: c.Query("keyword"), Status: c.Query("status"), IDs: selectedIDs})
		if err != nil {
			failService(c, err)
			return
		}
		c.Header("Content-Disposition", "attachment; filename=api-calls-"+time.Now().UTC().Format("20060102-150405")+".csv")
		c.Data(http.StatusOK, "text/csv; charset=utf-8", data)
	})
}

func RegisterSystemProxyRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.Any("/ai/system/:channelId/*path", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		channel, err := svc.SystemChannel(c.Param("channelId"))
		if err != nil {
			fail(c, http.StatusNotFound, errors.New("系统渠道不存在或已停用"))
			return
		}
		proxySystemRequest(c, svc, user, channel)
	})
}

func proxySystemRequest(c *gin.Context, svc *service.Service, user *model.User, channel *model.ModelChannel) {
	startedAt := time.Now()
	policy, available := loadRuntimePolicy(c, svc)
	if !available || !enforceRateLimit(c, "system-proxy:"+user.ID, policy.Request.SystemRelayPerMinute, time.Minute) {
		return
	}
	relayTimeout := time.Duration(policy.Request.CustomRelayTimeoutMinutes) * time.Minute
	requestCtx, cancelRequest := context.WithTimeout(c.Request.Context(), relayTimeout)
	defer cancelRequest()
	path := c.Param("path")
	if path == "" {
		path = "/"
	}
	requestDeadline, _ := requestCtx.Deadline()
	body, err := readProxyRequestBody(c, policy.Request.SystemRelayRequestMB<<20, requestDeadline)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			fail(c, http.StatusRequestEntityTooLarge, errors.New("系统渠道请求超过配置上限"))
			return
		}
		if proxyRequestReadTimedOut(err) || (errors.Is(requestCtx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil) {
			fail(c, http.StatusGatewayTimeout, errors.New("系统渠道请求在调用供应商前超时：本次尚未创建计费订单或发出上游请求"))
			return
		}
		fail(c, http.StatusBadRequest, errors.New("读取系统渠道请求失败"))
		return
	}
	if err := authorizeSystemProxy(channel, c.Request.Method, path, c.GetHeader("Content-Type"), body); err != nil {
		fail(c, http.StatusForbidden, err)
		return
	}
	modelName := proxyRequestModelFromPath(path)
	if modelName == "" {
		modelName = proxyRequestModel(c.GetHeader("Content-Type"), body)
	}
	requestAPIFormat := proxyAPIFormat(path, channel.APIFormat)
	if c.Request.Method == http.MethodPost {
		configuredProtocol, protocolErr := svc.SystemTextModelProtocol(channel.ID, modelName)
		if protocolErr != nil {
			failService(c, protocolErr)
			return
		}
		if requestedProtocol := proxyTextProtocol(path); requestedProtocol == "" || requestedProtocol != configuredProtocol {
			fail(c, http.StatusForbidden, errors.New("请求路径与该模型配置的文本协议不一致，本次未调用供应商"))
			return
		}
	}
	scene := proxyRequestScene(c.GetHeader("X-Canvas-Scene"))
	billingOrderID := ""
	query := c.Request.URL.Query()
	for _, key := range []string{"key", "api_key", "access_token", "token"} {
		query.Del(key)
	}
	target := systemProxyUpstreamBase(channel, requestAPIFormat) + path
	if encodedQuery := query.Encode(); encodedQuery != "" {
		target += "?" + encodedQuery
	}
	if _, err := service.ValidateOutboundURLContext(requestCtx, target); err != nil {
		failService(c, err)
		return
	}
	proxyIdempotencyKey := ""
	if c.Request.Method == http.MethodPost {
		// 同一逻辑请求必须在等待渠道槽位前完成去重；写入时仍由数据库唯一索引处理并发竞态。
		proxyIdempotencyKey, err = svc.PrepareProxyBillingIdempotency(user.ID, c.GetHeader("X-Idempotency-Key"))
		if err != nil {
			failService(c, err)
			return
		}
	}
	streamRequested := requestWantsTextEventStream(c.GetHeader("Accept"), target, body)
	streamObservationKey := ""
	if streamRequested {
		key, observationErr := svc.SystemTextTransportObservationKey(channel.ID, modelName)
		if observationErr != nil {
			stdlog.Printf("system text transport observation start failed channel=%s model=%s: %v", channel.ID, strings.TrimPrefix(modelName, "models/"), observationErr)
		} else {
			streamObservationKey = key
		}
	}
	// 同步代理与后台任务必须共享渠道槽位，否则两条入口会共同超过供应商并发上限。
	releaseChannel, concurrencyLimit, err := svc.AcquireChannelSlot(requestCtx, channel.ID, "", relayTimeout+time.Minute)
	if err != nil {
		log := apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, model.ApiCallStatusFailed, 0, time.Since(startedAt), err.Error(), concurrencyLimit)
		log.ErrorCode, log.Error = service.ChannelSlotFailureDetails(err)
		if logErr := logSystemProxyCall(svc, log, nil); logErr != nil {
			stdlog.Printf("system proxy preflight log failed channel=%s user=%s: %v", channel.ID, user.ID, logErr)
		}
		if errors.Is(err, context.DeadlineExceeded) && c.Request.Context().Err() == nil {
			fail(c, http.StatusGatewayTimeout, errors.New("等待系统渠道并发槽位超时：本次尚未调用供应商或创建计费订单，请稍后再试"))
			return
		}
		failInternal(c, http.StatusServiceUnavailable, "系统渠道并发协调服务暂时不可用", err)
		return
	}
	defer releaseChannel()
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil {
		fail(c, http.StatusGatewayTimeout, errors.New("系统渠道总时限在调用供应商前到期：本次尚未创建计费订单或发出上游请求，请稍后重试"))
		return
	}
	if c.Request.Method == http.MethodPost {
		order, err := svc.ReserveProxyBilling(user.ID, channel.ID, strings.TrimPrefix(modelName, "models/"), "text", scene, proxyIdempotencyKey, 0)
		if err != nil {
			failService(c, err)
			return
		}
		billingOrderID = order.ID
		if err := svc.MarkBillingRunning(billingOrderID); err != nil {
			refundErr := systemProxyBillingTransitionError(billingOrderID, "退回尚未发出的系统渠道请求", svc.RefundBillingFromExecution(billingOrderID, "系统渠道请求尚未发出"))
			failService(c, errors.Join(err, refundErr))
			return
		}
	}
	upstreamReq, err := http.NewRequestWithContext(requestCtx, c.Request.Method, target, bytes.NewReader(body))
	if err != nil {
		refundErr := systemProxyBillingTransitionError(billingOrderID, "退回请求构造失败的预留积分", svc.RefundBillingFromExecution(billingOrderID, "系统渠道请求构造失败"))
		if refundErr != nil {
			failInternal(c, http.StatusInternalServerError, "系统渠道请求尚未发出，但预留积分退款失败，请联系管理员核对", errors.Join(err, refundErr))
			return
		}
		fail(c, http.StatusBadRequest, errors.New("系统渠道请求地址或参数无效，本次没有调用供应商"))
		return
	}
	if contentType := c.GetHeader("Content-Type"); contentType != "" {
		upstreamReq.Header.Set("Content-Type", contentType)
	}
	if streamRequested {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else if accept := c.GetHeader("Accept"); accept != "" {
		upstreamReq.Header.Set("Accept", accept)
	}
	if requestAPIFormat == "gemini" {
		upstreamReq.Header.Set("x-goog-api-key", channel.APIKey)
	} else {
		upstreamReq.Header.Set("Authorization", "Bearer "+channel.APIKey)
	}
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil {
		if refundErr := systemProxyBillingTransitionError(billingOrderID, "退回上游请求发出前超时的预留积分", svc.RefundBillingFromExecution(billingOrderID, "系统渠道总时限在上游请求发出前到期")); refundErr != nil {
			failInternal(c, http.StatusInternalServerError, "系统渠道没有发出上游请求，但预留积分退款失败，请联系管理员核对", refundErr)
			return
		}
		fail(c, http.StatusGatewayTimeout, errors.New("系统渠道总时限在调用供应商前到期：本次没有发出上游请求，已退回预留积分"))
		return
	}

	status := model.ApiCallStatusSucceeded
	statusCode := 0
	errorText := ""
	resp, err := service.OutboundHTTPClient(relayTimeout).Do(upstreamReq)
	if err != nil {
		status = model.ApiCallStatusFailed
		stdlog.Printf("system proxy request failed order=%s channel=%s method=%s path=%s: %v", billingOrderID, channel.ID, c.Request.Method, path, err)
		errorText = service.SafeProviderLogError(err)
		if streamRequested && c.Request.Context().Err() == nil {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
		billingErr := systemProxyBillingTransitionError(billingOrderID, "标记系统渠道连接中断的费用待核对", svc.MarkBillingUncertain(billingOrderID, "系统渠道连接中断，费用状态待核对"))
		errorText = appendProxyAccountingError(errorText, billingErr)
		if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, status, statusCode, time.Since(startedAt), errorText, concurrencyLimit), nil); logErr != nil {
			stdlog.Printf("system proxy failure log failed order=%s: %v", billingOrderID, logErr)
		}
		if errors.Is(err, context.DeadlineExceeded) && c.Request.Context().Err() == nil {
			fail(c, http.StatusGatewayTimeout, errors.New("系统渠道等待超时：模型请求可能仍在供应商服务端执行并产生费用，请勿立即重试，请先核对请求明细或供应商后台"))
			return
		}
		fail(c, http.StatusBadGateway, errors.New("系统渠道连接中断：请求状态不确定且可能已经计费，请勿立即重试，请先核对请求明细或供应商后台"))
		return
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = model.ApiCallStatusFailed
	}
	responseLimit := policy.Request.SystemRelayResponseMB << 20
	responseReader := io.Reader(resp.Body)
	eventStream := false
	var streamDelivery *upstreamStreamDeliveryObservation
	if status == model.ApiCallStatusSucceeded {
		if streamRequested {
			streamDelivery = &upstreamStreamDeliveryObservation{}
			responseReader = &observedUpstreamStreamReader{source: resp.Body, observation: streamDelivery}
		}
		responseReader, eventStream, err = eventStreamResponseReader(responseReader, resp.Header.Get("Content-Type"), streamRequested)
		if err != nil {
			status = model.ApiCallStatusFailed
			stdlog.Printf("system proxy response protocol detection failed order=%s channel=%s path=%s: %v", billingOrderID, channel.ID, path, err)
			errorText = service.SafeProviderLogError(err)
			if c.Request.Context().Err() == nil {
				recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
			}
			billingErr := systemProxyBillingTransitionError(billingOrderID, "标记响应协议识别中断的费用待核对", svc.MarkBillingUncertain(billingOrderID, "系统渠道响应协议识别中断，费用状态待核对"))
			errorText = appendProxyAccountingError(errorText, billingErr)
			if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, status, statusCode, time.Since(startedAt), errorText, concurrencyLimit), nil); logErr != nil {
				stdlog.Printf("system proxy protocol failure log failed order=%s: %v", billingOrderID, logErr)
			}
			fail(c, http.StatusBadGateway, errors.New("系统渠道流式响应读取中断：请求状态不确定且可能已经计费，请勿立即重试"))
			return
		}
	}
	if status == model.ApiCallStatusSucceeded && eventStream {
		proxySystemStreamResponse(c, svc, user, channel, resp, responseReader, body, scene, billingOrderID, path, target, modelName, streamRequested, streamObservationKey, streamDelivery, startedAt, concurrencyLimit, responseLimit)
		return
	}
	if status == model.ApiCallStatusSucceeded && strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))), "text/event-stream") {
		// 前缀确认是 JSON 时修正上游反向误标，让浏览器按非流式结果解析并记录风险。
		resp.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(responseReader, responseLimit+1))
	if readErr != nil {
		status = model.ApiCallStatusFailed
		stdlog.Printf("system proxy response read failed order=%s channel=%s path=%s: %v", billingOrderID, channel.ID, path, readErr)
		errorText = service.SafeProviderLogError(readErr)
		if streamRequested && c.Request.Context().Err() == nil {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
		billingErr := systemProxyBillingTransitionError(billingOrderID, "标记响应读取失败的费用待核对", svc.MarkBillingUncertain(billingOrderID, "系统渠道响应读取失败，费用状态待核对"))
		errorText = appendProxyAccountingError(errorText, billingErr)
		if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, status, statusCode, time.Since(startedAt), errorText, concurrencyLimit), nil); logErr != nil {
			stdlog.Printf("system proxy response failure log failed order=%s: %v", billingOrderID, logErr)
		}
		fail(c, http.StatusBadGateway, errors.New("系统渠道响应读取中断：请求状态不确定且可能已经计费，请勿立即重试，请先核对请求明细或供应商后台"))
		return
	}
	if int64(len(responseBody)) > responseLimit {
		reason := "上游已响应但响应体超过限制，费用状态待核对"
		if streamRequested {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
		billingErr := systemProxyBillingTransitionError(billingOrderID, "标记响应超限的费用待核对", svc.MarkBillingUncertain(billingOrderID, reason))
		reason = appendProxyAccountingError(reason, billingErr)
		if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, model.ApiCallStatusFailed, statusCode, time.Since(startedAt), reason, concurrencyLimit), nil); logErr != nil {
			stdlog.Printf("system proxy oversized response log failed order=%s: %v", billingOrderID, logErr)
		}
		fail(c, http.StatusBadGateway, fmt.Errorf("系统渠道响应超过 %dMB 限制：结果不完整且可能已经计费，请勿立即重试", policy.Request.SystemRelayResponseMB))
		return
	}
	if streamRequested && status == model.ApiCallStatusSucceeded && !json.Valid(responseBody) {
		reason := "系统渠道忽略流式请求后没有返回完整 JSON，费用状态待核对"
		if c.Request.Context().Err() == nil {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
		billingErr := systemProxyBillingTransitionError(billingOrderID, "标记非完整响应的费用待核对", svc.MarkBillingUncertain(billingOrderID, reason))
		reason = appendProxyAccountingError(reason, billingErr)
		if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, model.ApiCallStatusFailed, statusCode, time.Since(startedAt), reason, concurrencyLimit), responseBody); logErr != nil {
			stdlog.Printf("system proxy invalid response log failed order=%s: %v", billingOrderID, logErr)
		}
		fail(c, http.StatusBadGateway, errors.New("系统渠道没有返回完整的流式事件或 JSON：请求可能已经计费，请勿立即重试，请先核对请求明细或供应商后台"))
		return
	}
	if streamRequested {
		if status == model.ApiCallStatusSucceeded {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "succeeded", "non-stream-compatible", startedAt)
		} else if statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity || service.ProviderHTTPStatusRequiresBillingReview(statusCode) {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
	}
	logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, body, scene, status, statusCode, time.Since(startedAt), errorText, concurrencyLimit), responseBody)
	if logErr != nil && status != model.ApiCallStatusSucceeded {
		stdlog.Printf("system proxy upstream failure log failed order=%s status=%d: %v", billingOrderID, statusCode, logErr)
	}
	var billingTransitionErr error
	if status == model.ApiCallStatusSucceeded {
		if logErr != nil {
			stdlog.Printf("system proxy success log failed order=%s: %v", billingOrderID, logErr)
			billingTransitionErr = systemProxyBillingTransitionError(billingOrderID, "标记成功请求日志缺失的费用待核对", svc.MarkBillingUncertain(billingOrderID, "上游成功但请求日志写入失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
		} else if err := svc.SettleBillingFromExecution(billingOrderID, ""); err != nil {
			settlementErr := systemProxyBillingTransitionError(billingOrderID, "结算系统渠道请求", err)
			uncertainErr := systemProxyBillingTransitionError(billingOrderID, "标记系统渠道结算失败的费用待核对", svc.MarkBillingUncertain(billingOrderID, "上游成功但积分结算失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
			billingTransitionErr = errors.Join(settlementErr, uncertainErr)
		}
	} else if service.ProviderHTTPStatusRequiresBillingReview(statusCode) {
		billingTransitionErr = systemProxyBillingTransitionError(billingOrderID, "标记上游异常状态的费用待核对", svc.MarkBillingUncertain(billingOrderID, fmt.Sprintf("上游返回 %d，费用状态待核对", statusCode)))
	} else {
		billingTransitionErr = systemProxyBillingTransitionError(billingOrderID, "退回上游明确拒绝请求的预留积分", svc.RefundBillingFromExecution(billingOrderID, "上游明确返回失败"))
	}
	if service.ProviderHTTPStatusRequiresBillingReview(statusCode) {
		fail(c, statusCode, errors.New(service.ProviderHTTPBillingReviewMessage(statusCode)))
		return
	}
	if status == model.ApiCallStatusSucceeded && logErr != nil {
		failInternal(c, http.StatusInternalServerError, "上游已返回完整结果，但平台请求日志写入失败；本次结果未交付、费用状态待核对且不会自动重试，请联系管理员核对", logErr)
		return
	}
	if status == model.ApiCallStatusSucceeded && billingTransitionErr != nil {
		failInternal(c, http.StatusInternalServerError, "上游已返回完整结果，但平台积分结算失败；本次结果未作为成功交付、费用状态待核对且不会自动重试，请联系管理员核对", billingTransitionErr)
		return
	}
	if status == model.ApiCallStatusFailed && billingTransitionErr != nil {
		failInternal(c, http.StatusInternalServerError, "上游已明确拒绝请求，但平台预留积分状态更新失败；本次不会自动重试，请联系管理员核对", billingTransitionErr)
		return
	}
	for _, key := range []string{"Content-Type", "Cache-Control", "Content-Disposition"} {
		if value := resp.Header.Get(key); value != "" {
			c.Header(key, value)
		}
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), responseBody)
}

// 系统渠道必须把 SSE 逐块送到浏览器，否则慢推理模型即使正常流式返回，页面仍会长时间表现为无响应。
// 同时保留一份受大小限制的副本用于 token、费用和请求日志解析；中途断开时费用状态必须进入待核对。
func proxySystemStreamResponse(c *gin.Context, svc *service.Service, user *model.User, channel *model.ModelChannel, resp *http.Response, source io.Reader, requestBody []byte, scene string, billingOrderID string, path string, target string, modelName string, streamRequested bool, streamObservationKey string, streamDelivery *upstreamStreamDeliveryObservation, startedAt time.Time, concurrencyLimit int, responseLimit int64) {
	for _, key := range []string{"Content-Type", "Cache-Control", "Content-Disposition"} {
		if value := resp.Header.Get(key); value != "" {
			c.Header(key, value)
		}
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(resp.StatusCode)
	c.Writer.WriteHeaderNow()

	var captured bytes.Buffer
	integrity := &eventStreamIntegrity{}
	buffer := make([]byte, 32<<10)
	failStream := func(reason string) {
		billingErr := error(nil)
		if billingOrderID != "" {
			billingErr = systemProxyBillingTransitionError(billingOrderID, "标记流式请求的费用待核对", svc.MarkBillingUncertain(billingOrderID, reason))
		}
		reason = appendProxyAccountingError(reason, billingErr)
		if logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, requestBody, scene, model.ApiCallStatusFailed, resp.StatusCode, time.Since(startedAt), reason, concurrencyLimit), captured.Bytes()); logErr != nil {
			stdlog.Printf("system proxy stream failure log failed order=%s: %v", billingOrderID, logErr)
		}
	}
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			if int64(captured.Len())+int64(read) > responseLimit {
				writeProxyStreamError(c, "系统渠道流式响应超过上限，结果不完整且费用状态待核对；请勿立即重试，请先核对请求明细或供应商后台")
				if streamRequested {
					recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
				}
				failStream(fmt.Sprintf("系统渠道流式响应超过 %dMB 限制，费用状态待核对", responseLimit>>20))
				return
			}
			if err := integrity.Push(buffer[:read]); err != nil {
				stdlog.Printf("system proxy stream integrity failed order=%s channel=%s path=%s: %v", billingOrderID, channel.ID, path, err)
				writeProxyStreamError(c, "系统渠道返回了损坏的流式事件，结果不完整且费用状态待核对；请勿立即重试")
				if streamRequested {
					recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
				}
				failStream("系统渠道流式响应事件损坏，费用状态待核对；底层诊断仅记录于 Backend 日志")
				return
			}
			captured.Write(buffer[:read])
			if _, writeErr := c.Writer.Write(buffer[:read]); writeErr != nil {
				failStream("浏览器连接在系统渠道流式响应完成前中断，费用状态待核对")
				return
			}
			c.Writer.Flush()
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			stdlog.Printf("system proxy stream read failed order=%s channel=%s path=%s: %v", billingOrderID, channel.ID, path, readErr)
			writeProxyStreamError(c, "系统渠道流式响应中断，结果不完整且费用状态待核对；请勿立即重试，请先核对请求明细或供应商后台")
			if streamRequested && c.Request.Context().Err() == nil {
				recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
			}
			failStream("系统渠道流式响应读取中断，费用状态待核对；底层诊断仅记录于 Backend 日志")
			return
		}
	}
	if err := integrity.Finish(); err != nil {
		stdlog.Printf("system proxy stream completion check failed order=%s channel=%s path=%s: %v", billingOrderID, channel.ID, path, err)
		writeProxyStreamError(c, "系统渠道流式响应没有完整结束，结果不完整且费用状态待核对；请勿立即重试")
		if streamRequested {
			recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "failed", "", startedAt)
		}
		failStream("系统渠道流式响应没有完整结束，费用状态待核对；底层诊断仅记录于 Backend 日志")
		return
	}
	if streamRequested {
		transport := "stream-unverified"
		if streamDelivery.Progressive() {
			transport = "stream"
		}
		recordSystemTextTransportObservation(svc, channel.ID, modelName, streamObservationKey, "succeeded", transport, startedAt)
	}

	logErr := logSystemProxyCall(svc, apiCallLog(user, channel, billingOrderID, c.Request.Method, path, target, requestBody, scene, model.ApiCallStatusSucceeded, resp.StatusCode, time.Since(startedAt), "", concurrencyLimit), captured.Bytes())
	if billingOrderID != "" {
		if logErr != nil {
			stdlog.Printf("system proxy stream success log failed order=%s: %v", billingOrderID, logErr)
			uncertainErr := systemProxyBillingTransitionError(billingOrderID, "标记流式成功日志缺失的费用待核对", svc.MarkBillingUncertain(billingOrderID, "上游流式请求成功但请求日志写入失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
			message := appendProxyAccountingError("上游流式响应已结束，但平台请求日志写入失败；本次结果未作为成功交付、费用状态待核对且不会自动重试，请联系管理员核对", uncertainErr)
			writeProxyStreamError(c, internalResponseMessage(c, message))
		} else if err := svc.SettleBillingFromExecution(billingOrderID, ""); err != nil {
			settlementErr := systemProxyBillingTransitionError(billingOrderID, "结算流式系统渠道请求", err)
			uncertainErr := systemProxyBillingTransitionError(billingOrderID, "标记流式请求结算失败的费用待核对", svc.MarkBillingUncertain(billingOrderID, "上游流式请求成功但积分结算失败，费用状态待核对；底层诊断仅记录于 Backend 日志"))
			message := "上游流式响应已结束，但平台积分结算失败；本次结果未作为成功交付、费用状态待核对且不会自动重试，请联系管理员核对"
			if settlementErr != nil && uncertainErr != nil {
				message = appendProxyAccountingError(message, uncertainErr)
			}
			writeProxyStreamError(c, internalResponseMessage(c, message))
		}
	}
}

func recordSystemTextTransportObservation(svc *service.Service, channelID string, modelName string, configHash string, status string, transport string, startedAt time.Time) {
	if configHash == "" {
		return
	}
	if err := svc.RecordSystemTextTransportObservation(channelID, modelName, configHash, status, transport, startedAt, time.Since(startedAt).Milliseconds()); err != nil {
		stdlog.Printf("system text transport observation failed channel=%s model=%s: %v", channelID, strings.TrimPrefix(modelName, "models/"), err)
	}
}

func proxyRequestScene(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

func apiCallLog(user *model.User, channel *model.ModelChannel, billingOrderID string, method string, path string, target string, body []byte, scene string, status model.ApiCallStatus, statusCode int, duration time.Duration, errorText string, concurrencyLimit int) model.ApiCallLog {
	requestKind := "create"
	if method == http.MethodGet {
		requestKind = "poll"
		if strings.HasSuffix(strings.TrimRight(path, "/"), "/content") {
			requestKind = "download"
		}
	}
	return model.ApiCallLog{
		UserID:           user.ID,
		ChannelID:        channel.ID,
		BillingOrderID:   billingOrderID,
		Source:           "system-channel",
		Capability:       "text",
		Operation:        scene,
		RequestKind:      requestKind,
		Billable:         method == http.MethodPost,
		APIFormat:        proxyAPIFormat(path, channel.APIFormat),
		Method:           method,
		Path:             path,
		Model:            readPayloadModel(path, body),
		Status:           status,
		StatusCode:       statusCode,
		DurationMs:       duration.Milliseconds(),
		Error:            errorText,
		ConcurrencyLimit: concurrencyLimit,
		UpstreamURL:      target,
	}
}

func logSystemProxyCall(svc *service.Service, log model.ApiCallLog, responseBody []byte) error {
	svc.EnrichAPICallLog(&log, responseBody)
	return svc.LogAPICall(log)
}

func systemProxyBillingTransitionError(orderID string, action string, transitionErr error) error {
	if transitionErr == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s失败：%w", action, transitionErr)
	stdlog.Printf("system proxy billing transition failed order=%s action=%s: %v", orderID, action, transitionErr)
	return wrapped
}

func appendProxyAccountingError(message string, accountingErr error) string {
	if accountingErr == nil {
		return message
	}
	const publicAccountingMessage = "平台计费状态更新失败，请管理员按订单核对 Backend 日志"
	if strings.TrimSpace(message) == "" {
		return publicAccountingMessage
	}
	return message + "；" + publicAccountingMessage
}

func readPayloadModel(requestPath string, body []byte) string {
	if modelName := proxyRequestModelFromPath(requestPath); modelName != "" {
		return modelName
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if modelName, ok := payload["model"].(string); ok {
		return modelName
	}
	return ""
}

// 浏览器向本站系统代理发送协议相对路径；服务端必须补齐供应商 API 版本，官方根地址才不会落到不存在的 /responses 或 /models。
func systemProxyUpstreamBase(channel *model.ModelChannel, apiFormat string) string {
	base := strings.TrimRight(strings.TrimSpace(channel.BaseURL), "/")
	lowerBase := strings.ToLower(base)
	if apiFormat == "gemini" {
		if !strings.HasSuffix(lowerBase, "/v1") && !strings.HasSuffix(lowerBase, "/v1beta") {
			base += "/v1beta"
		}
		return base
	}
	for _, suffix := range []string{"/v1", "/v1beta", "/api/v3", "/api/plan/v3"} {
		if strings.HasSuffix(lowerBase, suffix) {
			return base
		}
	}
	return base + "/v1"
}

func currentUser(c *gin.Context, svc *service.Service) (*model.User, error) {
	return svc.CurrentUser(sessionCookie(c))
}

func sessionCookie(c *gin.Context) string {
	value, _ := c.Cookie(service.SessionCookieName)
	return value
}

func setSessionCookie(c *gin.Context, value string, maxAge int) {
	secure := requestUsesHTTPS(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     service.SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

func clearSessionCookie(c *gin.Context) {
	secure := requestUsesHTTPS(c)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     service.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

func requestUsesHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	forwardedProto := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(forwardedProto, "https")
}
