package middleware

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Recovery Gin 中间件：Panic 恢复。
// 捕获 handler 中的 panic，记录堆栈，返回 500。
func Recovery() gin.HandlerFunc {
	// TODO: Phase 1 — 集成 Gin Recovery + 自定义错误日志
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %v", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": "Internal Server Error",
				})
			}
		}()

		c.Next()
	}
}
