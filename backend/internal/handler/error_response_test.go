package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestFailServiceRedactsInternalErrorAndAddsRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), RecoveryMiddleware())
	router.GET("/internal", func(c *gin.Context) {
		failService(c, errors.New("sqlite /private/data.db password=secret"))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "private/data.db") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("internal error leaked: %s", response.Body.String())
	}
	requestID := response.Header().Get("X-Request-ID")
	if requestID == "" || !strings.Contains(response.Body.String(), requestID) {
		t.Fatalf("request id missing: header=%q body=%s", requestID, response.Body.String())
	}
}

func TestFailServicePreservesPublicErrorsAndNotFound(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		text   string
	}{
		{name: "business", err: service.Conflict("原请求仍在执行"), status: http.StatusConflict, text: "原请求仍在执行"},
		{name: "not-found", err: gorm.ErrRecordNotFound, status: http.StatusNotFound, text: "请求的资源不存在"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(RequestIDMiddleware())
			router.GET("/error", func(c *gin.Context) { failService(c, test.err) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/error", nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFailServiceKeepsSafeServerMessageAndRedactsCause(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/upstream", func(c *gin.Context) {
		failService(c, errors.Join(
			&service.AuthError{Status: http.StatusBadGateway, Message: "模型服务连接失败，请检查渠道配置"},
			errors.New("dial tcp internal-gateway.local:9443"),
		))
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/upstream", nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "模型服务连接失败") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "internal-gateway.local") || !strings.Contains(response.Body.String(), "请求编号") {
		t.Fatalf("server cause leaked or request id missing: %s", response.Body.String())
	}
}

func TestFailNotFoundDoesNotHideStorageFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/resource", func(c *gin.Context) {
		failNotFound(c, "资源不存在", errors.New("decrypt key path=/private/key"))
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resource", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "/private/key") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestPublicServiceErrorMessageRedactsOAuthFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/auth/linuxdo/callback", nil)
	message := publicServiceErrorMessage(context, errors.New("oauth database dsn=secret"), "第三方登录暂时失败")
	if strings.Contains(message, "dsn=secret") || !strings.Contains(message, "请求编号") {
		t.Fatalf("message = %q", message)
	}
}

func TestRecoveryMiddlewareReturnsSafeEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), RecoveryMiddleware())
	router.GET("/panic", func(_ *gin.Context) { panic("database password=secret") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "password=secret") {
		t.Fatalf("panic response = %d %s", response.Code, response.Body.String())
	}
}
