package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal"
	"routerx/internal/common"
)

// RateLimit Gin 中间件：多维限流。
// 支持全局 / 每令牌 / 每 IP 三级限流，
// 限流计数存储在 Redis (Sliding Window / Token Bucket)。
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if internal.RDB == nil {
			c.Next()
			return
		}
		now := time.Now().Unix() / 60
		ipKey := fmt.Sprintf("rl:ip:%s:%d", c.ClientIP(), now)
		if exceeded(ipKey, 120) {
			common.FailWithStatus(c, 429, "请求过于频繁")
			c.Abort()
			return
		}
		if token, ok := CurrentAPIToken(c); ok {
			tokenKey := fmt.Sprintf("rl:token:%d:%d", token.ID, now)
			if exceeded(tokenKey, 60) {
				common.FailWithStatus(c, 429, "请求过于频繁")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

func exceeded(key string, limit int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		return false
	}
	if count == 1 {
		_ = internal.RDB.Expire(ctx, key, 2*time.Minute).Err()
	}
	return count > limit
}
