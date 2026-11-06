package cache

import "testing"

func TestRedisOptionsDefaults(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("REDIS_PASSWORD", "")
	config, err := options()
	if err != nil {
		t.Fatal(err)
	}
	if config.Addr != "localhost:6379" || config.DB != 0 || config.Password != "" {
		t.Fatalf("unexpected Redis defaults: address=%q, db=%d", config.Addr, config.DB)
	}
}

func TestRedisOptionsUseEnvironment(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis.internal:6380")
	t.Setenv("REDIS_DB", "2")
	t.Setenv("REDIS_PASSWORD", "secret with spaces")
	config, err := options()
	if err != nil {
		t.Fatal(err)
	}
	if config.Addr != "redis.internal:6380" || config.DB != 2 || config.Password != "secret with spaces" {
		t.Fatal("Redis configuration did not preserve environment values")
	}
}

func TestRedisOptionsRejectInvalidDatabase(t *testing.T) {
	for _, database := range []string{"invalid", "-1", "1.5", "99999999999999999999999999999"} {
		t.Run(database, func(t *testing.T) {
			t.Setenv("REDIS_DB", database)
			if _, err := options(); err == nil {
				t.Fatal("expected invalid REDIS_DB to fail before connecting")
			}
		})
	}
}
