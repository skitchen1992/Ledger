package api

import "github.com/skitchen1992/Ledger/ledger"

// CreateTransactionRequest содержит клиентские поля расхода; ID назначает Ledger.
type CreateTransactionRequest struct {
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

// TransactionResponse описывает публичное представление сохранённого расхода.
type TransactionResponse struct {
	ID          int     `json:"id"`
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

// CreateBudgetRequest используется для создания и обновления лимита категории.
type CreateBudgetRequest struct {
	Category string  `json:"category"`
	Limit    float64 `json:"limit"`
}

// BudgetResponse описывает публичное представление бюджета категории.
type BudgetResponse struct {
	Category string  `json:"category"`
	Limit    float64 `json:"limit"`
}

func (r CreateTransactionRequest) toTransaction() ledger.Transaction {
	return ledger.Transaction{
		Amount: r.Amount, Category: r.Category, Description: r.Description, Date: r.Date,
	}
}

func transactionResponse(tx ledger.Transaction) TransactionResponse {
	return TransactionResponse{
		ID: tx.ID, Amount: tx.Amount, Category: tx.Category, Description: tx.Description, Date: tx.Date,
	}
}

func (r CreateBudgetRequest) toBudget() ledger.Budget {
	return ledger.Budget{Category: r.Category, Limit: r.Limit}
}

func budgetResponse(b ledger.Budget) BudgetResponse {
	return BudgetResponse{Category: b.Category, Limit: b.Limit}
}
