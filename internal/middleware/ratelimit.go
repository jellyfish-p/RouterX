package middleware

import (
	"github.com/gin-gonic/gin"
)

// RateLimit Gin 中间件：多维限流。
// 支持全局 / 每令牌 / 每 IP 三级限流，
// 限流计数存储在 Redis (Sliding Window / Token Bucket)。
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 8 实现
		// 1. 从 context 获取 token (可选)
		// 2. 从 SettingService 读取限流配置
		// 3. Redis 滑动窗口计数
		// 4. 超限返回 429 Too Many Requests
		c.Next()
	}
}
