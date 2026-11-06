package api

import (
	"errors"
	"net/http"
	"sync"

	"github.com/skitchen1992/Ledger/ledger"
)

type handler struct {
	mu     sync.RWMutex
	ledger ledger.Ledger
}

// NewHandler создаёт маршруты и отдельное пустое хранилище в памяти.
// Все обращения к Ledger защищены одним mutex, включая чтение назначенного ID.
func NewHandler() http.Handler {
	return loggingMiddleware(&handler{})
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ping":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, "GET")
			return
		}
		writeJSON(w, r, http.StatusOK, "pong")
	case "/api/transactions":
		switch r.Method {
		case http.MethodGet:
			h.listTransactions(w, r)
		case http.MethodPost:
			h.createTransaction(w, r)
		default:
			methodNotAllowed(w, r, "GET, POST")
		}
	case "/api/budgets":
		switch r.Method {
		case http.MethodGet:
			h.listBudgets(w, r)
		case http.MethodPost:
			h.createBudget(w, r)
		default:
			methodNotAllowed(w, r, "GET, POST")
		}
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	var request CreateTransactionRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	tx := request.toTransaction()
	if err := tx.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	h.mu.Lock()
	err := h.ledger.AddTransaction(tx)
	if err == nil {
		// AddTransaction принимает значение. Читаем сохранённую запись с ID
		// под той же блокировкой, чтобы другой запрос не заменил последний элемент.
		transactions := h.ledger.ListTransactions()
		tx = transactions[len(transactions)-1]
	}
	h.mu.Unlock()
	if errors.Is(err, ledger.ErrBudgetExceeded) {
		writeError(w, r, http.StatusConflict, "budget exceeded")
		return
	}
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, transactionResponse(tx))
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	transactions := h.ledger.ListTransactions()
	h.mu.RUnlock()
	response := make([]TransactionResponse, 0, len(transactions))
	for _, tx := range transactions {
		response = append(response, transactionResponse(tx))
	}
	writeJSON(w, r, http.StatusOK, response)
}

func (h *handler) createBudget(w http.ResponseWriter, r *http.Request) {
	var request CreateBudgetRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	b := request.toBudget()
	if err := b.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	h.mu.Lock()
	err := h.ledger.SetBudget(b)
	h.mu.Unlock()
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, budgetResponse(b))
}

func (h *handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	budgets := h.ledger.ListBudgets()
	h.mu.RUnlock()
	response := make([]BudgetResponse, 0, len(budgets))
	for _, b := range budgets {
		response = append(response, budgetResponse(b))
	}
	writeJSON(w, r, http.StatusOK, response)
}
