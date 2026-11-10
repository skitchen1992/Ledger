// Package httpapi exposes the persistent Ledger service over HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/skitchen1992/Ledger/ledger/service"
)

type handler struct {
	service service.ReportingService
}

// NewHandler serves Ledger's budgets, transactions, and cached report summary.
func NewHandler(application service.ReportingService) http.Handler {
	h := &handler{service: application}
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
		summary, err := h.service.GetReportSummary(r.Context(), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, summary)
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

func (h *handler) createBudget(w http.ResponseWriter, r *http.Request) {
	var budget service.Budget
	if err := decodeJSON(r.Body, &budget); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	if err := h.service.SetBudget(r.Context(), budget); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, budget)
}

func (h *handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	budgets, err := h.service.ListBudgets(r.Context())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if budgets == nil {
		budgets = []service.Budget{}
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

func transactionJSON(tx service.Transaction) transactionResponse {
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
	tx := service.Transaction{
		Amount: request.Amount, Category: request.Category,
		Description: request.Description, Date: request.Date,
	}
	tx, err := h.service.CreateTransaction(r.Context(), tx)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, transactionJSON(tx))
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	transactions, err := h.service.ListTransactions(r.Context())
	if err != nil {
		writeServiceError(w, r, err)
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
	writeServiceError(w, r, err)
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), r.Context().Err() != nil:
		writeError(w, r, http.StatusGatewayTimeout, "request timed out or canceled")
	case errors.Is(err, service.ErrBudgetExceeded):
		writeError(w, r, http.StatusConflict, "budget exceeded")
	case errors.Is(err, service.ErrInvalidInput):
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
