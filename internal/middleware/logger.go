package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger Gin 中间件：结构化请求日志。
// 记录每个请求的 method / path / status / latency / client_ip。
func Logger() gin.HandlerFunc {
	// TODO: Phase 1 — 集成 Zap 结构化日志
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		log.Printf("[HTTP] %s %s | %d | %v | %s",
			c.Request.Method,
			c.Request.URL.Path,
			status,
			latency,
			c.ClientIP(),
		)
	}
}
