// Package store persists Ledger data in PostgreSQL and caches report summaries.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/skitchen1992/Ledger/ledger"
)

const queryTimeout = 5 * time.Second
const reportTTL = 30 * time.Second

// ErrInvalidInput identifies values that cannot be stored without losing data.
var ErrInvalidInput = errors.New("invalid input")

// Store uses the caller's connections; the caller remains responsible for closing them.
type Store struct {
	db    *sql.DB
	cache *redis.Client
}

func New(db *sql.DB, cache *redis.Client) *Store {
	return &Store{db: db, cache: cache}
}

// money preserves the decimal value rather than silently rounding to NUMERIC(14,2).
func money(value float64) (string, error) {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	amount, ok := new(big.Rat).SetString(text)
	if !ok || amount.Sign() <= 0 {
		return "", fmt.Errorf("%w: money must be positive and finite", ErrInvalidInput)
	}
	cents := new(big.Rat).Mul(amount, big.NewRat(100, 1))
	if !cents.IsInt() || cents.Cmp(big.NewRat(99999999999999, 1)) > 0 {
		return "", fmt.Errorf("%w: money must have at most two decimal places and be at most 999999999999.99", ErrInvalidInput)
	}
	return text, nil
}

func parseDate(value string) (time.Time, error) {
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != value {
		return time.Time{}, fmt.Errorf("%w: date must be a valid YYYY-MM-DD", ErrInvalidInput)
	}
	return date, nil
}

// lockCategory serializes budget changes and expenses, including unbudgeted categories.
func lockCategory(ctx context.Context, tx *sql.Tx, category string) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", category)
	return err
}

func (s *Store) SetBudget(b ledger.Budget) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	limit, err := money(b.Limit)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin budget update: %w", err)
	}
	defer tx.Rollback()
	if err := lockCategory(ctx, tx, b.Category); err != nil {
		return fmt.Errorf("lock budget category: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO budgets(category, limit_amount) VALUES ($1, $2::numeric)
		ON CONFLICT(category) DO UPDATE SET limit_amount = EXCLUDED.limit_amount`, b.Category, limit); err != nil {
		return fmt.Errorf("set budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit budget: %w", err)
	}
	return nil
}

func (s *Store) ListBudgets() ([]ledger.Budget, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT category, limit_amount FROM budgets ORDER BY category")
	if err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	defer rows.Close()
	budgets := make([]ledger.Budget, 0)
	for rows.Next() {
		var b ledger.Budget
		if err := rows.Scan(&b.Category, &b.Limit); err != nil {
			return nil, fmt.Errorf("read budget: %w", err)
		}
		budgets = append(budgets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate budgets: %w", err)
	}
	return budgets, nil
}

func (s *Store) AddTransaction(tx ledger.Transaction) error {
	_, err := s.CreateTransaction(context.Background(), tx)
	return err
}

// CreateTransaction returns the actual generated ID, independent of list ordering.
func (s *Store) CreateTransaction(ctx context.Context, expense ledger.Transaction) (ledger.Transaction, error) {
	if err := expense.Validate(); err != nil {
		return ledger.Transaction{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	amount, err := money(expense.Amount)
	if err != nil {
		return ledger.Transaction{}, err
	}
	date, err := parseDate(expense.Date)
	if err != nil {
		return ledger.Transaction{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ledger.Transaction{}, fmt.Errorf("begin expense: %w", err)
	}
	defer tx.Rollback()
	if err := lockCategory(ctx, tx, expense.Category); err != nil {
		return ledger.Transaction{}, fmt.Errorf("lock expense category: %w", err)
	}
	var exceeded bool
	err = tx.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT SUM(amount) FROM expenses WHERE category = $1), 0) + $2::numeric > limit_amount
		FROM budgets WHERE category = $1`, expense.Category, amount).Scan(&exceeded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ledger.Transaction{}, fmt.Errorf("check expense budget: %w", err)
	}
	if exceeded {
		return ledger.Transaction{}, fmt.Errorf("category %q: %w", expense.Category, ledger.ErrBudgetExceeded)
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO expenses(amount, category, description, date)
		VALUES ($1::numeric, $2, $3, $4) RETURNING id`, amount, expense.Category, expense.Description, date).Scan(&expense.ID); err != nil {
		return ledger.Transaction{}, fmt.Errorf("insert expense: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ledger.Transaction{}, fmt.Errorf("commit expense: %w", err)
	}
	return expense, nil
}

func (s *Store) ListTransactions() ([]ledger.Transaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, amount, category, COALESCE(description, ''), date
		FROM expenses ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	defer rows.Close()
	expenses := make([]ledger.Transaction, 0)
	for rows.Next() {
		var expense ledger.Transaction
		var date time.Time
		if err := rows.Scan(&expense.ID, &expense.Amount, &expense.Category, &expense.Description, &date); err != nil {
			return nil, fmt.Errorf("read expense: %w", err)
		}
		expense.Date = date.Format(time.DateOnly)
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expenses: %w", err)
	}
	return expenses, nil
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

// GetReportSummary includes both dates. Reports may lag writes by up to reportTTL.
// Redis errors never replace a successful database report with an error or empty data.
func (s *Store) GetReportSummary(ctx context.Context, from, to string) (ReportSummary, error) {
	start, err := parseDate(from)
	if err != nil {
		return ReportSummary{}, err
	}
	end, err := parseDate(to)
	if err != nil {
		return ReportSummary{}, err
	}
	if start.After(end) {
		return ReportSummary{}, fmt.Errorf("%w: from must not be after to", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	key := "report:summary:" + from + ":" + to
	if s.cache != nil {
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		data, cacheErr := s.cache.Get(cacheCtx, key).Bytes()
		cacheCancel()
		if cacheErr == nil {
			var cached ReportSummary
			if json.Unmarshal(data, &cached) == nil && cached.From == from && cached.To == to && cached.Categories != nil {
				log.Printf("report cache hit key=%s", key)
				return cached, nil
			}
			log.Printf("report cache invalid JSON/value key=%s; reading PostgreSQL", key)
		} else if !errors.Is(cacheErr, redis.Nil) {
			log.Printf("report cache read failed key=%s: %v", key, cacheErr)
		}
	}
	log.Printf("report cache miss key=%s", key)
	rows, err := s.db.QueryContext(ctx, `SELECT category, SUM(amount), SUM(SUM(amount)) OVER ()
		FROM expenses WHERE date >= $1 AND date <= $2 GROUP BY category ORDER BY category`, start, end)
	if err != nil {
		return ReportSummary{}, fmt.Errorf("query report summary: %w", err)
	}
	defer rows.Close()
	report := ReportSummary{From: from, To: to, Categories: make([]CategoryTotal, 0)}
	for rows.Next() {
		var category CategoryTotal
		if err := rows.Scan(&category.Category, &category.Amount, &report.Total); err != nil {
			return ReportSummary{}, fmt.Errorf("read report summary: %w", err)
		}
		report.Categories = append(report.Categories, category)
	}
	if err := rows.Err(); err != nil {
		return ReportSummary{}, fmt.Errorf("iterate report summary: %w", err)
	}
	if s.cache != nil {
		data, err := json.Marshal(report)
		if err != nil {
			return ReportSummary{}, fmt.Errorf("encode report summary: %w", err)
		}
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cacheCancel()
		if err := s.cache.Set(cacheCtx, key, data, reportTTL).Err(); err != nil {
			log.Printf("report cache write failed key=%s: %v", key, err)
		}
	}
	return report, nil
}
