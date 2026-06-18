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
