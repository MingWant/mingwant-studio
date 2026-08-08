package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"infinite-canvas/backend/internal/database"
	"infinite-canvas/backend/internal/handler"
	"infinite-canvas/backend/internal/repository"
	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	defaultShutdownDrainTimeout = 40 * time.Minute
	minimumShutdownDrainTimeout = time.Minute
	maximumContainerStopGrace   = 7 * 24 * time.Hour
	minimumContainerStopBuffer  = time.Minute
	maximumShutdownDrainTimeout = maximumContainerStopGrace - minimumContainerStopBuffer
	shutdownFinalizationTimeout = 30 * time.Second
)

func main() {
	if err := validateCORSOrigins(os.Getenv("CANVAS_CORS_ORIGINS")); err != nil {
		log.Fatal(err)
	}
	shutdownDrainTimeout, err := parseShutdownDrainTimeout(os.Getenv("CANVAS_SHUTDOWN_DRAIN_TIMEOUT"))
	if err != nil {
		log.Fatal(err)
	}
	if err := validateContainerStopGracePeriod(os.Getenv("CANVAS_CONTAINER_STOP_GRACE_PERIOD"), shutdownDrainTimeout); err != nil {
		log.Fatal(err)
	}
	dataDir := env("CANVAS_BACKEND_DATA_DIR", "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(database.Config{
		Driver:  env("CANVAS_DATABASE_DRIVER", "sqlite"),
		DSN:     os.Getenv("DATABASE_URL"),
		DataDir: dataDir,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := database.ConfigurePool(db); err != nil {
		log.Fatal(err)
	}
	if err := database.MigrateSchema(db); err != nil {
		log.Fatal(err)
	}

	repo := repository.New(db)
	svc := service.New(repo, dataDir)
	svc.ConfigureShutdownDrainTimeout(shutdownDrainTimeout)
	if err := svc.ValidateRuntime(); err != nil {
		log.Fatal(err)
	}
	if err := svc.ValidateRuntimePolicyShutdownWindow(); err != nil {
		log.Fatal(err)
	}
	if err := svc.EnsureSystemChannelModels(); err != nil {
		log.Fatal(err)
	}
	if err := svc.EnsureDefaultStoryboardPromptTemplate(); err != nil {
		log.Fatal(err)
	}
	if err := svc.EnsureBuiltinProjectWorkflowTemplate(); err != nil {
		log.Fatal(err)
	}
	if summary, err := svc.MigrateLegacyStorage(); err != nil {
		log.Printf("storage migration skipped after error: %v", err)
	} else if summary.Backup != "" {
		log.Printf("storage migration completed: tasks=%d assets=%d projects=%d backup=%s", summary.Tasks, summary.Assets, summary.Projects, summary.Backup)
	}
	svc.StartWorker()

	r := gin.New()
	requestDrain := newRequestDrainTracker()
	r.Use(handler.RequestIDMiddleware(), handler.RecoveryMiddleware(), securityHeaders(), gin.LoggerWithConfig(gin.LoggerConfig{
		// 查询参数可能含 OAuth code、签名地址等一次性凭据；访问日志只保留脱敏路径和请求编号。
		SkipQueryString: true,
		Formatter: func(param gin.LogFormatterParams) string {
			requestID, _ := param.Keys[handler.RequestIDContextKey].(string)
			return fmt.Sprintf("%s request_id=%s - [%s] \"%s %s\" %d %s %s\n", param.ClientIP, requestID, param.TimeStamp.Format(time.RFC3339), param.Method, redactCanvasSharePath(param.Path), param.StatusCode, param.Latency, param.ErrorMessage)
		},
	}))
	r.Use(cors(), requestDrain.Middleware())
	handler.ConfigureRuntime(svc)
	api := r.Group("/api")
	handler.RegisterHealthRoutes(api, svc)
	handler.RegisterOAuthCallbackRoutes(r, svc)
	handler.RegisterAuthRoutes(api, svc)
	handler.RegisterAdminRoutes(api, svc)
	handler.RegisterAdminAnalyticsRoutes(api, svc)
	handler.RegisterAnnouncementRoutes(api, svc)
	handler.RegisterFinanceRoutes(api, svc)
	// 登录态模型目录代理：避免浏览器直连各上游时分别处理 CORS。
	handler.RegisterChannelModelRoutes(api, svc)
	handler.RegisterChannelProbeRoutes(api, svc)
	handler.RegisterSystemProxyRoutes(api, svc)
	handler.RegisterCustomRelayRoutes(api, svc)
	handler.RegisterTaskRoutes(api, svc)
	handler.RegisterSessionRoutes(api, svc)
	handler.RegisterSkillRoutes(api, svc)
	handler.RegisterUserDataRoutes(api, svc)
	handler.RegisterProjectRoutes(api, svc)
	handler.RegisterCanvasShareRoutes(api, svc)

	addr := env("CANVAS_BACKEND_ADDR", ":8080")
	log.Printf("MingWant Studio backend listening on %s", addr)
	// 只限制请求头和空闲 Keep-Alive；生成接口需要长时间上传及 SSE，不能设置全局 ReadTimeout/WriteTimeout 提前截断。
	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownSignals)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case receivedSignal := <-shutdownSignals:
		// Docker 默认停机宽限很短；先停止接单，再让在线 SSE 与后台付费任务共用一个排空窗口。
		log.Printf("received %s, draining HTTP requests and worker tasks for up to %s", receivedSignal, shutdownDrainTimeout)
		httpRequestsDrained := requestDrain.BeginDrain()
		svc.BeginWorkerShutdown()
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), shutdownDrainTimeout)
		httpDone := make(chan error, 1)
		workerDone := make(chan error, 1)
		go func() { httpDone <- server.Shutdown(drainCtx) }()
		go func() { workerDone <- svc.ShutdownWorker(drainCtx) }()
		httpErr := <-httpDone
		workerErr := <-workerDone
		drainTimedOut := errors.Is(drainCtx.Err(), context.DeadlineExceeded)
		cancelDrain()
		if drainTimedOut {
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				httpErr = errors.Join(httpErr, closeErr)
			}
		}
		finalizationCtx, cancelFinalization := context.WithTimeout(context.Background(), shutdownFinalizationTimeout)
		finalizationErr := waitForRequestDrain(finalizationCtx, httpRequestsDrained)
		cancelFinalization()
		if drainErr := errors.Join(httpErr, workerErr, finalizationErr); drainErr != nil {
			log.Printf("backend graceful shutdown incomplete; remaining provider calls may require billing review and must not be replayed: %v", drainErr)
			return
		}
		log.Printf("backend graceful shutdown completed")
	}
}

func parseShutdownDrainTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultShutdownDrainTimeout, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("CANVAS_SHUTDOWN_DRAIN_TIMEOUT 必须是 Go duration，例如 40m 或 1h：%w", err)
	}
	if duration < minimumShutdownDrainTimeout || duration > maximumShutdownDrainTimeout {
		return 0, fmt.Errorf("CANVAS_SHUTDOWN_DRAIN_TIMEOUT 必须在 %s 到 %s 之间", minimumShutdownDrainTimeout, maximumShutdownDrainTimeout)
	}
	return duration, nil
}

func validateContainerStopGracePeriod(value string, drainTimeout time.Duration) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("CANVAS_CONTAINER_STOP_GRACE_PERIOD 必须是 Go duration，例如 45m 或 1h：%w", err)
	}
	if duration <= drainTimeout || duration-drainTimeout < minimumContainerStopBuffer {
		return fmt.Errorf("CANVAS_CONTAINER_STOP_GRACE_PERIOD 必须至少比 CANVAS_SHUTDOWN_DRAIN_TIMEOUT 多 %s（当前分别为 %s 与 %s）", minimumContainerStopBuffer, duration, drainTimeout)
	}
	if duration > maximumContainerStopGrace {
		return fmt.Errorf("CANVAS_CONTAINER_STOP_GRACE_PERIOD 不能超过 %s", maximumContainerStopGrace)
	}
	return nil
}

// Backend 可能被运维误暴露到公网；即使绕过 Web/Nginx，纯 API 响应也必须拒绝嵌入、嗅探和浏览器能力继承。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'none'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'none'; sandbox")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), hid=(), bluetooth=(), browsing-topics=()")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Permitted-Cross-Domain-Policies", "none")
		c.Header("X-XSS-Protection", "0")
		c.Next()
	}
}

func redactCanvasSharePath(path string) string {
	const prefix = "/api/public/canvas-shares/"
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	remainder := strings.TrimPrefix(path, prefix)
	if index := strings.IndexByte(remainder, '/'); index >= 0 {
		return prefix + ":token" + remainder[index:]
	}
	return prefix + ":token"
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin != "" && !allowedOrigin(c, origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": http.StatusForbidden, "data": nil, "msg": "不允许的跨域来源"})
			return
		}
		if origin == "" && requestChangesState(c.Request.Method) && strings.EqualFold(strings.TrimSpace(c.GetHeader("Sec-Fetch-Site")), "cross-site") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": http.StatusForbidden, "data": nil, "msg": "不允许无来源证明的跨站写请求"})
			return
		}
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
		}
		c.Header("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization, X-Requested-With, X-Goog-Api-Key, X-Canvas-Scene, X-Idempotency-Key, X-Canvas-Upstream-URL, X-Canvas-Upstream-Format")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func allowedOrigin(c *gin.Context, origin string) bool {
	parsed, ok := parseCORSOrigin(origin)
	if !ok {
		return false
	}
	// 反向代理必须保留原始 Host。X-Forwarded-Host 可由直连客户端伪造，不能用于放行携带 Cookie 的跨域请求。
	if strings.EqualFold(parsed.Host, strings.TrimSpace(c.Request.Host)) && strings.EqualFold(parsed.Scheme, requestScheme(c)) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("CANVAS_CORS_ORIGINS"), ",") {
		value := strings.TrimSpace(allowed)
		if value != "*" && strings.EqualFold(strings.TrimRight(value, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func validateCORSOrigins(value string) error {
	for _, entry := range strings.Split(value, ",") {
		origin := strings.TrimSpace(entry)
		if origin == "" {
			continue
		}
		if origin == "*" {
			return errors.New("CANVAS_CORS_ORIGINS 不允许使用 *；携带登录 Cookie 的接口必须逐项配置可信来源")
		}
		if _, ok := parseCORSOrigin(origin); !ok {
			return fmt.Errorf("CANVAS_CORS_ORIGINS 包含无效来源 %q；只允许 scheme://host[:port]", origin)
		}
	}
	return nil
}

func parseCORSOrigin(value string) (*url.URL, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(value, "#") || (parsed.Path != "" && parsed.Path != "/") {
		return nil, false
	}
	return parsed, true
}

func requestScheme(c *gin.Context) string {
	if c.Request.TLS != nil {
		return "https"
	}
	forwardedProto := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
	if strings.EqualFold(forwardedProto, "https") {
		return "https"
	}
	return "http"
}

func requestChangesState(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}
