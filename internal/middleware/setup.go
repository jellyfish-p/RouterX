package middleware

import (
	"github.com/gin-gonic/gin"
	"routerx/internal"
)

// SetupCheck Gin 中间件：系统初始化状态检查。
// 系统未初始化时，拦截所有非 /v0/setup/* 和 /health 的请求。
func SetupCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if path == "/v0/setup/status" || path == "/v0/setup/init" || path == "/health" {
			c.Next()
			return
		}

		if c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		if !internal.IsInitialized() {
			c.AbortWithStatusJSON(200, gin.H{
				"success": false,
				"message": "系统尚未初始化，请先完成首次设置",
			})
			return
		}

		c.Next()
	}
}
