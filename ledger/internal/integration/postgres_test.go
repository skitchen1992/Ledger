package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/skitchen1992/Ledger/ledger"
	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/internal/cache"
	"github.com/skitchen1992/Ledger/ledger/internal/repository/pg"
	"github.com/skitchen1992/Ledger/ledger/service"
)

// Each test owns a random schema, including its sequences. No application tables
// or other tests' Redis keys are deleted, even when a test fails.
type databaseFixture struct {
	db     *sql.DB
	dsn    string
	schema string
}

func newDatabaseFixture(t *testing.T) *databaseFixture {
	t.Helper()
	dsn := os.Getenv("LEDGER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LEDGER_TEST_DATABASE_URL to run PostgreSQL integration tests")
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
	fixture := &databaseFixture{dsn: dsn, schema: "ledger_test_" + hex.EncodeToString(token[:])}
	// The identifier contains only this fixed prefix and generated hexadecimal.
	quotedSchema := `"` + fixture.schema + `"`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("clean up test schema: %v", err)
		}
	})
	fixture.db = fixture.connect(t)
	paths, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("find actual SQL migrations: paths=%v, err=%v", paths, err)
	}
	for _, path := range paths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(migration), "-- +goose Down")
		if !ok || !strings.Contains(up, "-- +goose Up") {
			t.Fatalf("migration %s needs Goose Up and Down sections", path)
		}
		if _, err := fixture.db.ExecContext(ctx, up); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	return fixture
}

func (f *databaseFixture) connect(t *testing.T) *sql.DB {
	t.Helper()
	config, err := pgx.ParseConfig(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = f.schema
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(10)
	t.Cleanup(func() { database.Close() })
	return database
}

func TestSQLBudgetsUpsertAndValidation(t *testing.T) {
	f := newDatabaseFixture(t)
	s := newSQLService(f.db, nil)
	for _, budget := range []ledger.Budget{
		{Category: "travel", Limit: 20},
		{Category: "food", Limit: 30},
		{Category: "food", Limit: 40},
	} {
		if err := s.SetBudget(context.Background(), budget); err != nil {
			t.Fatal(err)
		}
	}
	want := []ledger.Budget{{Category: "food", Limit: 40}, {Category: "travel", Limit: 20}}
	for _, budget := range []ledger.Budget{
		{Category: "food", Limit: 0},
		{Category: "food", Limit: -1},
		{Category: "food", Limit: math.NaN()},
		{Category: "food", Limit: math.Inf(1)},
		{Category: "food", Limit: 1.001},
		{Category: "food", Limit: 1e12},
		{Category: " \t", Limit: 10},
	} {
		if err := s.SetBudget(context.Background(), budget); !errors.Is(err, service.ErrInvalidInput) {
			t.Fatalf("invalid budget %#v: expected ErrInvalidInput, got %v", budget, err)
		}
		got, err := s.ListBudgets(context.Background())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("invalid update changed budgets: got=%#v, err=%v", got, err)
		}
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM budgets WHERE category = 'food'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("UPSERT should keep one row: count=%d, err=%v", count, err)
	}
}

func TestSQLBudgetLimitIsExactAndRejectedExpenseDoesNotConsumeID(t *testing.T) {
	f := newDatabaseFixture(t)
	s := newSQLService(f.db, nil)
	if err := s.SetBudget(context.Background(), ledger.Budget{Category: "food", Limit: 0.3}); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []float64{0.1, 0.2} {
		if _, err := s.CreateTransaction(context.Background(), ledger.Transaction{Amount: amount, Category: "food", Date: "2026-01-01"}); err != nil {
			t.Fatalf("exact budget boundary should be accepted: %v", err)
		}
	}
	if _, err := s.CreateTransaction(context.Background(), ledger.Transaction{Amount: 0.01, Category: "food", Date: "2026-01-01"}); !errors.Is(err, ledger.ErrBudgetExceeded) {
		t.Fatalf("expected accumulated budget error, got %v", err)
	}
	tx, err := s.CreateTransaction(context.Background(), ledger.Transaction{ID: 999, Amount: 50, Category: "unbudgeted", Date: "2026-01-02"})
	if err != nil || tx.ID != 3 {
		t.Fatalf("unbudgeted expense should use next ID 3: tx=%#v, err=%v", tx, err)
	}
	got, err := s.ListTransactions(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("rejected expense must not be inserted: rows=%#v, err=%v", got, err)
	}
	if err := s.SetBudget(context.Background(), ledger.Budget{Category: "food", Limit: 0.2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTransaction(context.Background(), ledger.Transaction{Amount: 0.01, Category: "food", Date: "2026-01-01"}); !errors.Is(err, ledger.ErrBudgetExceeded) {
		t.Fatalf("budget update must retain previous spending: %v", err)
	}
}

func TestSQLTransactionValidationOrderingAndPersistence(t *testing.T) {
	f := newDatabaseFixture(t)
	s := newSQLService(f.db, nil)
	first := ledger.Transaction{Amount: 12.34, Category: "food", Description: "Обед: 'суп'", Date: "2026-01-02"}
	first, err := s.CreateTransaction(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []ledger.Transaction{
		{Amount: 0, Category: "food", Date: "2026-01-01"},
		{Amount: -1, Category: "food", Date: "2026-01-01"},
		{Amount: math.NaN(), Category: "food", Date: "2026-01-01"},
		{Amount: math.Inf(1), Category: "food", Date: "2026-01-01"},
		{Amount: 0.001, Category: "food", Date: "2026-01-01"},
		{Amount: 1e12, Category: "food", Date: "2026-01-01"},
		{Amount: 1, Category: " \t", Date: "2026-01-01"},
		{Amount: 1, Category: "food", Date: "2026-02-30"},
		{Amount: 1, Category: "food", Date: "2026-1-1"},
		{Amount: 1, Category: "food", Date: ""},
	}
	for _, tx := range invalid {
		if _, err := s.CreateTransaction(context.Background(), tx); !errors.Is(err, service.ErrInvalidInput) {
			t.Fatalf("invalid expense %#v: expected ErrInvalidInput, got %v", tx, err)
		}
		got, err := s.ListTransactions(context.Background())
		if err != nil || !reflect.DeepEqual(got, []ledger.Transaction{first}) {
			t.Fatalf("invalid expense changed history: got=%#v, err=%v", got, err)
		}
	}
	older, err := s.CreateTransaction(context.Background(), ledger.Transaction{Amount: 2, Category: "travel", Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	laterID, err := s.CreateTransaction(context.Background(), ledger.Transaction{Amount: 3, Category: "food", Date: "2026-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || older.ID != 2 || laterID.ID != 3 {
		t.Fatalf("validation failure consumed IDs: %d, %d, %d", first.ID, older.ID, laterID.ID)
	}
	if err := s.SetBudget(context.Background(), ledger.Budget{Category: "food", Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newSQLService(f.connect(t), nil)
	got, err := restarted.ListTransactions(context.Background())
	want := []ledger.Transaction{laterID, first, older}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("new connection must preserve fields and date DESC, id DESC: got=%#v, err=%v", got, err)
	}
	budgets, err := restarted.ListBudgets(context.Background())
	if err != nil || !reflect.DeepEqual(budgets, []ledger.Budget{{Category: "food", Limit: 100}}) {
		t.Fatalf("new connection lost budgets: got=%#v, err=%v", budgets, err)
	}
}

func TestSQLConcurrentServicesRespectBudget(t *testing.T) {
	f := newDatabaseFixture(t)
	s := newSQLService(f.db, nil)
	// Independent pools model Gateway and standalone Ledger sharing a database.
	services := []service.ReportingService{s, newSQLService(f.connect(t), nil)}
	if err := s.SetBudget(context.Background(), ledger.Budget{Category: "food", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	const attempts = 20
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for i := 0; i < attempts; i++ {
		application := services[i%len(services)]
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := application.CreateTransaction(context.Background(), ledger.Transaction{Amount: 1, Category: "food", Date: "2026-01-01"})
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ledger.ErrBudgetExceeded):
			rejected++
		default:
			t.Errorf("unexpected concurrent insertion error: %v", err)
		}
	}
	if accepted != 10 || rejected != 10 {
		t.Fatalf("expected 10 accepted and 10 rejected, got %d and %d", accepted, rejected)
	}
	var count int
	var amount float64
	if err := f.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(amount),0) FROM expenses").Scan(&count, &amount); err != nil {
		t.Fatal(err)
	}
	if count != 10 || amount != 10 {
		t.Fatalf("concurrent writes exceeded budget: count=%d, amount=%v", count, amount)
	}
}

func seedReport(t *testing.T, s service.ReportingService) domain.ReportSummary {
	t.Helper()
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	from := time.Date(4000+int(binary.BigEndian.Uint16(token[:2])%4000), time.Month(1+token[2]%12), 1+int(token[3]%28), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(1+int(binary.BigEndian.Uint16(token[4:6])%1000), 0, int(token[6]%28))
	for _, tx := range []ledger.Transaction{
		{Amount: 1.25, Category: "alpha", Date: from.Format(time.DateOnly)},
		{Amount: 2.5, Category: "alpha", Date: to.Format(time.DateOnly)},
		{Amount: 3.25, Category: "beta", Date: from.AddDate(0, 0, 1).Format(time.DateOnly)},
		{Amount: 100, Category: "outside", Date: from.AddDate(0, 0, -1).Format(time.DateOnly)},
		{Amount: 100, Category: "outside", Date: to.AddDate(0, 0, 1).Format(time.DateOnly)},
	} {
		if _, err := s.CreateTransaction(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
	}
	return domain.ReportSummary{
		From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Total: 7,
		Categories: []domain.CategoryTotal{{Category: "alpha", Amount: 3.75}, {Category: "beta", Amount: 3.25}},
	}
}

func TestSQLReportInclusiveRangeAndInvalidInput(t *testing.T) {
	f := newDatabaseFixture(t)
	s := newSQLService(f.db, nil)
	want := seedReport(t, s)
	got, err := s.GetReportSummary(context.Background(), want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("inclusive report: got=%#v, want=%#v, err=%v", got, want, err)
	}
	empty, err := s.GetReportSummary(context.Background(), "1900-01-01", "1900-01-01")
	if err != nil || empty.Total != 0 || empty.Categories == nil || len(empty.Categories) != 0 {
		t.Fatalf("empty range should return zero and an empty category array: got=%#v, err=%v", empty, err)
	}
	for _, dates := range [][2]string{{"bad", want.To}, {want.From, "2026-02-30"}, {want.To, want.From}} {
		if _, err := s.GetReportSummary(context.Background(), dates[0], dates[1]); !errors.Is(err, service.ErrInvalidInput) {
			t.Fatalf("invalid range %v: expected ErrInvalidInput, got %v", dates, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetReportSummary(ctx, want.From, want.To); !errors.Is(err, context.Canceled) {
		t.Fatalf("report must preserve canceled context error, got %v", err)
	}
}

func TestRedisReportCacheTTLHitRefreshAndFallback(t *testing.T) {
	addr := os.Getenv("LEDGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set LEDGER_TEST_REDIS_ADDR to run Redis cache integration tests")
	}
	f := newDatabaseFixture(t)
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	s := newSQLService(f.db, client)
	want := seedReport(t, s)
	key := "report:summary:" + want.From + ":" + want.To
	if exists, err := client.Exists(ctx, key).Result(); err != nil || exists != 0 {
		t.Fatalf("test must own an unused random report key: exists=%d, err=%v", exists, err)
	}
	t.Cleanup(func() {
		if err := client.Del(context.Background(), key).Err(); err != nil {
			t.Errorf("clean up own report cache key: %v", err)
		}
	})
	got, err := s.GetReportSummary(ctx, want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cache miss report: got=%#v, err=%v", got, err)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > 30*time.Second {
		t.Fatalf("expected positive TTL up to 30s, got %v, err=%v", ttl, err)
	}
	// Change only this schema directly to distinguish a cache hit from recomputation.
	if _, err := f.db.Exec("UPDATE expenses SET amount = amount + 1 WHERE date = $1", want.From); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetReportSummary(ctx, want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cache hit should preserve cached value: got=%#v, err=%v", got, err)
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	want.Total = 8
	want.Categories[0].Amount = 4.75
	got, err = s.GetReportSummary(ctx, want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cache miss should refresh from PostgreSQL: got=%#v, err=%v", got, err)
	}
	if err := client.Set(ctx, key, "{broken JSON", 30*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetReportSummary(ctx, want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("malformed cache should fall back to PostgreSQL: got=%#v, err=%v", got, err)
	}
	cached, err := client.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var repaired domain.ReportSummary
	if err := json.Unmarshal(cached, &repaired); err != nil || !reflect.DeepEqual(repaired, want) {
		t.Fatalf("malformed cache must be replaced with valid report: got=%#v, err=%v", repaired, err)
	}
	closedClient := redis.NewClient(&redis.Options{Addr: addr})
	if err := closedClient.Close(); err != nil {
		t.Fatal(err)
	}
	uncached := newSQLService(f.db, closedClient)
	got, err = uncached.GetReportSummary(ctx, want.From, want.To)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Redis outage should fall back to PostgreSQL: got=%#v, err=%v", got, err)
	}
}

func newSQLService(db *sql.DB, client *redis.Client) service.ReportingService {
	repo := pg.New(db)
	return service.NewReporting(service.NewStrict(repo, repo, repo), repo, cache.NewReportCache(client))
}
