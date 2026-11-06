package integration_test

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/internal/repository/pg"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func TestGatewayCompatibilityPersistsWithoutRoundingOrDateChanges(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	ctx := context.Background()
	inputs := []domain.Transaction{
		{Category: "food", Amount: 0.001, Date: ""},
		{Category: "tiny", Amount: math.SmallestNonzeroFloat64, Date: "arbitrary date"},
		{Category: "large", Amount: math.MaxFloat64, Date: "2026-02-30"},
		{Category: "food", Amount: 0.002, Date: "2026-01-02"},
		{Category: "food", Amount: 0.003, Date: "2026-01-01"},
	}
	if err := s.SetBudget(ctx, domain.Budget{Category: "food", Limit: 0.006}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBudget(ctx, domain.Budget{Category: "large", Limit: math.MaxFloat64}); err != nil {
		t.Fatal(err)
	}
	for i, input := range inputs {
		saved, err := s.CreateTransaction(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		inputs[i].ID = i + 1
		if saved != inputs[i] {
			t.Fatalf("saved = %#v, want %#v", saved, inputs[i])
		}
	}
	if _, err := s.CreateTransaction(ctx, domain.Transaction{Category: "food", Amount: 0.001}); !errors.Is(err, service.ErrBudgetExceeded) {
		t.Fatalf("undated spending must count toward exact cumulative limit: %v", err)
	}
	if _, err := s.CreateTransaction(ctx, domain.Transaction{Category: "large", Amount: math.SmallestNonzeroFloat64}); !errors.Is(err, service.ErrBudgetExceeded) {
		t.Fatalf("even a tiny amount exceeds an exhausted large budget: %v", err)
	}
	restarted := pg.New(f.connect(t))
	got, err := service.New(restarted, restarted, restarted).ListTransactions(ctx)
	if err != nil || !reflect.DeepEqual(got, inputs) {
		t.Fatalf("persistence must retain precision, dates, insertion order: got=%#v, err=%v", got, err)
	}
	reports := service.NewReporting(s, repo, nil)
	report, err := reports.GetReportSummary(ctx, "2026-01-01", "2026-12-31")
	want := domain.ReportSummary{From: "2026-01-01", To: "2026-12-31", Total: 0.005,
		Categories: []domain.CategoryTotal{{Category: "food", Amount: 0.005}}}
	if err != nil || !reflect.DeepEqual(report, want) {
		t.Fatalf("report must skip missing/arbitrary/impossible dates: got=%#v, err=%v", report, err)
	}
}

func TestCategoryUnitOfWorkRollsBackWritesOnFailure(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	ctx := context.Background()
	cause := errors.New("operation failed after writes")
	err := repo.WithinCategory(ctx, "food", func(b domain.BudgetRepository, e domain.ExpenseRepository) error {
		if err := b.SetBudget(ctx, domain.Budget{Category: "food", Limit: 10}); err != nil {
			return err
		}
		if _, err := e.InsertTransaction(ctx, domain.Transaction{Category: "food", Amount: 1}); err != nil {
			return err
		}
		return cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("lost callback error: %v", err)
	}
	budgets, err := repo.ListBudgets(ctx)
	if err != nil || len(budgets) != 0 {
		t.Fatalf("partial budget persisted: %v, %v", budgets, err)
	}
	expenses, err := repo.ListTransactions(ctx)
	if err != nil || len(expenses) != 0 {
		t.Fatalf("partial expense persisted: %v, %v", expenses, err)
	}
}

func TestCompatibilityMigrationRoundTripAndRollbackOfOriginalSchema(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx := context.Background()
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	if err := s.SetBudget(ctx, domain.Budget{Category: "food", Limit: 12.34}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.CreateTransaction(ctx, domain.Transaction{Category: "food", Amount: 1.23, Date: "2026-11-06"})
	if err != nil {
		t.Fatal(err)
	}
	up, down := compatibilityMigration(t)
	if _, err := f.db.Exec(down); err != nil {
		t.Fatal(err)
	}
	var dateType, moneyType string
	if err := f.db.QueryRow("SELECT pg_typeof(date)::text, pg_typeof(amount)::text FROM expenses").Scan(&dateType, &moneyType); err != nil {
		t.Fatal(err)
	}
	if dateType != "date" || moneyType != "numeric" {
		t.Fatalf("rollback types = %s,%s", dateType, moneyType)
	}
	if _, err := f.db.Exec(up); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTransactions(ctx)
	if err != nil || !reflect.DeepEqual(got, []domain.Transaction{saved}) {
		t.Fatalf("migration lost data: %v, %v", got, err)
	}
	// Original migrations must remain reversible after migration 3 is undone.
	if _, err := f.db.Exec(down); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00002_expenses.sql", "00001_budgets.sql"} {
		data, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		_, rollback, _ := strings.Cut(string(data), "-- +goose Down")
		if _, err := f.db.Exec(rollback); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = $1", f.schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("original migrations did not remove tables: count=%d, err=%v", count, err)
	}
}

func TestCompatibilityRollbackRefusesLossAndKeepsData(t *testing.T) {
	for _, tc := range []struct {
		name, date    string
		amount, limit float64
	}{
		{"fractional expense", "2026-11-06", 0.001, 10},
		{"large expense", "2026-11-06", 1e12, 1e12},
		{"fractional budget", "2026-11-06", 1, 1.001},
		{"undated", "", 1, 10},
		{"invalid date", "2026-02-30", 1, 10},
		{"noncanonical date", "2026-1-1", 1, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDatabaseFixture(t)
			repo := pg.New(f.db)
			s := service.New(repo, repo, repo)
			ctx := context.Background()
			if err := s.SetBudget(ctx, domain.Budget{Category: "food", Limit: tc.limit}); err != nil {
				t.Fatal(err)
			}
			saved, err := s.CreateTransaction(ctx, domain.Transaction{Category: "food", Amount: tc.amount, Date: tc.date})
			if err != nil {
				t.Fatal(err)
			}
			_, down := compatibilityMigration(t)
			if _, err := f.db.Exec(down); err == nil {
				t.Fatal("lossy rollback succeeded")
			}
			got, err := s.ListTransactions(ctx)
			if err != nil || !reflect.DeepEqual(got, []domain.Transaction{saved}) {
				t.Fatalf("failed rollback changed history: %v, %v", got, err)
			}
			budgets, err := s.ListBudgets(ctx)
			if err != nil || !reflect.DeepEqual(budgets, []domain.Budget{{Category: "food", Limit: tc.limit}}) {
				t.Fatalf("failed rollback changed budgets: %v, %v", budgets, err)
			}
		})
	}
}

func compatibilityMigration(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile("../../migrations/00003_gateway_compatibility.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(data), "-- +goose Down")
	if !ok {
		t.Fatal("missing migration Down")
	}
	return up, down
}

func TestGatewayPreservesNULAndBackslashStrings(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	ctx := context.Background()
	categories := []string{"nul\x00category", `literal\0`, `double\\0`, `path\folder`, "a", "A", "b", "B"}
	want := make([]domain.Transaction, 0, len(categories))
	for _, category := range categories {
		if err := s.SetBudget(ctx, domain.Budget{Category: category, Limit: 1}); err != nil {
			t.Fatal(err)
		}
		input := domain.Transaction{Category: category, Amount: 1, Description: "nul\x00 and \\0 and \\\\0", Date: "date\x00\\0"}
		saved, err := s.CreateTransaction(ctx, input)
		if err != nil {
			t.Fatalf("compatible strings rejected: %v", err)
		}
		input.ID = saved.ID
		if saved != input {
			t.Fatalf("created string fields changed: %#v", saved)
		}
		want = append(want, saved)
		if _, err := s.CreateTransaction(ctx, domain.Transaction{Category: category, Amount: 1}); !errors.Is(err, service.ErrBudgetExceeded) {
			t.Fatalf("encoded category lost its budget: %v", err)
		}
	}
	got, err := s.ListTransactions(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("string roundtrip changed values: got=%#v, err=%v", got, err)
	}
	_, down := compatibilityMigration(t)
	if _, err := f.db.Exec(down); err == nil {
		t.Fatal("rollback must refuse NUL string loss")
	}
}

func TestCompatibilityMigrationPreservesExistingBackslashes(t *testing.T) {
	f := newDatabaseFixture(t)
	up, down := compatibilityMigration(t)
	if _, err := f.db.Exec(down); err != nil {
		t.Fatal(err)
	}
	// One category's escaped representation equals another's old value. Rebuilding
	// the unique index during migration must avoid transient duplicate failures.
	for _, category := range []string{`path\`, `path\\`, `literal\0`} {
		if _, err := f.db.Exec("INSERT INTO budgets(category, limit_amount) VALUES ($1, 10)", category); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("INSERT INTO expenses(category, amount, description, date) VALUES ($1, 1, $2, '2026-11-06')", category, `note\0`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(up); err != nil {
		t.Fatal(err)
	}
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	for _, category := range []string{`path\`, `path\\`, `literal\0`} {
		budget, found, err := repo.GetBudget(context.Background(), category)
		if err != nil || !found || budget.Category != category || budget.Limit != 10 {
			t.Fatalf("migration changed category: %#v %v %v", budget, found, err)
		}
	}
	rows, err := s.ListTransactions(context.Background())
	if err != nil || len(rows) != 3 {
		t.Fatalf("migration changed expenses: %#v %v", rows, err)
	}
	for _, row := range rows {
		if row.Description != `note\0` {
			t.Fatalf("migration changed description: %#v", row)
		}
	}
	if _, err := f.db.Exec(down); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{`path\`, `path\\`, `literal\0`} {
		var got string
		if err := f.db.QueryRow("SELECT category FROM budgets WHERE category = $1", category).Scan(&got); err != nil || got != category {
			t.Fatalf("rollback changed category: %q %v", got, err)
		}
	}
}

func TestIndependentServiceBudgetUpdateWaitsForCategoryTransaction(t *testing.T) {
	f := newDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := pg.New(f.db)
	other := pg.New(f.connect(t))
	s := service.New(other, other, other)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- repo.WithinCategory(ctx, "food", func(b domain.BudgetRepository, e domain.ExpenseRepository) error {
			if err := b.SetBudget(ctx, domain.Budget{Category: "food", Limit: 10}); err != nil {
				return err
			}
			if _, err := e.InsertTransaction(ctx, domain.Transaction{Category: "food", Amount: 8}); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-operationDone:
		t.Fatalf("setup failed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	updateDone := make(chan error, 1)
	go func() { updateDone <- s.SetBudget(ctx, domain.Budget{Category: "food", Limit: 5}) }()
	select {
	case err := <-updateDone:
		t.Fatalf("update bypassed category lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTransaction(ctx, domain.Transaction{Category: "food", Amount: 1}); !errors.Is(err, service.ErrBudgetExceeded) {
		t.Fatalf("new limit must include the committed expense: %v", err)
	}
	rows, err := s.ListTransactions(ctx)
	if err != nil || len(rows) != 1 || rows[0].Amount != 8 {
		t.Fatalf("budget update lost history: %#v %v", rows, err)
	}
}
