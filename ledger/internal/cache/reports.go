package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/skitchen1992/Ledger/ledger/domain"
)

type reportCache struct{ client *redis.Client }

func NewReportCache(client *redis.Client) domain.ReportCache {
	if client == nil {
		return nil
	}
	return &reportCache{client: client}
}

func (c *reportCache) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, domain.ErrCacheMiss
	}
	return data, err
}

func (c *reportCache) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, data, ttl).Err()
}
