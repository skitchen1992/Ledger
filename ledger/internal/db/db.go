// Package db opens the PostgreSQL connection used by Ledger.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open reads the database environment, configures the pool, and checks connectivity.
// The caller owns the returned connection and must close it.
func Open(ctx context.Context) (*sql.DB, error) {
	dsn, err := connectionString()
	if err != nil {
		return nil, err
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL connection: %w", err)
	}
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.PingContext(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

func connectionString() (string, error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}
	port := envOrDefault("DB_PORT", "5432")
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("DB_PORT must be an integer between 1 and 65535")
	}
	dsn := url.URL{
		Scheme: "postgres",
		User: url.UserPassword(
			envOrDefault("DB_USER", "postgres"),
			envOrDefault("DB_PASS", "postgres"),
		),
		Host:     net.JoinHostPort(envOrDefault("DB_HOST", "localhost"), port),
		Path:     "/" + envOrDefault("DB_NAME", "cashapp"),
		RawQuery: "sslmode=disable",
	}
	return dsn.String(), nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
