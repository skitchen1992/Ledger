package service_test

import (
	"context"
	"errors"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

// memoryRepositories provides transactional state for service tests. Failed
// callbacks or commits discard staged data, just as the domain contract requires.
type memoryRepositories struct {
	budgets   map[string]domain.Budget
	expenses  []domain.Transaction
	setErr    error
	getErr    error
	sumErr    error
	insertErr error
	commitErr error
	listErr   error
}

func newRepositories() *memoryRepositories {
	return &memoryRepositories{budgets: make(map[string]domain.Budget), expenses: make([]domain.Transaction, 0)}
}

func (r *memoryRepositories) WithinCategory(ctx context.Context, _ string, operation func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	staged := *r
	staged.budgets = make(map[string]domain.Budget, len(r.budgets))
	for category, budget := range r.budgets {
		staged.budgets[category] = budget
	}
	staged.expenses = append([]domain.Transaction{}, r.expenses...)
	if err := operation(&staged, &staged); err != nil {
		return err
	}
	if r.commitErr != nil {
		return r.commitErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.budgets, r.expenses = staged.budgets, staged.expenses
	return nil
}

func (r *memoryRepositories) SetBudget(ctx context.Context, budget domain.Budget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.setErr != nil {
		return r.setErr
	}
	r.budgets[budget.Category] = budget
	return nil
}

func (r *memoryRepositories) GetBudget(ctx context.Context, category string) (domain.Budget, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Budget{}, false, err
	}
	if r.getErr != nil {
		return domain.Budget{}, false, r.getErr
	}
	budget, found := r.budgets[category]
	return budget, found, nil
}

func (r *memoryRepositories) ListBudgets(ctx context.Context) ([]domain.Budget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.listErr != nil {
		return nil, r.listErr
	}
	budgets := make([]domain.Budget, 0, len(r.budgets))
	for _, budget := range r.budgets {
		budgets = append(budgets, budget)
	}
	sort.Slice(budgets, func(i, j int) bool { return budgets[i].Category < budgets[j].Category })
	return budgets, nil
}

func (r *memoryRepositories) InsertTransaction(ctx context.Context, expense domain.Transaction) (domain.Transaction, error) {
	if err := ctx.Err(); err != nil {
		return domain.Transaction{}, err
	}
	if r.insertErr != nil {
		return domain.Transaction{}, r.insertErr
	}
	expense.ID = len(r.expenses) + 1
	r.expenses = append(r.expenses, expense)
	return expense, nil
}

func (r *memoryRepositories) ListTransactions(ctx context.Context) ([]domain.Transaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]domain.Transaction{}, r.expenses...), nil
}

func (r *memoryRepositories) SumByCategory(ctx context.Context, category string) (*big.Rat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.sumErr != nil {
		return nil, r.sumErr
	}
	total := new(big.Rat)
	for _, expense := range r.expenses {
		if expense.Category == category {
			amount, ok := new(big.Rat).SetString(strconv.FormatFloat(expense.Amount, 'f', -1, 64))
			if !ok {
				return nil, errors.New("test repository contains invalid money")
			}
			total.Add(total, amount)
		}
	}
	return total, nil
}

func TestBudgetUsesExactAccumulatedDecimalsAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	repositories := newRepositories()
	s := service.New(repositories, repositories, repositories)
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 0.3}); err != nil {
		t.Fatal(err)
	}
	var accepted []service.Transaction
	for _, amount := range []float64{0.1, 0.2} {
		saved, err := s.CreateTransaction(ctx, service.Transaction{ID: 99, Category: "food", Amount: amount})
		if err != nil {
			t.Fatalf("exact decimal boundary must be accepted: %v", err)
		}
		if saved.ID == 99 || saved.ID == 0 {
			t.Fatalf("service did not return repository-assigned ID: %#v", saved)
		}
		accepted = append(accepted, saved)
	}
	for _, limit := range []float64{0.3, 0.2} {
		if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: limit}); err != nil {
			t.Fatal(err)
		}
		saved, err := s.CreateTransaction(ctx, service.Transaction{Category: "food", Amount: 0.00000000000000001})
		if !errors.Is(err, service.ErrBudgetExceeded) || saved != (service.Transaction{}) {
			t.Fatalf("limit %v must reject even a tiny excess without returning a record: saved=%#v, err=%v", limit, saved, err)
		}
		got, err := s.ListTransactions(ctx)
		if err != nil || !reflect.DeepEqual(got, accepted) {
			t.Fatalf("budget update or rejected expense changed history: got=%#v, err=%v", got, err)
		}
	}
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 0.4}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTransaction(ctx, service.Transaction{Category: "food", Amount: 0.1}); err != nil {
		t.Fatalf("raising the budget should allow the new exact boundary: %v", err)
	}
}

func TestUnbudgetedCategoriesAndGatewayValuesRemainIndependent(t *testing.T) {
	ctx := context.Background()
	repositories := newRepositories()
	s := service.New(repositories, repositories, repositories)
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	inputs := []service.Transaction{
		{Category: "travel", Amount: math.MaxFloat64, Date: "whenever", Description: "preserved"},
		{Category: "travel", Amount: math.SmallestNonzeroFloat64},
		{Category: "food", Amount: 1, Date: "2026-11-06"},
	}
	want := make([]service.Transaction, 0, len(inputs))
	for _, input := range inputs {
		saved, err := s.CreateTransaction(ctx, input)
		if err != nil {
			t.Fatalf("compatible input %#v rejected: %v", input, err)
		}
		input.ID = saved.ID
		if saved != input {
			t.Fatalf("saved fields changed: got=%#v, want=%#v", saved, input)
		}
		want = append(want, input)
	}
	got, err := s.ListTransactions(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Gateway history must preserve insertion order and fields: got=%#v, err=%v", got, err)
	}
}

func TestCreateTransactionFailuresPreserveStateAndDoNotReturnSavedRecord(t *testing.T) {
	failure := errors.New("storage failure")
	tests := []struct {
		name      string
		input     service.Transaction
		configure func(*memoryRepositories)
		wantError error
	}{
		{name: "validation", input: service.Transaction{Category: "food", Amount: 0}, wantError: service.ErrInvalidInput},
		{name: "nonfinite", input: service.Transaction{Category: "food", Amount: math.Inf(1)}, wantError: service.ErrInvalidInput},
		{name: "category", input: service.Transaction{Category: " \t", Amount: 1}, wantError: service.ErrInvalidInput},
		{name: "budget", input: service.Transaction{Category: "food", Amount: 91}, wantError: service.ErrBudgetExceeded},
		{name: "get budget", input: service.Transaction{Category: "food", Amount: 1}, configure: func(r *memoryRepositories) { r.getErr = failure }, wantError: failure},
		{name: "sum expenses", input: service.Transaction{Category: "food", Amount: 1}, configure: func(r *memoryRepositories) { r.sumErr = failure }, wantError: failure},
		{name: "insert", input: service.Transaction{Category: "food", Amount: 1}, configure: func(r *memoryRepositories) { r.insertErr = failure }, wantError: failure},
		{name: "commit", input: service.Transaction{Category: "food", Amount: 1}, configure: func(r *memoryRepositories) { r.commitErr = failure }, wantError: failure},
		{name: "query canceled", input: service.Transaction{Category: "food", Amount: 1}, configure: func(r *memoryRepositories) { r.getErr = context.Canceled }, wantError: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositories := newRepositories()
			repositories.budgets["food"] = service.Budget{Category: "food", Limit: 100}
			original := service.Transaction{ID: 1, Category: "food", Amount: 10, Date: "original"}
			repositories.expenses = []service.Transaction{original}
			if test.configure != nil {
				test.configure(repositories)
			}
			s := service.New(repositories, repositories, repositories)
			saved, err := s.CreateTransaction(context.Background(), test.input)
			if !errors.Is(err, test.wantError) || saved != (service.Transaction{}) {
				t.Fatalf("failed transaction must retain cause and return no saved record: saved=%#v, err=%v", saved, err)
			}
			got, err := s.ListTransactions(context.Background())
			if err != nil || !reflect.DeepEqual(got, []service.Transaction{original}) {
				t.Fatalf("failure changed history: got=%#v, err=%v", got, err)
			}
			budgets, err := s.ListBudgets(context.Background())
			if err != nil || !reflect.DeepEqual(budgets, []service.Budget{{Category: "food", Limit: 100}}) {
				t.Fatalf("failure changed budgets: got=%#v, err=%v", budgets, err)
			}
		})
	}
}

func TestBudgetUpdateFailuresKeepPreviousBudgetAndExpenses(t *testing.T) {
	failure := errors.New("budget storage failure")
	tests := []struct {
		name      string
		limit     float64
		configure func(*memoryRepositories)
		wantError error
	}{
		{name: "invalid", limit: 0, wantError: service.ErrInvalidInput},
		{name: "write", limit: 20, configure: func(r *memoryRepositories) { r.setErr = failure }, wantError: failure},
		{name: "commit", limit: 20, configure: func(r *memoryRepositories) { r.commitErr = failure }, wantError: failure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositories := newRepositories()
			originalBudget := service.Budget{Category: "food", Limit: 10}
			originalExpense := service.Transaction{ID: 1, Category: "food", Amount: 5}
			repositories.budgets["food"] = originalBudget
			repositories.expenses = []service.Transaction{originalExpense}
			if test.configure != nil {
				test.configure(repositories)
			}
			s := service.New(repositories, repositories, repositories)
			if err := s.SetBudget(context.Background(), service.Budget{Category: "food", Limit: test.limit}); !errors.Is(err, test.wantError) {
				t.Fatalf("expected original error cause %v, got %v", test.wantError, err)
			}
			budgets, err := s.ListBudgets(context.Background())
			if err != nil || !reflect.DeepEqual(budgets, []service.Budget{originalBudget}) {
				t.Fatalf("failed update changed budget: got=%#v, err=%v", budgets, err)
			}
			expenses, err := s.ListTransactions(context.Background())
			if err != nil || !reflect.DeepEqual(expenses, []service.Transaction{originalExpense}) {
				t.Fatalf("failed update changed expenses: got=%#v, err=%v", expenses, err)
			}
		})
	}
}

func TestCanceledContextAndReadFailuresAreNotSuccessfulResults(t *testing.T) {
	repositories := newRepositories()
	s := service.New(repositories, repositories, repositories)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled budget write: %v", err)
	}
	saved, err := s.CreateTransaction(ctx, service.Transaction{Category: "food", Amount: 1})
	if !errors.Is(err, context.Canceled) || saved != (service.Transaction{}) {
		t.Fatalf("canceled expense write: saved=%#v, err=%v", saved, err)
	}
	if len(repositories.budgets) != 0 || len(repositories.expenses) != 0 {
		t.Fatalf("canceled operations changed state: %#v", repositories)
	}
	if _, err := s.ListBudgets(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled budget read: %v", err)
	}
	if _, err := s.ListTransactions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled expense read: %v", err)
	}
	repositories.listErr = errors.New("database unavailable")
	if _, err := s.ListBudgets(context.Background()); !errors.Is(err, repositories.listErr) {
		t.Fatalf("budget read failure was lost: %v", err)
	}
	if _, err := s.ListTransactions(context.Background()); !errors.Is(err, repositories.listErr) {
		t.Fatalf("expense read failure was lost: %v", err)
	}
}

func TestStrictServiceKeepsDateMoneyAndHistoryContracts(t *testing.T) {
	ctx := context.Background()
	repositories := newRepositories()
	s := service.NewStrict(repositories, repositories, repositories)
	for _, input := range []service.Transaction{
		{Category: "food", Amount: 1},
		{Category: "food", Amount: 1, Date: "2026-02-30"},
		{Category: "food", Amount: 1, Date: "2026-1-1"},
		{Category: "food", Amount: 1.001, Date: "2026-01-01"},
		{Category: "food", Amount: 1e12, Date: "2026-01-01"},
	} {
		if saved, err := s.CreateTransaction(ctx, input); !errors.Is(err, service.ErrInvalidInput) || saved != (service.Transaction{}) {
			t.Fatalf("invalid strict expense accepted: input=%#v, saved=%#v, err=%v", input, saved, err)
		}
	}
	for _, limit := range []float64{1.001, 1e12} {
		if err := s.SetBudget(ctx, service.Budget{Category: "food", Limit: limit}); !errors.Is(err, service.ErrInvalidInput) {
			t.Fatalf("invalid strict budget %v accepted: %v", limit, err)
		}
	}
	if len(repositories.expenses) != 0 || len(repositories.budgets) != 0 {
		t.Fatal("strict validation changed repository state")
	}
	var accepted []service.Transaction
	for _, date := range []string{"2026-01-02", "2026-01-01", "2026-01-02"} {
		saved, err := s.CreateTransaction(ctx, service.Transaction{Category: "food", Amount: 1, Date: date})
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, saved)
	}
	got, err := s.ListTransactions(ctx)
	want := []service.Transaction{accepted[2], accepted[0], accepted[1]}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("strict history needs descending date and ID: got=%#v, err=%v", got, err)
	}
	if !reflect.DeepEqual(repositories.expenses, accepted) {
		t.Fatal("sorting strict results mutated repository history")
	}
}

type differentlyOrderedBudgets struct{ *memoryRepositories }

func (r differentlyOrderedBudgets) ListBudgets(context.Context) ([]domain.Budget, error) {
	return []domain.Budget{{Category: "a", Limit: 1}, {Category: "A", Limit: 1}, {Category: "b", Limit: 1}, {Category: "B", Limit: 1}}, nil
}

func TestGatewayBudgetOrderDoesNotDependOnRepositoryCollation(t *testing.T) {
	repo := differentlyOrderedBudgets{newRepositories()}
	s := service.New(repo, repo, repo)
	got, err := s.ListBudgets(context.Background())
	want := []domain.Budget{{Category: "A", Limit: 1}, {Category: "B", Limit: 1}, {Category: "a", Limit: 1}, {Category: "b", Limit: 1}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Gateway category order changed: got=%v err=%v", got, err)
	}
}
