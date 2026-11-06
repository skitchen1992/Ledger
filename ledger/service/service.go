// Package service implements Ledger business operations using domain contracts.
package service

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
)

// Public application types let clients use the service without storage imports.
type Budget = domain.Budget
type Transaction = domain.Transaction
type ReportSummary = domain.ReportSummary

var ErrBudgetExceeded = domain.ErrBudgetExceeded
var ErrInvalidInput = domain.ErrInvalidInput

type Service interface {
	SetBudget(context.Context, Budget) error
	ListBudgets(context.Context) ([]Budget, error)
	CreateTransaction(context.Context, Transaction) (Transaction, error)
	ListTransactions(context.Context) ([]Transaction, error)
}

type ReportingService interface {
	Service
	GetReportSummary(context.Context, string, string) (ReportSummary, error)
}

const queryTimeout = 5 * time.Second

type Ledger struct {
	budgets  domain.BudgetRepository
	expenses domain.ExpenseRepository
	work     domain.UnitOfWork
	strict   bool
}

// New preserves Gateway's original validation and insertion-order history.
func New(budgets domain.BudgetRepository, expenses domain.ExpenseRepository, work domain.UnitOfWork) *Ledger {
	return &Ledger{budgets: budgets, expenses: expenses, work: work}
}

// NewStrict preserves the standalone Ledger API's date and NUMERIC(14,2) limits.
func NewStrict(budgets domain.BudgetRepository, expenses domain.ExpenseRepository, work domain.UnitOfWork) *Ledger {
	s := New(budgets, expenses, work)
	s.strict = true
	return s
}

func (s *Ledger) SetBudget(ctx context.Context, budget Budget) error {
	if err := budget.Validate(); err != nil {
		return domain.ValidationError{Cause: err}
	}
	if s.strict {
		if err := validateMoney(budget.Limit); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	return s.work.WithinCategory(ctx, budget.Category, func(budgets domain.BudgetRepository, _ domain.ExpenseRepository) error {
		return budgets.SetBudget(ctx, budget)
	})
}

func (s *Ledger) ListBudgets(ctx context.Context) ([]Budget, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	budgets, err := s.budgets.ListBudgets(ctx)
	if err != nil {
		return nil, err
	}
	if !s.strict {
		// Preserve Gateway's bytewise category order regardless of DB collation.
		sort.Slice(budgets, func(i, j int) bool { return budgets[i].Category < budgets[j].Category })
	}
	return budgets, nil
}

func (s *Ledger) CreateTransaction(ctx context.Context, expense Transaction) (Transaction, error) {
	if err := expense.Validate(); err != nil {
		return Transaction{}, domain.ValidationError{Cause: err}
	}
	if s.strict {
		if err := validateMoney(expense.Amount); err != nil {
			return Transaction{}, err
		}
		if _, err := parseDate(expense.Date); err != nil {
			return Transaction{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var saved Transaction
	err := s.work.WithinCategory(ctx, expense.Category, func(budgets domain.BudgetRepository, expenses domain.ExpenseRepository) error {
		budget, found, err := budgets.GetBudget(ctx, expense.Category)
		if err != nil {
			return fmt.Errorf("get expense budget: %w", err)
		}
		if found {
			total, err := expenses.SumByCategory(ctx, expense.Category)
			if err != nil {
				return fmt.Errorf("sum category expenses: %w", err)
			}
			if new(big.Rat).Add(total, decimalAmount(expense.Amount)).Cmp(decimalAmount(budget.Limit)) > 0 {
				return fmt.Errorf("category %q: %w", expense.Category, ErrBudgetExceeded)
			}
		}
		saved, err = expenses.InsertTransaction(ctx, expense)
		return err
	})
	if err != nil {
		return Transaction{}, err
	}
	return saved, nil
}

func (s *Ledger) ListTransactions(ctx context.Context) ([]Transaction, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	expenses, err := s.expenses.ListTransactions(ctx)
	if err != nil {
		return nil, err
	}
	if s.strict {
		sort.Slice(expenses, func(i, j int) bool {
			if expenses[i].Date == expenses[j].Date {
				return expenses[i].ID > expenses[j].ID
			}
			return expenses[i].Date > expenses[j].Date
		})
	}
	return expenses, nil
}

func decimalAmount(value float64) *big.Rat {
	amount, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	if !ok {
		panic("decimalAmount requires a finite value")
	}
	return amount
}

func validateMoney(value float64) error {
	cents := new(big.Rat).Mul(decimalAmount(value), big.NewRat(100, 1))
	if !cents.IsInt() || cents.Cmp(big.NewRat(99999999999999, 1)) > 0 {
		return fmt.Errorf("%w: money must have at most two decimal places and be at most 999999999999.99", ErrInvalidInput)
	}
	return nil
}

func parseDate(value string) (time.Time, error) {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != value {
		return time.Time{}, fmt.Errorf("%w: date must be a valid YYYY-MM-DD", ErrInvalidInput)
	}
	return date, nil
}
