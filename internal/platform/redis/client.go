package redis

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/redis/go-redis/v9"
)

// OpenRedis 创建 Redis client，并在启动期做一次 ping。
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:            cfg.Addr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		DialTimeout:     cfg.DialTimeout,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		PoolSize:        cfg.PoolSize,
		MaxRetries:      cfg.MaxRetries,
		MinRetryBackoff: cfg.MinRetryBackoff,
		MaxRetryBackoff: cfg.MaxRetryBackoff,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, failure.Wrap(
			failure.CodeDependencyRedisUnavailable,
			err,
			failure.WithMessage("ping redis"),
		)
	}

	return client, nil
}
