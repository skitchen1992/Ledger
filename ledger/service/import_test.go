package service_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

// concurrentRepositories serializes units of work, as the PostgreSQL category
// lock does (here globally, which is stricter), so import workers may call the
// service in parallel. enter runs before the lock and lets tests hold workers.
type concurrentRepositories struct {
	mu sync.Mutex
	*memoryRepositories
	enter func(context.Context, string) error
}

func newConcurrentRepositories() *concurrentRepositories {
	return &concurrentRepositories{memoryRepositories: newRepositories()}
}

func (r *concurrentRepositories) WithinCategory(ctx context.Context, category string, operation func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	if r.enter != nil {
		if err := r.enter(ctx, category); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memoryRepositories.WithinCategory(ctx, category, operation)
}

func (r *concurrentRepositories) ListTransactions(ctx context.Context) ([]domain.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memoryRepositories.ListTransactions(ctx)
}

func (r *concurrentRepositories) SumByCategory(ctx context.Context, category string) (*big.Rat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.memoryRepositories.SumByCategory(ctx, category)
}

// gate holds every unit of work until release is closed or the import context
// ends, and counts the workers currently inside the repository.
type gate struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	cause   func(context.Context) error
}

func newGate(capacity int) *gate {
	return &gate{started: make(chan string, capacity), release: make(chan struct{})}
}

func (g *gate) enter(ctx context.Context, category string) error {
	g.active.Add(1)
	defer g.active.Add(-1)
	g.started <- category
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		if g.cause != nil {
			return g.cause(ctx)
		}
		return fmt.Errorf("query canceled: %w", ctx.Err())
	}
}

func (g *gate) waitStarted(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-g.started:
		case <-time.After(time.Second):
			t.Fatalf("expected %d workers to enter the repository concurrently", count)
		}
	}
}

type importOutcome struct {
	summary service.ImportSummary
	err     error
}

func runImport(ctx context.Context, s *service.Ledger, input []service.Transaction, workers int) <-chan importOutcome {
	outcome := make(chan importOutcome, 1)
	go func() {
		summary, err := s.ImportTransactions(ctx, input, workers)
		outcome <- importOutcome{summary, err}
	}()
	return outcome
}

func waitImport(t *testing.T, outcome <-chan importOutcome) importOutcome {
	t.Helper()
	select {
	case result := <-outcome:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("bulk import did not finish")
		return importOutcome{}
	}
}

// errorIndexes returns indexes in summary order, which must follow the input.
func errorIndexes(summary service.ImportSummary) []int {
	indexes := make([]int, 0, len(summary.Errors))
	for _, importError := range summary.Errors {
		indexes = append(indexes, importError.Index)
	}
	return indexes
}

func TestImportWithOneWorkerAppliesCreateTransactionRulesInInputOrder(t *testing.T) {
	ctx := context.Background()
	repositories := newConcurrentRepositories()
	s := service.New(repositories, repositories, repositories)
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	input := []service.Transaction{
		{Category: "food", Amount: 4, Description: "lunch", Date: "2026-11-01"},
		{Category: "food", Amount: 0},
		{Category: " \t", Amount: 5},
		{Category: "food", Amount: 7},
		{ID: 99, Category: "food", Amount: 6, Date: "any text"},
		{Category: "travel", Amount: 1e6},
	}
	summary, err := s.ImportTransactions(ctx, input, 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Accepted != 3 || summary.Rejected != 3 {
		t.Fatalf("summary counts = %d accepted, %d rejected; want 3 and 3", summary.Accepted, summary.Rejected)
	}
	wantErrors := []struct {
		index   int
		cause   error
		message string
	}{
		{1, service.ErrInvalidInput, "transaction amount must be a positive finite number"},
		{2, service.ErrInvalidInput, "transaction category must not be empty"},
		{3, service.ErrBudgetExceeded, `category "food": budget exceeded`},
	}
	if len(summary.Errors) != len(wantErrors) {
		t.Fatalf("errors = %+v, want %d entries", summary.Errors, len(wantErrors))
	}
	for i, want := range wantErrors {
		got := summary.Errors[i]
		if got.Index != want.index || !errors.Is(got.Err, want.cause) || got.Err.Error() != want.message {
			t.Errorf("errors[%d] = {%d %v}, want index %d, cause %v, message %q", i, got.Index, got.Err, want.index, want.cause, want.message)
		}
	}
	history, err := s.ListTransactions(ctx)
	want := []service.Transaction{
		{ID: 1, Category: "food", Amount: 4, Description: "lunch", Date: "2026-11-01"},
		{ID: 2, Category: "food", Amount: 6, Date: "any text"},
		{ID: 3, Category: "travel", Amount: 1e6},
	}
	if err != nil || !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %+v, err = %v; want only accepted elements in input order %+v", history, err, want)
	}
}

func TestConcurrentImportKeepsBudgetAndCollectsEveryErrorOnce(t *testing.T) {
	ctx := context.Background()
	repositories := newConcurrentRepositories()
	s := service.New(repositories, repositories, repositories)
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	input := make([]service.Transaction, 0, 60)
	invalid := make(map[int]bool)
	for i := 0; i < 60; i++ {
		if i%6 == 0 {
			invalid[i] = true
			input = append(input, service.Transaction{Category: "food", Amount: -1})
			continue
		}
		input = append(input, service.Transaction{Category: "food", Amount: 0.5})
	}
	summary, err := s.ImportTransactions(ctx, input, 8)
	if err != nil {
		t.Fatal(err)
	}
	// 50 valid elements of 0.5 compete for a limit of 20: exactly 40 fit.
	if summary.Accepted != 40 || summary.Rejected != 20 {
		t.Fatalf("summary counts = %d accepted, %d rejected; want 40 and 20", summary.Accepted, summary.Rejected)
	}
	budgetRejected := 0
	for i, importError := range summary.Errors {
		if i > 0 && importError.Index <= summary.Errors[i-1].Index {
			t.Fatalf("errors are not strictly ordered by input index: %v", errorIndexes(summary))
		}
		switch {
		case invalid[importError.Index] && errors.Is(importError.Err, service.ErrInvalidInput):
			delete(invalid, importError.Index)
		case !invalid[importError.Index] && errors.Is(importError.Err, service.ErrBudgetExceeded):
			budgetRejected++
		default:
			t.Errorf("unexpected error for element %d: %v", importError.Index, importError.Err)
		}
	}
	if len(invalid) != 0 || budgetRejected != 10 {
		t.Errorf("missing invalid elements %v; budget rejections = %d, want 10", invalid, budgetRejected)
	}
	history, err := s.ListTransactions(ctx)
	if err != nil || len(history) != 40 {
		t.Fatalf("history has %d rows, err = %v; want 40 accepted rows", len(history), err)
	}
	ids := make(map[int]bool)
	for _, tx := range history {
		if ids[tx.ID] || tx.Category != "food" || tx.Amount != 0.5 {
			t.Errorf("unexpected or duplicate stored transaction %+v", tx)
		}
		ids[tx.ID] = true
	}
}

func TestImportRunsRequestedNumberOfWorkersConcurrently(t *testing.T) {
	repositories := newConcurrentRepositories()
	g := newGate(6)
	repositories.enter = g.enter
	s := service.New(repositories, repositories, repositories)
	input := make([]service.Transaction, 6)
	for i := range input {
		input[i] = service.Transaction{Category: fmt.Sprintf("category-%d", i), Amount: 1}
	}
	outcome := runImport(context.Background(), s, input, 3)
	g.waitStarted(t, 3)
	select {
	case category := <-g.started:
		t.Fatalf("a fourth worker started %s while three elements were in progress", category)
	case <-time.After(50 * time.Millisecond):
	}
	close(g.release)
	result := waitImport(t, outcome)
	if result.err != nil || result.summary.Accepted != 6 || result.summary.Rejected != 0 || len(result.summary.Errors) != 0 {
		t.Fatalf("summary = %+v, err = %v; want all six accepted", result.summary, result.err)
	}
}

func TestCanceledImportReturnsPartialSummaryAndStopsWorkers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline bool
		cause    error
		// rollback simulates a driver that reports a canceled commit
		// without wrapping ctx.Err(), such as database/sql's ErrTxDone.
		rollback bool
	}{
		{name: "cancel", cause: context.Canceled},
		{name: "deadline", deadline: true, cause: context.DeadlineExceeded},
		{name: "driver error after cancel", cause: context.Canceled, rollback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repositories := newConcurrentRepositories()
			g := newGate(1)
			if tc.rollback {
				g.cause = func(context.Context) error {
					return errors.New("commit category update: transaction already rolled back")
				}
			}
			repositories.enter = func(ctx context.Context, category string) error {
				if category != "slow" {
					return nil
				}
				return g.enter(ctx, category)
			}
			s := service.New(repositories, repositories, repositories)
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			input := []service.Transaction{
				{Category: "fast", Amount: 1},
				{Category: "fast", Amount: 0},
				{Category: "slow", Amount: 1},
				{Category: "fast", Amount: 2},
				{Category: "fast", Amount: 3},
			}
			outcome := runImport(ctx, s, input, 1)
			g.waitStarted(t, 1)
			if !tc.deadline {
				cancel()
			}
			result := waitImport(t, outcome)
			if !errors.Is(result.err, tc.cause) {
				t.Fatalf("error = %v, want cause %v", result.err, tc.cause)
			}
			if want := "bulk import interrupted after 2 of 5 transactions"; !strings.HasPrefix(result.err.Error(), want) {
				t.Errorf("error = %v, want message starting with %q", result.err, want)
			}
			summary := result.summary
			if summary.Accepted != 1 || summary.Rejected != 1 || !reflect.DeepEqual(errorIndexes(summary), []int{1}) ||
				!errors.Is(summary.Errors[0].Err, service.ErrInvalidInput) {
				t.Fatalf("partial summary = %+v, want element 0 accepted and element 1 rejected as invalid", summary)
			}
			if active := g.active.Load(); active != 0 {
				t.Errorf("%d workers still inside the repository after return", active)
			}
			history, err := s.ListTransactions(context.Background())
			if want := []service.Transaction{{ID: 1, Category: "fast", Amount: 1}}; err != nil || !reflect.DeepEqual(history, want) {
				t.Fatalf("history = %+v, err = %v; interrupted and unsent elements must not be stored", history, err)
			}
		})
	}
}

func TestCanceledImportWaitsForEveryWorker(t *testing.T) {
	repositories := newConcurrentRepositories()
	g := newGate(10)
	repositories.enter = g.enter
	s := service.New(repositories, repositories, repositories)
	input := make([]service.Transaction, 10)
	for i := range input {
		input[i] = service.Transaction{Category: "food", Amount: 1}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := runImport(ctx, s, input, 4)
	g.waitStarted(t, 4)
	cancel()
	result := waitImport(t, outcome)
	if !errors.Is(result.err, context.Canceled) || result.summary.Accepted != 0 || result.summary.Rejected != 0 || len(result.summary.Errors) != 0 {
		t.Fatalf("summary = %+v, err = %v; want empty partial summary and context.Canceled", result.summary, result.err)
	}
	if active := g.active.Load(); active != 0 {
		t.Errorf("%d workers still running after the import returned", active)
	}
	if len(repositories.expenses) != 0 {
		t.Errorf("canceled import stored %+v", repositories.expenses)
	}
}

func TestImportValidatesWorkersAndHandlesEmptyInput(t *testing.T) {
	for _, workers := range []int{0, -1, service.MaxImportWorkers + 1} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			repositories := newConcurrentRepositories()
			s := service.New(repositories, repositories, repositories)
			summary, err := s.ImportTransactions(context.Background(), []service.Transaction{{Category: "food", Amount: 1}}, workers)
			if !errors.Is(err, service.ErrInvalidInput) || !reflect.DeepEqual(summary, service.ImportSummary{}) {
				t.Fatalf("workers=%d: summary = %+v, err = %v; want ErrInvalidInput", workers, summary, err)
			}
			if len(repositories.expenses) != 0 {
				t.Fatalf("invalid workers stored %+v", repositories.expenses)
			}
		})
	}
	repositories := newConcurrentRepositories()
	s := service.New(repositories, repositories, repositories)
	summary, err := s.ImportTransactions(context.Background(), nil, service.MaxImportWorkers)
	if err != nil || !reflect.DeepEqual(summary, service.ImportSummary{}) {
		t.Fatalf("empty import: summary = %+v, err = %v; want zero summary", summary, err)
	}
}

func TestImportReportsRepositoryFailuresPerElementWithoutRows(t *testing.T) {
	failure := errors.New("insert failed")
	repositories := newConcurrentRepositories()
	repositories.insertErr = failure
	s := service.New(repositories, repositories, repositories)
	input := []service.Transaction{{Category: "food", Amount: 1}, {Category: "books", Amount: 2}, {Category: "food", Amount: 3}}
	summary, err := s.ImportTransactions(context.Background(), input, 2)
	if err != nil {
		t.Fatalf("element failures must not fail the whole import: %v", err)
	}
	if summary.Accepted != 0 || summary.Rejected != 3 || !reflect.DeepEqual(errorIndexes(summary), []int{0, 1, 2}) {
		t.Fatalf("summary = %+v, want three ordered rejections", summary)
	}
	for _, importError := range summary.Errors {
		if !errors.Is(importError.Err, failure) {
			t.Errorf("element %d lost repository cause: %v", importError.Index, importError.Err)
		}
	}
	if len(repositories.expenses) != 0 {
		t.Fatalf("failed inserts stored %+v", repositories.expenses)
	}
}
