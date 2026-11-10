package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/internal/repository/pg"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func TestSQLReportCategoryQueriesPreserveDatesTextAndExactAmounts(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	ctx := context.Background()
	for _, expense := range []domain.Transaction{
		{Category: "food", Amount: 0.1, Date: "2026-11-01"},
		{Category: "food", Amount: 0.2, Date: "2026-11-30"},
		{Category: "food", Amount: 100, Date: "2026-10-31"},
		{Category: "food", Amount: 100, Date: "2026-12-01"},
		{Category: "A", Amount: 1, Date: "2026-11-02"},
		{Category: "a", Amount: 2, Date: "2026-11-02"},
		{Category: `literal\0`, Amount: 0.002, Date: "2026-11-02"},
		{Category: "nul\x00category", Amount: 0.005, Date: "2026-11-02"},
		{Category: "nul\x00category", Amount: 0.006, Date: "2026-11-03"},
		{Category: `path\folder`, Amount: 3, Date: "2026-11-02"},
		{Category: "outside", Amount: 100, Date: "2026-10-31"},
	} {
		if _, err := repo.InsertTransaction(ctx, expense); err != nil {
			t.Fatal(err)
		}
	}
	for _, date := range []string{"", "today", "2026-02-30", "2026-1-02", "2026-11-02 ", "2026-00-01", "2026-11-02\x00"} {
		for _, category := range []string{"food", "invalid dates only"} {
			if _, err := repo.InsertTransaction(ctx, domain.Transaction{Category: category, Amount: 100, Date: date}); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := repo.ListReportCategories(ctx, "2026-11-01", "2026-11-30")
	want := []string{"A", "a", "food", `literal\0`, "nul\x00category", `path\folder`}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("report categories must be decoded, sorted and restricted to valid dates in the period: got=%q, err=%v", got, err)
	}
	for _, tc := range []struct {
		category, amount string
	}{
		{"food", "3/10"},
		{`literal\0`, "1/500"},
		{"nul\x00category", "11/1000"},
		{`path\folder`, "3"},
		{"invalid dates only", "0"},
		{"missing", "0"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			got, err := repo.SumReportCategory(ctx, tc.category, "2026-11-01", "2026-11-30")
			want, _ := new(big.Rat).SetString(tc.amount)
			if err != nil || got == nil || got.Cmp(want) != 0 {
				t.Fatalf("exact sum = %v, want %v, err=%v", got, want, err)
			}
		})
	}
	empty, err := repo.ListReportCategories(ctx, "1900-01-01", "1900-01-01")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty period categories = %#v, err=%v", empty, err)
	}
}

func TestSQLReportQueriesCancelWhileWaitingForTableLock(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	lock, err := f.db.BeginTx(setupCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(setupCtx, "LOCK TABLE expenses IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	// Prepare another connection so the queries encounter a held PostgreSQL lock,
	// rather than merely exhausting their deadline opening a connection.
	if err := f.db.PingContext(setupCtx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"category list", func(ctx context.Context) error {
			_, err := repo.ListReportCategories(ctx, "2026-11-01", "2026-11-30")
			return err
		}},
		{"category sum", func(ctx context.Context) error {
			_, err := repo.SumReportCategory(ctx, "food", "2026-11-01", "2026-11-30")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked query must preserve context deadline: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("query remained blocked after its request deadline")
			}
			if inUse := f.db.Stats().InUse; inUse != 1 {
				t.Fatalf("canceled report query retained a connection: in use=%d, want only the lock transaction", inUse)
			}
		})
	}
}

func TestSQLReportCancellationJoinsWorkersAndReleasesConnections(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	for _, category := range []string{"alpha", "beta", "gamma"} {
		if _, err := repo.InsertTransaction(setupCtx, domain.Transaction{Category: category, Amount: 0.1, Date: "2026-11-10"}); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := f.db.BeginTx(setupCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	// Listing uses the real repository, then takes a table lock before category
	// workers begin. All worker sums must wait in PostgreSQL until cancellation.
	blocked := &reportWithLockedSums{Repository: repo, lock: lock}
	reports := service.NewReporting(service.NewStrict(repo, repo, repo), blocked, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reports.GetReportSummary(ctx, "2026-11-01", "2026-11-30")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked report must return its request deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("report workers remained blocked after request cancellation")
	}
	if inUse := f.db.Stats().InUse; inUse != 1 {
		t.Fatalf("report returned with worker SQL connections still in use: %d", inUse)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	reports = service.NewReporting(service.NewStrict(repo, repo, repo), repo, nil)
	got, err := reports.GetReportSummary(setupCtx, "2026-11-01", "2026-11-30")
	want := domain.ReportSummary{From: "2026-11-01", To: "2026-11-30", Total: 0.3,
		Categories: []domain.CategoryTotal{{Category: "alpha", Amount: 0.1}, {Category: "beta", Amount: 0.1}, {Category: "gamma", Amount: 0.1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh report after canceled SQL operations: got=%#v, err=%v", got, err)
	}
}

type reportWithLockedSums struct {
	*pg.Repository
	lock *sql.Tx
}

func (r *reportWithLockedSums) ListReportCategories(ctx context.Context, from, to string) ([]string, error) {
	categories, err := r.Repository.ListReportCategories(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if _, err := r.lock.ExecContext(ctx, "LOCK TABLE expenses IN ACCESS EXCLUSIVE MODE"); err != nil {
		return nil, err
	}
	return categories, nil
}
