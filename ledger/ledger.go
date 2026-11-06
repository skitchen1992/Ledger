package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"

	"github.com/skitchen1992/Ledger/ledger/domain"
)

// These aliases preserve the API of the original in-memory Ledger.
// Entity definitions and validation belong to the domain package.
type Transaction = domain.Transaction
type Budget = domain.Budget
type Validatable = domain.Validatable

var ErrBudgetExceeded = domain.ErrBudgetExceeded

func CheckValid(v Validatable) error { return domain.CheckValid(v) }

// Ledger хранит транзакции и бюджеты в памяти. Нулевое значение готово к использованию.
type Ledger struct {
	transactions []Transaction
	budgets      map[string]Budget
}

// Reset очищает транзакции и бюджеты; ID новых транзакций снова начинаются с 1.
func (l *Ledger) Reset() {
	*l = Ledger{}
}

// AddTransaction проверяет расход и сохраняет его с очередным ID, начиная с 1.
// Переданный ID заменяется; при ошибке история и следующий ID не меняются.
func (l *Ledger) AddTransaction(tx Transaction) error {
	if err := tx.Validate(); err != nil {
		return err
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
// При ошибке валидации состояние не меняется.
func (l *Ledger) SetBudget(b Budget) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if l.budgets == nil {
		l.budgets = make(map[string]Budget)
	}
	l.budgets[b.Category] = b
	return nil
}

// ListBudgets возвращает независимую копию бюджетов в порядке категорий.
func (l *Ledger) ListBudgets() []Budget {
	budgets := make([]Budget, 0, len(l.budgets))
	for _, budget := range l.budgets {
		budgets = append(budgets, budget)
	}
	sort.Slice(budgets, func(i, j int) bool {
		return budgets[i].Category < budgets[j].Category
	})
	return budgets
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
		if err := l.SetBudget(budget); err != nil {
			return fmt.Errorf("set budget %q: %w", budget.Category, err)
		}
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
		if item.Limit == nil {
			return nil, fmt.Errorf("budget %d: limit is required and must not be null", i+1)
		}
		budget := Budget{Category: item.Category, Limit: *item.Limit}
		if err := budget.Validate(); err != nil {
			return nil, fmt.Errorf("budget %d: %w", i+1, err)
		}
		budgets = append(budgets, budget)
	}
	return budgets, nil
}
