package handler

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const RequestIDContextKey = "requestID"

var fallbackRequestID atomic.Uint64

// 请求编号由服务端生成，不能信任调用方传入的值；这样用户可据此查日志，也不会把攻击者控制的文本写入日志字段。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := newRequestID()
		c.Set(RequestIDContextKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// 默认 Gin Recovery 可能把完整请求头写入诊断日志；自定义恢复只记录路由模板、请求编号和堆栈，避免 Cookie 或密钥进入日志。
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				requestID := ensureRequestID(c)
				log.Printf("request panic request_id=%s method=%s route=%s panic=%v\n%s", requestID, c.Request.Method, requestLogRoute(c), recovered, debug.Stack())
				c.Abort()
				if !c.Writer.Written() {
					fail(c, http.StatusInternalServerError, errors.New(internalResponseMessage(c, "服务器内部错误，请稍后再试；若持续发生请联系管理员")))
				}
			}
		}()
		c.Next()
	}
}

func failService(c *gin.Context, err error) {
	var authErr *service.AuthError
	if errors.As(err, &authErr) && authErr.Status >= 400 && authErr.Status <= 599 {
		if authErr.Status >= 500 {
			logInternalError(c, authErr.Status, err)
			fail(c, authErr.Status, errors.New(internalResponseMessage(c, authErr.Message)))
			return
		}
		fail(c, authErr.Status, errors.New(authErr.Message))
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, errors.New("请求的资源不存在"))
		return
	}
	failInternal(c, http.StatusInternalServerError, "服务器内部错误，请稍后再试；若持续发生请联系管理员", err)
}

// 只有确定是“不存在”时才返回 404；数据库、解密和存储故障不能伪装成资源不存在并吞掉诊断。
func failNotFound(c *gin.Context, message string, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, errors.New(message))
		return
	}
	failService(c, err)
}

func failInternal(c *gin.Context, status int, publicMessage string, err error) {
	logInternalError(c, status, err)
	fail(c, status, errors.New(internalResponseMessage(c, publicMessage)))
}

// OAuth 失败会进入浏览器地址栏，未知错误必须先脱敏；明确的业务错误仍保留可操作说明。
func publicServiceErrorMessage(c *gin.Context, err error, fallback string) string {
	var authErr *service.AuthError
	if errors.As(err, &authErr) && authErr.Status >= 400 && authErr.Status <= 499 {
		return authErr.Message
	}
	logInternalError(c, http.StatusInternalServerError, err)
	return internalResponseMessage(c, fallback)
}

func logInternalError(c *gin.Context, status int, err error) {
	requestID := ensureRequestID(c)
	log.Printf("request internal error request_id=%s method=%s route=%s status=%d err=%v", requestID, c.Request.Method, requestLogRoute(c), status, err)
}

func internalResponseMessage(c *gin.Context, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "服务器内部错误，请稍后再试"
	}
	return fmt.Sprintf("%s（请求编号：%s）", message, ensureRequestID(c))
}

func requestLogRoute(c *gin.Context) string {
	if route := strings.TrimSpace(c.FullPath()); route != "" {
		return route
	}
	return "<unmatched>"
}

func ensureRequestID(c *gin.Context) string {
	if value, exists := c.Get(RequestIDContextKey); exists {
		if requestID, ok := value.(string); ok && requestID != "" {
			return requestID
		}
	}
	requestID := newRequestID()
	c.Set(RequestIDContextKey, requestID)
	c.Header("X-Request-ID", requestID)
	return requestID
}

func newRequestID() string {
	var payload [16]byte
	if _, err := rand.Read(payload[:]); err == nil {
		return hex.EncodeToString(payload[:])
	}
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), fallbackRequestID.Add(1))
}
