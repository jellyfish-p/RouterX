package middleware

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal"
	"routerx/internal/model"
)

// CORS Gin 中间件：跨域配置。
// 允许的来源从 settings 表读取 (cors.allowed_origins)。
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowedOrigins, allowCredentials := corsConfig()
		if origin != "" && originAllowed(origin, allowedOrigins, allowCredentials) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}
		if allowCredentials {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
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

func corsConfig() ([]string, bool) {
	allowed := []string{"http://localhost:5173", "http://localhost:5174"}
	if env := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); env != "" {
		allowed = splitCSV(env)
	}
	allowCredentials := true
	if internal.DB != nil {
		var setting model.Setting
		if err := internal.DB.Where("key = ?", "cors.allowed_origins").First(&setting).Error; err == nil {
			var parsed []string
			if json.Unmarshal([]byte(setting.Value), &parsed) == nil && len(parsed) > 0 {
				allowed = parsed
			}
		}
		if err := internal.DB.Where("key = ?", "cors.allow_credentials").First(&setting).Error; err == nil {
			allowCredentials = setting.Value == "true"
		}
	}
	return allowed, allowCredentials
}

func originAllowed(origin string, allowed []string, allowCredentials bool) bool {
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == origin {
			return true
		}
		if item == "*" && !allowCredentials {
			return true
		}
	}
	return false
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
