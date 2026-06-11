package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

// InitRedis 初始化 Redis 连接。
// 从 REDIS_CONN 环境变量读取连接字符串，启动时由 cmd/server/main.go 调用。
func InitRedis() error {
	redisConn := os.Getenv("REDIS_CONN")
	if redisConn == "" {
		redisConn = "redis://localhost:6379/0"
	}

	options, err := redis.ParseURL(redisConn)
	if err != nil {
		return fmt.Errorf("invalid REDIS_CONN: %w", err)
	}

	RDB = redis.NewClient(options)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := RDB.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect redis: %w", err)
	}

	log.Println("[Redis] connected")
	return nil
}
