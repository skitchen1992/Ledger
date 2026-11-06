// Package cache opens the Redis connection used for short-lived report results.
package cache

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Open reads the Redis environment and checks connectivity.
// The caller owns the returned client and must close it.
func Open(ctx context.Context) (*redis.Client, error) {
	config, err := options()
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(config)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("connect to Redis: %w", err)
	}
	return client, nil
}

func options() (*redis.Options, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	database := 0
	if value := os.Getenv("REDIS_DB"); value != "" {
		var err error
		database, err = strconv.Atoi(value)
		if err != nil || database < 0 {
			return nil, fmt.Errorf("REDIS_DB must be a non-negative integer")
		}
	}
	return &redis.Options{
		Addr:                  addr,
		DB:                    database,
		Password:              os.Getenv("REDIS_PASSWORD"),
		DialTimeout:           5 * time.Second,
		ReadTimeout:           3 * time.Second,
		WriteTimeout:          3 * time.Second,
		ContextTimeoutEnabled: true,
	}, nil
}
