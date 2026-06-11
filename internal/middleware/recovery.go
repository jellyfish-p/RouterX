package middleware

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"routerx/internal/common"
)

// Recovery Gin 中间件：Panic 恢复。
// 捕获 handler 中的 panic，记录堆栈，返回 500。
func Recovery() gin.HandlerFunc {
	// TODO: Phase 1 — 集成 Gin Recovery + 自定义错误日志
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %v", err)
				path := c.Request.URL.Path
				if strings.HasPrefix(path, "/v1/") || path == "/v1" {
					c.AbortWithStatusJSON(http.StatusInternalServerError, common.OpenAIError("internal server error", "server_error", "internal_error"))
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
