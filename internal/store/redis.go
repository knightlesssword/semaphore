package store

import (
	"context"
	"fmt"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/redis/go-redis/v9"
)

// NewRedisClient creates a Redis client from cfg and verifies connectivity
// with a ping. Returns an error if Redis is unreachable.
func NewRedisClient(cfg *config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	if err := client.Ping(context.Background()).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping %s: %w", cfg.Addr, err)
	}

	return client, nil
}
