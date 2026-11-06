package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Transaction описывает расход. Amount задан в рублях, Date — строка YYYY-MM-DD.
type Transaction struct {
	ID          int
	Amount      float64
	Category    string
	Description string
	Date        string
}

// Budget задаёт фиксированный лимит расходов категории в рублях.
type Budget struct {
	Category string  `json:"category"`
	Limit    float64 `json:"limit"`
}

// ErrBudgetExceeded означает, что расход превысил бы бюджет категории.
var ErrBudgetExceeded = errors.New("budget exceeded")

// Ledger хранит транзакции и бюджеты в памяти. Нулевое значение готово к использованию.
type Ledger struct {
	transactions []Transaction
	budgets      map[string]Budget
}

// AddTransaction проверяет расход и сохраняет его с очередным ID, начиная с 1.
// Переданный ID заменяется; при ошибке история и следующий ID не меняются.
func (l *Ledger) AddTransaction(tx Transaction) error {
	if strings.TrimSpace(tx.Category) == "" {
		return errors.New("transaction category must not be empty")
	}
	if tx.Amount <= 0 || math.IsNaN(tx.Amount) || math.IsInf(tx.Amount, 0) {
		return errors.New("transaction amount must be a positive finite number")
	}

	if budget, ok := l.budgets[tx.Category]; ok {
		total := decimalAmount(tx.Amount)
		for _, saved := range l.transactions {
			if saved.Category == tx.Category {
				total.Add(total, decimalAmount(saved.Amount))
			}
		}
		if total.Cmp(decimalAmount(budget.Limit)) > 0 {
			return fmt.Errorf("category %q, limit %.2f: %w", tx.Category, budget.Limit, ErrBudgetExceeded)
		}
	}

	tx.ID = len(l.transactions) + 1
	l.transactions = append(l.transactions, tx)
	return nil
}

// decimalAmount точно представляет кратчайшую десятичную запись конечного float64.
// Это позволяет принять 0.10 + 0.20 при лимите 0.30 без допуска превышения.
func decimalAmount(value float64) *big.Rat {
	amount, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	if !ok {
		panic("decimalAmount requires a finite value")
	}
	return amount
}

// ListTransactions возвращает независимую копию истории в порядке добавления.
func (l *Ledger) ListTransactions() []Transaction {
	transactions := make([]Transaction, len(l.transactions))
	copy(transactions, l.transactions)
	return transactions
}

// SetBudget добавляет или заменяет бюджет категории, сохраняя историю расходов.
// Вызывающий код передаёт непустую категорию и конечный неотрицательный лимит.
// Для внешних JSON-данных эти условия проверяет LoadBudgets.
func (l *Ledger) SetBudget(b Budget) {
	if l.budgets == nil {
		l.budgets = make(map[string]Budget)
	}
	l.budgets[b.Category] = b
}

// LoadBudgets принимает один JSON-массив бюджетов и применяет его целиком.
// При любой ошибке состояние не меняется. Повторная категория заменяет бюджет,
// пустой массив ничего не меняет. Переданный источник остаётся открытым.
func (l *Ledger) LoadBudgets(r io.Reader) error {
	budgets, err := readBudgets(r)
	if err != nil {
		return err
	}
	for _, budget := range budgets {
		l.SetBudget(budget)
	}
	return nil
}

func readBudgets(r io.Reader) ([]Budget, error) {
	var input []struct {
		Category string   `json:"category"`
		Limit    *float64 `json:"limit"`
	}
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("read budgets JSON array: %w", err)
	}
	if input == nil {
		return nil, errors.New("budgets must be a JSON array, not null")
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("read after budgets JSON array: %w", err)
		}
		return nil, errors.New("budgets must contain only one JSON array")
	}

	budgets := make([]Budget, 0, len(input))
	for i, item := range input {
		if strings.TrimSpace(item.Category) == "" {
			return nil, fmt.Errorf("budget %d: category must not be empty", i+1)
		}
		if item.Limit == nil {
			return nil, fmt.Errorf("budget %d: limit is required and must not be null", i+1)
		}
		if *item.Limit < 0 || math.IsNaN(*item.Limit) || math.IsInf(*item.Limit, 0) {
			return nil, fmt.Errorf("budget %d: limit must be a nonnegative finite number", i+1)
		}
		budgets = append(budgets, Budget{Category: item.Category, Limit: *item.Limit})
	}
	return budgets, nil
}
