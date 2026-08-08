package main

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestAllowedOriginWildcardFailsClosed(t *testing.T) {
	t.Setenv("CANVAS_CORS_ORIGINS", "*")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "http://backend/api/health", nil)
	if allowedOrigin(context, "https://example.com") {
		t.Fatal("credentialed CORS wildcard must fail closed")
	}
	if err := validateCORSOrigins("*"); err == nil {
		t.Fatal("startup validation should reject wildcard CORS")
	}
}

func TestValidateCORSOriginsRequiresExactOrigins(t *testing.T) {
	if err := validateCORSOrigins("https://canvas.example.com, http://localhost:3000/"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"https://example.com/path", "https://user@example.com", "https://example.com?debug=1", "ftp://example.com"} {
		if err := validateCORSOrigins(value); err == nil {
			t.Fatalf("expected invalid CORS origin %q", value)
		}
	}
}

func TestAllowedOriginDoesNotTrustForwardedHost(t *testing.T) {
	t.Setenv("CANVAS_CORS_ORIGINS", "")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "http://backend/api/health", nil)
	context.Request.Header.Set("X-Forwarded-Host", " canvas.example.com, proxy.internal")
	if allowedOrigin(context, "https://canvas.example.com") {
		t.Fatal("client-controlled forwarded host must not bypass credentialed CORS")
	}
}

func TestAllowedOriginRequiresSameSchemeAndHost(t *testing.T) {
	t.Setenv("CANVAS_CORS_ORIGINS", "")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "http://backend/api/health", nil)
	if !allowedOrigin(context, "http://backend") {
		t.Fatal("matching request scheme and host should be same-origin")
	}
	if allowedOrigin(context, "https://backend") {
		t.Fatal("cross-scheme origin must not be treated as same-origin")
	}
	context.Request.Header.Set("X-Forwarded-Proto", "https")
	if !allowedOrigin(context, "https://backend") {
		t.Fatal("trusted proxy HTTPS scheme should match the public origin")
	}
}

func TestAllowedOriginRequiresExplicitLocalCrossPort(t *testing.T) {
	t.Setenv("CANVAS_CORS_ORIGINS", "")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "http://localhost:8080/api/health", nil)
	if allowedOrigin(context, "http://localhost:5173") {
		t.Fatal("a different localhost port must be configured explicitly")
	}
	t.Setenv("CANVAS_CORS_ORIGINS", "http://localhost:5173")
	if !allowedOrigin(context, "http://localhost:5173") {
		t.Fatal("an exact configured localhost origin should be allowed")
	}
}

func TestRedactCanvasSharePath(t *testing.T) {
	got := redactCanvasSharePath("/api/public/canvas-shares/private-token/resources/resource-1/file")
	if got != "/api/public/canvas-shares/:token/resources/resource-1/file" {
		t.Fatalf("unexpected redacted path: %s", got)
	}
	if got := redactCanvasSharePath("/api/tasks"); got != "/api/tasks" {
		t.Fatalf("unrelated path changed: %s", got)
	}
}

func TestParseShutdownDrainTimeout(t *testing.T) {
	if got, err := parseShutdownDrainTimeout(""); err != nil || got != 40*time.Minute {
		t.Fatalf("default shutdown drain timeout = %s, %v", got, err)
	}
	if got, err := parseShutdownDrainTimeout(" 90s "); err != nil || got != 90*time.Second {
		t.Fatalf("configured shutdown drain timeout = %s, %v", got, err)
	}
	for _, value := range []string{"invalid", "59s", "168h"} {
		if _, err := parseShutdownDrainTimeout(value); err == nil {
			t.Fatalf("expected invalid shutdown drain timeout %q", value)
		}
	}
}

func TestValidateContainerStopGracePeriod(t *testing.T) {
	for _, value := range []string{"", "41m", "45m", "1h"} {
		if err := validateContainerStopGracePeriod(value, 40*time.Minute); err != nil {
			t.Fatalf("validateContainerStopGracePeriod(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"invalid", "39m", "40m", "40m30s", "169h"} {
		if err := validateContainerStopGracePeriod(value, 40*time.Minute); err == nil {
			t.Fatalf("expected invalid container stop grace period %q", value)
		}
	}
}
