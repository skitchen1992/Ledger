// Package pg implements domain repositories using PostgreSQL.
package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/skitchen1992/Ledger/ledger/domain"
)

type queries interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Repository uses the caller's database connection; the caller closes it.
type Repository struct {
	db      *sql.DB
	queries queries
}

var (
	_ domain.BudgetRepository  = (*Repository)(nil)
	_ domain.ExpenseRepository = (*Repository)(nil)
	_ domain.UnitOfWork        = (*Repository)(nil)
	_ domain.ReportRepository  = (*Repository)(nil)
)

func New(db *sql.DB) *Repository {
	return &Repository{db: db, queries: db}
}

// WithinCategory makes a category's budget changes and expense writes atomic.
// The advisory lock also covers categories that do not yet have a budget row.
func (r *Repository) WithinCategory(ctx context.Context, category string, operation func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	// A fresh snapshot after acquiring the lock is required even when the
	// connection's default isolation level is REPEATABLE READ.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin category update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", encodeText(category)); err != nil {
		return fmt.Errorf("lock category: %w", err)
	}
	bound := &Repository{db: r.db, queries: tx}
	if err := operation(bound, bound); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit category update: %w", err)
	}
	return nil
}

func (r *Repository) SetBudget(ctx context.Context, budget domain.Budget) error {
	_, err := r.queries.ExecContext(ctx, `INSERT INTO budgets(category, limit_amount) VALUES ($1, $2::numeric)
		ON CONFLICT(category) DO UPDATE SET limit_amount = EXCLUDED.limit_amount`,
		encodeText(budget.Category), strconv.FormatFloat(budget.Limit, 'f', -1, 64))
	if err != nil {
		return fmt.Errorf("set budget: %w", err)
	}
	return nil
}

func (r *Repository) GetBudget(ctx context.Context, category string) (domain.Budget, bool, error) {
	var budget domain.Budget
	err := r.queries.QueryRowContext(ctx,
		"SELECT category, limit_amount FROM budgets WHERE category = $1", encodeText(category)).
		Scan(&budget.Category, &budget.Limit)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Budget{}, false, nil
	}
	if err != nil {
		return domain.Budget{}, false, fmt.Errorf("get budget: %w", err)
	}
	if budget.Category, err = decodeText(budget.Category); err != nil {
		return domain.Budget{}, false, fmt.Errorf("read budget category: %w", err)
	}
	return budget, true, nil
}

func (r *Repository) ListBudgets(ctx context.Context) ([]domain.Budget, error) {
	rows, err := r.queries.QueryContext(ctx, "SELECT category, limit_amount FROM budgets ORDER BY category")
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer rows.Close()
	budgets := make([]domain.Budget, 0)
	for rows.Next() {
		var budget domain.Budget
		if err := rows.Scan(&budget.Category, &budget.Limit); err != nil {
			return nil, fmt.Errorf("read budget: %w", err)
		}
		if budget.Category, err = decodeText(budget.Category); err != nil {
			return nil, fmt.Errorf("read budget category: %w", err)
		}
		budgets = append(budgets, budget)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate budgets: %w", err)
	}
	return budgets, nil
}

func (r *Repository) InsertTransaction(ctx context.Context, expense domain.Transaction) (domain.Transaction, error) {
	err := r.queries.QueryRowContext(ctx, `INSERT INTO expenses(amount, category, description, date)
		VALUES ($1::numeric, $2, $3, $4) RETURNING id`,
		strconv.FormatFloat(expense.Amount, 'f', -1, 64), encodeText(expense.Category), encodeText(expense.Description), encodeText(expense.Date)).
		Scan(&expense.ID)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("insert expense: %w", err)
	}
	return expense, nil
}

// ListTransactions preserves Gateway's insertion order and its free-form dates.
func (r *Repository) ListTransactions(ctx context.Context) ([]domain.Transaction, error) {
	rows, err := r.queries.QueryContext(ctx, `SELECT id, amount, category, COALESCE(description, ''), date
		FROM expenses ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	defer rows.Close()
	expenses := make([]domain.Transaction, 0)
	for rows.Next() {
		var expense domain.Transaction
		if err := rows.Scan(&expense.ID, &expense.Amount, &expense.Category, &expense.Description, &expense.Date); err != nil {
			return nil, fmt.Errorf("read expense: %w", err)
		}
		if expense.Category, err = decodeText(expense.Category); err != nil {
			return nil, fmt.Errorf("read expense category: %w", err)
		}
		if expense.Description, err = decodeText(expense.Description); err != nil {
			return nil, fmt.Errorf("read expense description: %w", err)
		}
		if expense.Date, err = decodeText(expense.Date); err != nil {
			return nil, fmt.Errorf("read expense date: %w", err)
		}
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expenses: %w", err)
	}
	return expenses, nil
}

// SumByCategory returns the exact decimal aggregate used by the budget rule.
func (r *Repository) SumByCategory(ctx context.Context, category string) (*big.Rat, error) {
	var total string
	if err := r.queries.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(amount), 0)::text FROM expenses WHERE category = $1", encodeText(category)).
		Scan(&total); err != nil {
		return nil, fmt.Errorf("sum category expenses: %w", err)
	}
	amount, ok := new(big.Rat).SetString(total)
	if !ok {
		return nil, fmt.Errorf("sum category expenses: invalid decimal %q", total)
	}
	return amount, nil
}

// GetReportSummary aggregates canonical dates only; Gateway's undated or
// free-form dated expenses still count toward budgets but have no report day.
func (r *Repository) GetReportSummary(ctx context.Context, from, to string) (domain.ReportSummary, error) {
	rows, err := r.queries.QueryContext(ctx, `SELECT category, SUM(amount), SUM(SUM(amount)) OVER ()
		FROM expenses
		WHERE CASE WHEN date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' AND pg_input_is_valid(date, 'date')
			THEN date::date BETWEEN $1::date AND $2::date ELSE FALSE END
		GROUP BY category ORDER BY category`, from, to)
	if err != nil {
		return domain.ReportSummary{}, fmt.Errorf("query report summary: %w", err)
	}
	defer rows.Close()
	report := domain.ReportSummary{From: from, To: to, Categories: make([]domain.CategoryTotal, 0)}
	for rows.Next() {
		var category domain.CategoryTotal
		if err := rows.Scan(&category.Category, &category.Amount, &report.Total); err != nil {
			return domain.ReportSummary{}, fmt.Errorf("read report summary: %w", err)
		}
		if category.Category, err = decodeText(category.Category); err != nil {
			return domain.ReportSummary{}, fmt.Errorf("read report category: %w", err)
		}
		report.Categories = append(report.Categories, category)
	}
	if err := rows.Err(); err != nil {
		return domain.ReportSummary{}, fmt.Errorf("iterate report summary: %w", err)
	}
	return report, nil
}

// PostgreSQL TEXT cannot contain NUL. Escaping it and backslashes preserves all
// JSON string values while leaving ordinary text and canonical dates unchanged.
func encodeText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\x00", "\\0")
}

func decodeText(value string) (string, error) {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		i++
		if i == len(value) {
			return "", errors.New("stored text contains an incomplete escape")
		}
		switch value[i] {
		case '\\':
			decoded.WriteByte('\\')
		case '0':
			decoded.WriteByte(0)
		default:
			return "", errors.New("stored text contains an invalid escape")
		}
	}
	return decoded.String(), nil
}
