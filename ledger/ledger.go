package main

import (
	"errors"
	"math"
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

// Ledger хранит транзакции в памяти. Нулевое значение готово к использованию.
type Ledger struct {
	transactions []Transaction
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

	tx.ID = len(l.transactions) + 1
	l.transactions = append(l.transactions, tx)
	return nil
}

// ListTransactions возвращает независимую копию истории в порядке добавления.
func (l *Ledger) ListTransactions() []Transaction {
	transactions := make([]Transaction, len(l.transactions))
	copy(transactions, l.transactions)
	return transactions
}
