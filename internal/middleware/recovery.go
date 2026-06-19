package middleware

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// Recovery Gin 中间件：Panic 恢复。
// 捕获 handler 中的 panic，记录脱敏上下文和堆栈，并返回协议兼容的 500。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				path := c.Request.URL.Path
				requestID := c.GetString("request_id")
				panicType := fmt.Sprintf("%T", err)
				stack := string(debug.Stack())
				if common.StructuredLogsEnabled() {
					entry := map[string]interface{}{
						"event":      "panic",
						"request_id": requestID,
						"method":     c.Request.Method,
						"path":       path,
						"client_ip":  c.ClientIP(),
						"panic_type": panicType,
						"stack":      stack,
					}
					if traceID := c.GetString("trace_id"); traceID != "" {
						entry["trace_id"] = traceID
						entry["traceparent"] = c.GetString("traceparent")
					}
					writeStructuredLog(entry, func() {
						writeTextPanicLog(requestID, c.Request.Method, path, c.ClientIP(), panicType, stack)
					})
				} else {
					writeTextPanicLog(requestID, c.Request.Method, path, c.ClientIP(), panicType, stack)
				}
				if strings.HasPrefix(path, "/v1/") || path == "/v1" {
					abortV1Panic(c)
					return
				}
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": "Internal Server Error",
				})
			}
		}()

		c.Next()
	}
}

func writeTextPanicLog(requestID, method, path, clientIP, panicType, stack string) {
	log.Printf("[PANIC] request_id=%s method=%s path=%s client_ip=%s panic_type=%s stack=%s",
		requestID,
		method,
		path,
		clientIP,
		panicType,
		stack,
	)
}

func abortV1Panic(c *gin.Context) {
	const message = "internal server error"
	switch entryProtocol(c) {
	case "anthropic":
		c.AbortWithStatusJSON(http.StatusInternalServerError, common.AnthropicError(message, "server_error"))
	case "gemini":
		c.AbortWithStatusJSON(http.StatusInternalServerError, common.GeminiError(http.StatusInternalServerError, message, geminiStatusText(http.StatusInternalServerError)))
	default:
		c.AbortWithStatusJSON(http.StatusInternalServerError, common.OpenAIError(message, "server_error", "internal_error"))
	}
}
