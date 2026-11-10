package api

import (
	"context"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

// HTTP tests exercise the production service; only persistence is in memory.
type memoryState struct {
	mu           sync.Mutex
	budgets      map[string]domain.Budget
	transactions []domain.Transaction
	err          error
}

type memoryRepository struct {
	state  *memoryState
	locked bool
}

func newTestHandler() http.Handler {
	repository := newMemoryRepository()
	return NewHandler(service.NewReporting(service.New(repository, repository, repository), repository, nil))
}

func newMemoryRepository() memoryRepository {
	return memoryRepository{state: &memoryState{budgets: make(map[string]domain.Budget)}}
}

func (r memoryRepository) lock() func() {
	if r.locked {
		return func() {}
	}
	r.state.mu.Lock()
	return r.state.mu.Unlock
}

func (r memoryRepository) WithinCategory(ctx context.Context, _ string, apply func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	defer r.lock()()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.state.err != nil {
		return r.state.err
	}
	budgets := make(map[string]domain.Budget, len(r.state.budgets))
	for category, budget := range r.state.budgets {
		budgets[category] = budget
	}
	transactions := append([]domain.Transaction(nil), r.state.transactions...)
	bound := memoryRepository{state: r.state, locked: true}
	if err := apply(bound, bound); err != nil {
		r.state.budgets = budgets
		r.state.transactions = transactions
		return err
	}
	return nil
}

func (r memoryRepository) SetBudget(_ context.Context, budget domain.Budget) error {
	defer r.lock()()
	if r.state.err != nil {
		return r.state.err
	}
	r.state.budgets[budget.Category] = budget
	return nil
}

func (r memoryRepository) GetBudget(_ context.Context, category string) (domain.Budget, bool, error) {
	defer r.lock()()
	budget, found := r.state.budgets[category]
	return budget, found, r.state.err
}

func (r memoryRepository) ListBudgets(_ context.Context) ([]domain.Budget, error) {
	defer r.lock()()
	budgets := make([]domain.Budget, 0, len(r.state.budgets))
	for _, budget := range r.state.budgets {
		budgets = append(budgets, budget)
	}
	sort.Slice(budgets, func(i, j int) bool { return budgets[i].Category < budgets[j].Category })
	return budgets, r.state.err
}

func (r memoryRepository) InsertTransaction(_ context.Context, tx domain.Transaction) (domain.Transaction, error) {
	defer r.lock()()
	if r.state.err != nil {
		return domain.Transaction{}, r.state.err
	}
	tx.ID = len(r.state.transactions) + 1
	r.state.transactions = append(r.state.transactions, tx)
	return tx, nil
}

func (r memoryRepository) ListTransactions(_ context.Context) ([]domain.Transaction, error) {
	defer r.lock()()
	return append([]domain.Transaction(nil), r.state.transactions...), r.state.err
}

func (r memoryRepository) SumByCategory(_ context.Context, category string) (*big.Rat, error) {
	defer r.lock()()
	total := new(big.Rat)
	for _, tx := range r.state.transactions {
		if tx.Category == category {
			amount, _ := new(big.Rat).SetString(strconv.FormatFloat(tx.Amount, 'g', -1, 64))
			total.Add(total, amount)
		}
	}
	return total, r.state.err
}

func (r memoryRepository) ListReportCategories(ctx context.Context, from, to string) ([]string, error) {
	defer r.lock()()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	categories := make(map[string]bool)
	for _, tx := range r.state.transactions {
		if reportDateInRange(tx.Date, from, to) {
			categories[tx.Category] = true
		}
	}
	result := make([]string, 0, len(categories))
	for category := range categories {
		result = append(result, category)
	}
	sort.Strings(result)
	return result, r.state.err
}

func (r memoryRepository) SumReportCategory(ctx context.Context, category, from, to string) (*big.Rat, error) {
	defer r.lock()()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	total := new(big.Rat)
	for _, tx := range r.state.transactions {
		if tx.Category == category && reportDateInRange(tx.Date, from, to) {
			amount, _ := new(big.Rat).SetString(strconv.FormatFloat(tx.Amount, 'g', -1, 64))
			total.Add(total, amount)
		}
	}
	return total, r.state.err
}

func reportDateInRange(value, from, to string) bool {
	date, err := time.Parse(time.DateOnly, value)
	return err == nil && date.Year() > 0 && date.Format(time.DateOnly) == value && value >= from && value <= to
}
