package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

// CORS Gin 中间件：跨域配置。
// 允许的来源从 settings 表读取 (cors.allowed_origins)。
func CORS() gin.HandlerFunc {
	// TODO: Phase 1 实现 — 从 SettingService 读取 CORS 配置
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "http://localhost:5173")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// CORSConfig 允许从 SettingService 动态加载的 CORS 配置。
type CORSConfig struct {
	AllowedOrigins   []string      `json:"allowed_origins"`
	AllowCredentials bool          `json:"allow_credentials"`
	MaxAge           time.Duration `json:"max_age"`
}
