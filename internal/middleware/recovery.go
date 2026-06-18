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
				log.Printf("[PANIC] request_id=%s method=%s path=%s client_ip=%s panic_type=%s stack=%s",
					c.GetString("request_id"),
					c.Request.Method,
					path,
					c.ClientIP(),
					fmt.Sprintf("%T", err),
					string(debug.Stack()),
				)
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
