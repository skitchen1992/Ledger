package domain

import (
	"context"
	"errors"
	"math/big"
	"time"
)

type BudgetRepository interface {
	SetBudget(context.Context, Budget) error
	GetBudget(context.Context, string) (Budget, bool, error)
	ListBudgets(context.Context) ([]Budget, error)
}

type ExpenseRepository interface {
	InsertTransaction(context.Context, Transaction) (Transaction, error)
	ListTransactions(context.Context) ([]Transaction, error)
	// SumByCategory returns an exact decimal total, including zero for no expenses.
	SumByCategory(context.Context, string) (*big.Rat, error)
}

// UnitOfWork serializes changes to a category and commits the callback atomically.
// Callback repositories share the same unit; an error rolls back all its writes.
type UnitOfWork interface {
	WithinCategory(context.Context, string, func(BudgetRepository, ExpenseRepository) error) error
}

type CategoryTotal struct {
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
}

type ReportSummary struct {
	From       string          `json:"from"`
	To         string          `json:"to"`
	Total      float64         `json:"total"`
	Categories []CategoryTotal `json:"categories"`
}

type ReportRepository interface {
	GetReportSummary(context.Context, string, string) (ReportSummary, error)
}

var ErrCacheMiss = errors.New("report cache miss")

type ReportCache interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
}

var ErrInvalidInput = errors.New("invalid input")

// ValidationError preserves the original public validation message and its cause.
type ValidationError struct{ Cause error }

func (e ValidationError) Error() string        { return e.Cause.Error() }
func (e ValidationError) Unwrap() error        { return e.Cause }
func (e ValidationError) Is(target error) bool { return target == ErrInvalidInput }
