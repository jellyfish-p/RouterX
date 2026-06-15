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
		if cfg.globalPerMin > 0 && exceeded(fmt.Sprintf("rl:global:%d", now), cfg.globalPerMin) {
			writeRateLimitError(c, "global")
			c.Abort()
			return
		}
		if cfg.perIPPerMin > 0 && exceeded(fmt.Sprintf("rl:ip:%s:%d", c.ClientIP(), now), cfg.perIPPerMin) {
			writeRateLimitError(c, "ip")
			c.Abort()
			return
		}
		if token, ok := CurrentAPIToken(c); ok {
			if cfg.perTokenPerMin > 0 && exceeded(fmt.Sprintf("rl:token:%d:%d", token.ID, now), cfg.perTokenPerMin) {
				writeRateLimitError(c, "token")
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
	return cfg
}

func writeRateLimitError(c *gin.Context, dimension string) {
	recordRateLimitDeniedPolicyLog(c, dimension)
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

func recordRateLimitDeniedPolicyLog(c *gin.Context, dimension string) {
	token, ok := CurrentAPIToken(c)
	if !ok {
		return
	}
	scopeResult := policyDeniedScopeResult("rate_limit")
	scopeResult["rate_limit_dimension"] = dimension
	service.NewTokenService().RecordScopeDeniedPolicyLog(token, dimension+" rate limit exceeded", c.ClientIP(), c.GetHeader("User-Agent"), c.GetString("request_id"), "rate_limit_exceeded", "rate_limit_exceeded", scopeResult)
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
