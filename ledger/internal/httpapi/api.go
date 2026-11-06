// Package httpapi exposes the persistent Ledger service over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/skitchen1992/Ledger/ledger"
	"github.com/skitchen1992/Ledger/ledger/internal/store"
)

type handler struct {
	store *store.Store
}

// NewHandler serves Ledger's budgets, transactions, and cached report summary.
func NewHandler(storage *store.Store) http.Handler {
	h := &handler{store: storage}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		defer func() {
			log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(started))
		}()
		h.ServeHTTP(w, r)
	})
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ping":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, "GET")
			return
		}
		writeJSON(w, r, http.StatusOK, "pong")
	case "/api/budgets":
		switch r.Method {
		case http.MethodGet:
			h.listBudgets(w, r)
		case http.MethodPost:
			h.createBudget(w, r)
		default:
			methodNotAllowed(w, r, "GET, POST")
		}
	case "/api/transactions":
		switch r.Method {
		case http.MethodGet:
			h.listTransactions(w, r)
		case http.MethodPost:
			h.createTransaction(w, r)
		default:
			methodNotAllowed(w, r, "GET, POST")
		}
	case "/api/reports/summary":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, "GET")
			return
		}
		summary, err := h.store.GetReportSummary(r.Context(), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, summary)
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

func (h *handler) createBudget(w http.ResponseWriter, r *http.Request) {
	var budget ledger.Budget
	if err := decodeJSON(r.Body, &budget); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	if err := budget.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.SetBudget(budget); err != nil {
		writeStoreError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, budget)
}

func (h *handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	budgets, err := h.store.ListBudgets()
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	if budgets == nil {
		budgets = []ledger.Budget{}
	}
	writeJSON(w, r, http.StatusOK, budgets)
}

type transactionRequest struct {
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

type transactionResponse struct {
	ID          int     `json:"id"`
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

func transactionJSON(tx ledger.Transaction) transactionResponse {
	return transactionResponse{
		ID: tx.ID, Amount: tx.Amount, Category: tx.Category,
		Description: tx.Description, Date: tx.Date,
	}
}

func (h *handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	var request transactionRequest
	if err := decodeJSON(r.Body, &request); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	tx := ledger.Transaction{
		Amount: request.Amount, Category: request.Category,
		Description: request.Description, Date: request.Date,
	}
	if err := tx.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := h.store.CreateTransaction(r.Context(), tx)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, transactionJSON(tx))
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	transactions, err := h.store.ListTransactions()
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	response := make([]transactionResponse, 0, len(transactions))
	for _, tx := range transactions {
		response = append(response, transactionJSON(tx))
	}
	writeJSON(w, r, http.StatusOK, response)
}

var errExtraJSON = errors.New("request body must contain only one JSON object")

func decodeJSON[T any](body io.Reader, value *T) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errExtraJSON
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errExtraJSON) ||
		errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		writeError(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	writeStoreError(w, r, err)
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ledger.ErrBudgetExceeded):
		writeError(w, r, http.StatusConflict, "budget exceeded")
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, err.Error())
	default:
		log.Printf("handle %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, r, http.StatusInternalServerError, "internal server error")
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON[T any](w http.ResponseWriter, r *http.Request, status int, value T) {
	body, err := json.Marshal(value)
	if err != nil {
		log.Printf("encode %s response: %v", r.URL.Path, err)
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		log.Printf("write %s response: %v", r.URL.Path, err)
	}
}
