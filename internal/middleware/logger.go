package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// Logger Gin 中间件：结构化请求日志。
// 记录每个请求的 method / path / status / latency / client_ip。
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := c.GetHeader("X-Request-Id")
		if requestID == "" {
			if generated, err := common.GenerateRandomString(8); err == nil {
				requestID = generated
			}
		}
		if requestID != "" {
			c.Set("request_id", requestID)
			c.Header("X-Request-Id", requestID)
		}

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		log.Printf("[HTTP] %s %s | %d | %v | %s | request_id=%s",
			c.Request.Method,
			c.Request.URL.Path,
			status,
			latency,
			c.ClientIP(),
			requestID,
		)
	}
}
