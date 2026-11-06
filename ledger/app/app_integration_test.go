package app_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/skitchen1992/Ledger/ledger/app"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func TestFactoryDoesNotMigrateAndClosesFailedStartup(t *testing.T) {
	f := newFactoryDatabase(t)
	for _, factory := range factories() {
		t.Run(factory.name, func(t *testing.T) {
			ledgerService, closeFn, err := factory.new(context.Background())
			if ledgerService != nil || closeFn != nil || err == nil || !strings.Contains(err.Error(), "apply Goose migrations") {
				t.Fatalf("factory on empty schema returned service=%v, closeFn present=%t, err=%v", ledgerService, closeFn != nil, err)
			}
			f.assertNoFactoryConnections(t)
		})
	}
	var tableCount int
	if err := f.admin.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = $1", f.name).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatalf("factory changed the empty schema: found %d tables", tableCount)
	}
}

func TestGatewayFactoryNeedsOnlyPostgresAndReturnsCloseFunction(t *testing.T) {
	f := newFactoryDatabase(t)
	f.applyMigrations(t, 0)
	t.Setenv("REDIS_DB", "invalid")
	ledgerService, closeFn, err := app.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	if err := ledgerService.SetBudget(context.Background(), service.Budget{Category: "factory", Limit: 12}); err != nil {
		t.Fatal(err)
	}
	budgets, err := ledgerService.ListBudgets(context.Background())
	if err != nil || len(budgets) != 1 || budgets[0].Category != "factory" || budgets[0].Limit != 12 {
		t.Fatalf("assembled service did not persist budget: budgets=%v, err=%v", budgets, err)
	}
	closeFn()
	if _, err := ledgerService.ListBudgets(context.Background()); err == nil {
		t.Fatal("service still reads PostgreSQL after closeFn")
	}
	f.assertNoFactoryConnections(t)
}

func TestReportingFactoryClosesPostgresIfRedisInitializationFails(t *testing.T) {
	f := newFactoryDatabase(t)
	f.applyMigrations(t, 0)
	t.Setenv("REDIS_DB", "invalid")
	ledgerService, closeFn, err := app.NewReporting(context.Background())
	if ledgerService != nil || closeFn != nil || err == nil || !strings.Contains(err.Error(), "REDIS_DB") {
		t.Fatalf("factory returned service=%v, closeFn present=%t, err=%v; want only Redis configuration error", ledgerService, closeFn != nil, err)
	}
	f.assertNoFactoryConnections(t)
}

func TestFactoryRefusesUnmigratedCompatibilitySchema(t *testing.T) {
	f := newFactoryDatabase(t)
	f.applyMigrations(t, 2)
	for _, factory := range factories() {
		t.Run(factory.name, func(t *testing.T) {
			application, closeFn, err := factory.new(context.Background())
			if err == nil || !strings.Contains(err.Error(), "00003_gateway_compatibility") || application != nil || closeFn != nil {
				t.Fatalf("old schema must fail before accepting/rounding Gateway input: service=%v, closeFn=%t, err=%v", application, closeFn != nil, err)
			}
			f.assertNoFactoryConnections(t)
		})
	}
	// Failed startup must neither migrate nor change existing SQL constraints.
	var dateType string
	if err := f.setup.QueryRow("SELECT atttypid::regtype::text FROM pg_attribute WHERE attrelid = 'expenses'::regclass AND attname = 'date'").Scan(&dateType); err != nil || dateType != "date" {
		t.Fatalf("factory changed schema: type=%q err=%v", dateType, err)
	}
}

type factoryDatabase struct {
	admin *sql.DB
	setup *sql.DB
	name  string
}

func newFactoryDatabase(t *testing.T) *factoryDatabase {
	t.Helper()
	dsn := os.Getenv("LEDGER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LEDGER_TEST_DATABASE_URL to run factory PostgreSQL integration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "ledger_factory_" + hex.EncodeToString(token[:])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA "`+name+`" CASCADE`); err != nil {
			t.Errorf("clean up factory test schema: %v", err)
		}
	})
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = name
	config.RuntimeParams["application_name"] = name + "_setup"
	setup := stdlib.OpenDB(*config)
	t.Cleanup(func() { setup.Close() })
	config = config.Copy()
	config.RuntimeParams["application_name"] = name
	registered := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(registered) })
	t.Setenv("DATABASE_URL", registered)
	return &factoryDatabase{admin: admin, setup: setup, name: name}
}

func (f *factoryDatabase) applyMigrations(t *testing.T, through int) {
	t.Helper()
	paths, err := filepath.Glob("../migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("find actual SQL migrations: paths=%v, err=%v", paths, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, path := range paths {
		if through > 0 && i >= through {
			break
		}
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(migration), "-- +goose Down")
		if !ok || !strings.Contains(up, "-- +goose Up") {
			t.Fatalf("migration %s needs Goose Up and Down sections", path)
		}
		if _, err := f.setup.ExecContext(ctx, up); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
}

func (f *factoryDatabase) assertNoFactoryConnections(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var active int
		if err := f.admin.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = $1", f.name).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("factory left %d PostgreSQL connections open", active)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
