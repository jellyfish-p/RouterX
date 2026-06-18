package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecoveryLogsRequestContextAndRedactsPanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	})

	r := gin.New()
	r.Use(Recovery())
	r.Use(Logger())
	r.GET("/panic", func(c *gin.Context) {
		panic("super-secret-token")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set("X-Request-Id", "req-panic-1")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("panic should return 500, got %d %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"success":false`) || !strings.Contains(body, `"message":"Internal Server Error"`) {
		t.Fatalf("non-v1 panic should keep standard error envelope, got %s", body)
	}

	logBody := logs.String()
	if !strings.Contains(logBody, "[PANIC]") ||
		!strings.Contains(logBody, "request_id=req-panic-1") ||
		!strings.Contains(logBody, "method=GET") ||
		!strings.Contains(logBody, "path=/panic") ||
		!strings.Contains(logBody, "client_ip=") ||
		!strings.Contains(logBody, "stack=") {
		t.Fatalf("panic log should include request context and stack, got %q", logBody)
	}
	if strings.Contains(logBody, "super-secret-token") {
		t.Fatalf("panic log should not include raw panic value, got %q", logBody)
	}
}

func TestRecoveryUsesEntryProtocolErrorEnvelopeForV1Panics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logs bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	})

	r := gin.New()
	r.Use(Recovery())
	r.Use(Logger())
	r.POST("/v1/messages", func(c *gin.Context) {
		panic("anthropic-secret")
	})
	r.POST("/v1/models/:model", func(c *gin.Context) {
		panic("gemini-secret")
	})

	anthropicReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	anthropicReq.Header.Set("anthropic-version", "2023-06-01")
	anthropicResp := httptest.NewRecorder()
	r.ServeHTTP(anthropicResp, anthropicReq)
	if anthropicResp.Code != http.StatusInternalServerError ||
		!strings.Contains(anthropicResp.Body.String(), `"type":"error"`) ||
		!strings.Contains(anthropicResp.Body.String(), `"type":"server_error"`) ||
		strings.Contains(anthropicResp.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("anthropic v1 panic should use Anthropic error envelope, got %d %s", anthropicResp.Code, anthropicResp.Body.String())
	}

	geminiReq := httptest.NewRequest(http.MethodPost, "/v1/models/gemini-test:generateContent", strings.NewReader(`{}`))
	geminiResp := httptest.NewRecorder()
	r.ServeHTTP(geminiResp, geminiReq)
	if geminiResp.Code != http.StatusInternalServerError ||
		!strings.Contains(geminiResp.Body.String(), `"code":500`) ||
		!strings.Contains(geminiResp.Body.String(), `"status":"INTERNAL"`) ||
		strings.Contains(geminiResp.Body.String(), `"type":"server_error"`) {
		t.Fatalf("gemini v1 panic should use Gemini error envelope, got %d %s", geminiResp.Code, geminiResp.Body.String())
	}

	logBody := logs.String()
	if strings.Contains(logBody, "anthropic-secret") || strings.Contains(logBody, "gemini-secret") {
		t.Fatalf("panic logs should remain redacted for protocol panics, got %q", logBody)
	}
}
