package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/internal/repository/pg"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func TestSQLBulkImportRespectsBudgetAndCategoryIsolation(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	input := make([]service.Transaction, 0, 45)
	for i := 0; i < 30; i++ {
		input = append(input, service.Transaction{Category: "food", Amount: 1})
	}
	for i := 0; i < 10; i++ {
		input = append(input, service.Transaction{Category: "travel", Amount: 2, Date: "any text"})
	}
	for i := 0; i < 5; i++ {
		input = append(input, service.Transaction{Category: "food", Amount: -1})
	}
	summary, err := s.ImportTransactions(ctx, input, 8)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Accepted != 20 || summary.Rejected != 25 || len(summary.Errors) != 25 {
		t.Fatalf("summary = %d accepted, %d rejected, %d errors; want 20, 25, 25", summary.Accepted, summary.Rejected, len(summary.Errors))
	}
	budget, invalid := 0, 0
	for _, importError := range summary.Errors {
		switch {
		case importError.Index < 30 && errors.Is(importError.Err, service.ErrBudgetExceeded):
			budget++
		case importError.Index >= 40 && errors.Is(importError.Err, service.ErrInvalidInput):
			invalid++
		default:
			t.Errorf("unexpected error for element %d: %v", importError.Index, importError.Err)
		}
	}
	if budget != 20 || invalid != 5 {
		t.Errorf("budget rejections = %d, invalid = %d; want 20 and 5", budget, invalid)
	}
	for category, want := range map[string]struct {
		count  int
		amount float64
	}{"food": {10, 10}, "travel": {10, 20}} {
		var count int
		var amount float64
		if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM expenses WHERE category = $1", category).
			Scan(&count, &amount); err != nil {
			t.Fatal(err)
		}
		if count != want.count || amount != want.amount {
			t.Errorf("%s: count=%d amount=%v, want %d and %v", category, count, amount, want.count, want.amount)
		}
	}
}

func TestSQLBulkImportDeadlineKeepsCommittedRowsAndReleasesConnections(t *testing.T) {
	f := newDatabaseFixture(t)
	repo := pg.New(f.db)
	s := service.New(repo, repo, repo)
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSetup()
	lock, err := f.db.BeginTx(setupCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	// Hold the same advisory lock that WithinCategory takes for "slow".
	if _, err := lock.ExecContext(setupCtx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "slow"); err != nil {
		t.Fatal(err)
	}
	input := []service.Transaction{
		{Category: "fast", Amount: 1},
		{Category: "fast", Amount: 0},
		{Category: "slow", Amount: 1},
		{Category: "fast", Amount: 2},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	var summary service.ImportSummary
	go func() {
		var err error
		summary, err = s.ImportTransactions(ctx, input, 1)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked import must return the deadline: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("import remained blocked after its deadline")
	}
	if summary.Accepted != 1 || summary.Rejected != 1 || len(summary.Errors) != 1 || summary.Errors[0].Index != 1 {
		t.Fatalf("partial summary = %+v, want element 0 accepted and element 1 rejected", summary)
	}
	if inUse := f.db.Stats().InUse; inUse != 1 {
		t.Fatalf("import returned with worker connections in use: %d, want only the lock transaction", inUse)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	history, err := s.ListTransactions(setupCtx)
	if err != nil || len(history) != 1 || history[0].Category != "fast" || history[0].Amount != 1 {
		t.Fatalf("history = %+v, err = %v; want only the element committed before the deadline", history, err)
	}
}
