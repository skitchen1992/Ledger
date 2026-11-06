// Package domain defines Ledger entities and the contracts needed by business logic.
package domain

import (
	"errors"
	"math"
	"strings"
)

// Validatable задаёт контракт проверки данных без изменения состояния и логирования.
type Validatable interface {
	Validate() error
}

// CheckValid проверяет данные любого типа, реализующего Validatable.
func CheckValid(v Validatable) error {
	return v.Validate()
}

// Transaction описывает расход. Amount задан в рублях, Date — строка YYYY-MM-DD.
type Transaction struct {
	ID          int
	Amount      float64
	Category    string
	Description string
	Date        string
}

// Validate проверяет категорию и сумму расхода. Формат строковой Date не проверяется.
func (tx Transaction) Validate() error {
	if strings.TrimSpace(tx.Category) == "" {
		return errors.New("transaction category must not be empty")
	}
	if tx.Amount <= 0 || math.IsNaN(tx.Amount) || math.IsInf(tx.Amount, 0) {
		return errors.New("transaction amount must be a positive finite number")
	}
	return nil
}

// Budget задаёт фиксированный лимит расходов категории в рублях.
type Budget struct {
	Category string  `json:"category"`
	Limit    float64 `json:"limit"`
}

// Validate проверяет непустую категорию и положительный конечный лимит.
func (b Budget) Validate() error {
	if strings.TrimSpace(b.Category) == "" {
		return errors.New("budget category must not be empty")
	}
	if b.Limit <= 0 || math.IsNaN(b.Limit) || math.IsInf(b.Limit, 0) {
		return errors.New("budget limit must be a positive finite number")
	}
	return nil
}

// ErrBudgetExceeded означает, что расход превысил бы бюджет категории.
var ErrBudgetExceeded = errors.New("budget exceeded")
