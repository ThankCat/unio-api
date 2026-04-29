package redis

import (
	"context"
	"fmt"

	"github.com/ThankCat/unio-api/internal/config"
	"github.com/redis/go-redis/v9"
)

// OpenRedis 创建 Redis client，并在启动期做一次 ping。
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
