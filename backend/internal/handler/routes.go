package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func RegisterTaskRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.POST("/tasks", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<20)
		var req service.CreateTaskRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		task, err := svc.CreateTask(user.ID, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, task)
	})
	r.GET("/tasks", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		tasks, err := svc.TasksWithOptions(user.ID, service.TaskListOptions{
			Limit:      limit,
			ProjectID:  c.Query("projectId"),
			ActiveOnly: c.Query("activeOnly") == "true",
		})
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, tasks)
	})
	r.GET("/tasks/:id", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if c.Query("includeBilling") == "true" {
			task, err := svc.TaskWithBilling(user.ID, c.Param("id"))
			if err != nil {
				failNotFound(c, "任务不存在", err)
				return
			}
			ok(c, task)
			return
		}
		task, err := svc.Task(user.ID, c.Param("id"))
		if err != nil {
			failNotFound(c, "任务不存在", err)
			return
		}
		ok(c, task)
	})
	r.POST("/tasks/:id/retry", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "tasks:"+user.ID, policy.Request.TaskCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
		var req struct {
			ConfirmNewProviderRequest bool `json:"confirmNewProviderRequest"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, errors.New("重试确认参数格式错误"))
			return
		}
		task, err := svc.RetryTask(user.ID, c.Param("id"), req.ConfirmNewProviderRequest)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, task)
	})
	r.POST("/tasks/:id/query-provider", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		result, err := svc.QueryFailedVideoTask(c.Request.Context(), user.ID, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.POST("/tasks/:id/recover-delivery", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		result, err := svc.RecoverTaskDelivery(user.ID, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.POST("/tasks/:id/cancel", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		task, err := svc.CancelTask(user.ID, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, task)
	})
	r.GET("/tasks/:id/logs", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		logs, err := svc.TaskLogs(user.ID, c.Param("id"))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, logs)
	})
}

func RegisterSessionRoutes(r *gin.RouterGroup, svc *service.Service) {
	createSession := func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "sessions:"+user.ID, policy.Request.SessionCreatePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<20)
		var req service.CreateSessionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		detail, err := svc.CreateSession(user.ID, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, detail)
	}
	querySession := func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		detail, err := svc.SessionDetail(user.ID, c.Param("id"))
		if err != nil {
			failNotFound(c, "会话不存在", err)
			return
		}
		ok(c, detail)
	}
	uploadFile := func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		policy, available := loadRuntimePolicy(c, svc)
		if !available || !enforceRateLimit(c, "session-files:"+user.ID, policy.Request.SessionFilePerMinute, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, (policy.Resource.SessionUploadMB<<20)+(1<<20))
		file, err := c.FormFile("file")
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		item, err := svc.StoreUpload(user.ID, c.PostForm("sessionId"), file)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, item)
	}
	downloadResults := func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		detail, err := svc.SessionDetail(user.ID, c.Param("id"))
		if err != nil {
			failNotFound(c, "会话不存在", err)
			return
		}
		ok(c, detail.Results)
	}
	r.POST("/sessions", createSession)
	r.GET("/sessions/:id", querySession)
	r.POST("/files", uploadFile)
	r.GET("/sessions/:id/results", downloadResults)
	r.POST("/create_session", createSession)
	r.GET("/query_session/:id", querySession)
	r.POST("/upload_file", uploadFile)
	r.GET("/download_results/:id", downloadResults)
}

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "data": data, "msg": "ok"})
}

func fail(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"code": status, "data": nil, "msg": err.Error()})
}
