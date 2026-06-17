package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

// InitRedis 初始化 Redis 连接。
// 从 REDIS_CONN 环境变量读取连接字符串，启动时由 cmd/server/main.go 调用。
func InitRedis() error {
	redisConn := strings.TrimSpace(os.Getenv("REDIS_CONN"))
	if redisConn == "" {
		RDB = nil
		log.Println("[Redis] disabled: REDIS_CONN is empty")
		return nil
	}

	options, err := redis.ParseURL(redisConn)
	if err != nil {
		RDB = nil
		return fmt.Errorf("invalid REDIS_CONN: %w", err)
	}

	client := redis.NewClient(options)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		RDB = nil
		return fmt.Errorf("failed to connect redis: %w", err)
	}

	RDB = client
	log.Println("[Redis] connected")
	return nil
}
