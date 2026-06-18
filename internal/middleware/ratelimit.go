package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"routerx/internal"
	"routerx/internal/common"
	"routerx/internal/service"
)

type rateLimitConfig struct {
	enabled        bool
	globalPerMin   int64
	perTokenPerMin int64
	perIPPerMin    int64
	perUserPerMin  int64
}

// RateLimit Gin 中间件：基于 Redis 的分钟级多维限流。
// rate_limit.* 从 settings 热读取；任一维度配置为 0 时跳过该维度。
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if internal.RDB == nil {
			c.Next()
			return
		}
		cfg := loadRateLimitConfig()
		if !cfg.enabled {
			c.Next()
			return
		}
		now := time.Now().Unix() / 60
		if hit, current := rateLimitExceeded(fmt.Sprintf("rl:global:%d", now), cfg.globalPerMin); cfg.globalPerMin > 0 && hit {
			writeRateLimitError(c, "global", cfg.globalPerMin, current)
			c.Abort()
			return
		}
		if hit, current := rateLimitExceeded(fmt.Sprintf("rl:ip:%s:%d", c.ClientIP(), now), cfg.perIPPerMin); cfg.perIPPerMin > 0 && hit {
			writeRateLimitError(c, "ip", cfg.perIPPerMin, current)
			c.Abort()
			return
		}
		if token, ok := CurrentAPIToken(c); ok {
			if hit, current := rateLimitExceeded(fmt.Sprintf("rl:token:%d:%d", token.ID, now), cfg.perTokenPerMin); cfg.perTokenPerMin > 0 && hit {
				writeRateLimitError(c, "token", cfg.perTokenPerMin, current)
				c.Abort()
				return
			}
			if hit, current := rateLimitExceeded(fmt.Sprintf("rl:user:%d:%d", token.UserID, now), cfg.perUserPerMin); cfg.perUserPerMin > 0 && token.UserID > 0 && hit {
				writeRateLimitError(c, "user", cfg.perUserPerMin, current)
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

func loadRateLimitConfig() rateLimitConfig {
	cfg := rateLimitConfig{
		enabled:        true,
		globalPerMin:   1000,
		perTokenPerMin: 60,
		perIPPerMin:    30,
		perUserPerMin:  0,
	}
	if internal.DB == nil {
		return cfg
	}
	settingSvc := service.NewSettingService()
	if enabled, err := settingSvc.GetBool("rate_limit.enabled"); err == nil {
		cfg.enabled = enabled
	}
	if limit, err := settingSvc.GetInt("rate_limit.global_per_min"); err == nil && limit >= 0 {
		cfg.globalPerMin = int64(limit)
	}
	if limit, err := settingSvc.GetInt("rate_limit.per_token_per_min"); err == nil && limit >= 0 {
		cfg.perTokenPerMin = int64(limit)
	}
	if limit, err := settingSvc.GetInt("rate_limit.per_ip_per_min"); err == nil && limit >= 0 {
		cfg.perIPPerMin = int64(limit)
	}
	if limit, err := settingSvc.GetInt("rate_limit.per_user_per_min"); err == nil && limit >= 0 {
		cfg.perUserPerMin = int64(limit)
	}
	return cfg
}

func writeRateLimitError(c *gin.Context, dimension string, limit, current int64) {
	recordRateLimitDeniedPolicyLog(c, dimension, limit, current)
	switch entryProtocol(c) {
	case "anthropic":
		c.JSON(http.StatusTooManyRequests, common.AnthropicError("rate limit exceeded", "rate_limit_error"))
	case "gemini":
		c.JSON(http.StatusTooManyRequests, common.GeminiError(http.StatusTooManyRequests, "rate limit exceeded", geminiStatusText(http.StatusTooManyRequests)))
	case "openai":
		c.JSON(http.StatusTooManyRequests, common.OpenAIError("rate limit exceeded", "rate_limit_error", "rate_limit_exceeded"))
	default:
		common.FailWithStatus(c, http.StatusTooManyRequests, "请求过于频繁")
	}
}

func recordRateLimitDeniedPolicyLog(c *gin.Context, dimension string, limit, current int64) {
	token, ok := CurrentAPIToken(c)
	if !ok {
		return
	}
	service.NewTokenService().RecordRateLimitDeniedPolicyLog(token, dimension, limit, current, c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"))
}

func rateLimitExceeded(key string, limit int64) (bool, int64) {
	if limit <= 0 {
		return false, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	count, err := internal.RDB.Incr(ctx, key).Result()
	if err != nil {
		return false, 0
	}
	if count == 1 {
		_ = internal.RDB.Expire(ctx, key, 2*time.Minute).Err()
	}
	return count > limit, count
}
