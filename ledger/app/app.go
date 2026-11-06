// Package app assembles ready-to-use Ledger services and owns their resources.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/skitchen1992/Ledger/ledger/internal/cache"
	"github.com/skitchen1992/Ledger/ledger/internal/db"
	"github.com/skitchen1992/Ledger/ledger/internal/repository/pg"
	"github.com/skitchen1992/Ledger/ledger/service"
)

// New reads the database environment and returns the service used by Gateway.
// The caller must call closeFn after a successful initialization. Migrations must
// be applied separately before startup; the factory only checks the schema.
func New(ctx context.Context) (service.Service, func(), error) {
	pool, err := openDatabase(ctx)
	if err != nil {
		return nil, nil, err
	}
	repository := pg.New(pool)
	return service.New(repository, repository, repository), func() { pool.Close() }, nil
}

// NewReporting adds the Redis report cache and the standalone Ledger service's
// stricter transaction validation. It returns ownership of both resources through
// closeFn only after every startup check succeeds.
func NewReporting(ctx context.Context) (service.ReportingService, func(), error) {
	pool, err := openDatabase(ctx)
	if err != nil {
		return nil, nil, err
	}
	redisClient, err := cache.Open(ctx)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	log.Print("Ledger connected to Redis")
	repository := pg.New(pool)
	base := service.NewStrict(repository, repository, repository)
	reports := service.NewReporting(base, repository, cache.NewReportCache(redisClient))
	closeFn := func() {
		redisClient.Close()
		pool.Close()
	}
	return reports, closeFn, nil
}

func openDatabase(ctx context.Context) (*sql.DB, error) {
	pool, err := db.Open(ctx)
	if err != nil {
		return nil, err
	}
	log.Print("Ledger connected to PostgreSQL")
	if err := checkSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func checkSchema(ctx context.Context, pool *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, query := range []string{
		"SELECT id, category, limit_amount FROM budgets LIMIT 0",
		"SELECT id, amount, category, description, date FROM expenses LIMIT 0",
	} {
		rows, err := pool.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("check Ledger schema (apply Goose migrations before starting): %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close Ledger schema query: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read Ledger schema query: %w", err)
		}
	}
	// The old schema has the same column names, but rounds money and rejects
	// Gateway's free-form dates. Refuse startup until compatibility is migrated.
	var compatible bool
	err := pool.QueryRowContext(ctx, `SELECT count(*) = 3 FROM pg_attribute
		WHERE NOT attisdropped AND atttypmod = -1 AND (
			(attrelid = 'budgets'::regclass AND attname = 'limit_amount' AND atttypid = 'numeric'::regtype)
			OR (attrelid = 'expenses'::regclass AND attname = 'amount' AND atttypid = 'numeric'::regtype)
			OR (attrelid = 'expenses'::regclass AND attname = 'date' AND atttypid = 'text'::regtype)
		)`).Scan(&compatible)
	if err != nil {
		return fmt.Errorf("check Ledger schema types (apply Goose migrations before starting): %w", err)
	}
	if !compatible {
		return fmt.Errorf("incompatible Ledger schema: apply Goose migrations before starting (including 00003_gateway_compatibility.sql)")
	}
	return nil
}
