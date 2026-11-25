package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/skitchen1992/Ledger/ledger/service"
)

type handler struct {
	service service.ReportingService
}

// NewHandler создаёт HTTP-маршруты над готовым сервисом Ledger.
func NewHandler(s service.ReportingService) http.Handler {
	return loggingMiddleware(timeoutMiddleware(&handler{service: s}, DefaultRequestTimeout))
}

// NewHandlerWithTimeout задаёт положительный таймаут для всех маршрутов.
func NewHandlerWithTimeout(s service.ReportingService, timeout time.Duration) (http.Handler, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}
	return loggingMiddleware(timeoutMiddleware(&handler{service: s}, timeout)), nil
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
	case "/api/transactions/bulk":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, "POST")
			return
		}
		h.importTransactions(w, r)
	case "/api/budgets":
		switch r.Method {
		case http.MethodGet:
			h.listBudgets(w, r)
		case http.MethodPost:
			h.createBudget(w, r)
		default:
			methodNotAllowed(w, r, "GET, POST")
		}
	case "/api/reports/summary":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, "GET")
			return
		}
		h.reportSummary(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

func (h *handler) reportSummary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, to := query.Get("from"), query.Get("to")
	for _, parameter := range []struct{ name, value string }{{"from", from}, {"to", to}} {
		date, err := time.Parse(time.DateOnly, parameter.value)
		if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != parameter.value {
			writeError(w, r, http.StatusBadRequest, parameter.name+" must be a valid YYYY-MM-DD date")
			return
		}
	}
	summary, err := h.service.GetReportSummary(r.Context(), from, to)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	amounts := make(map[string]float64, len(summary.Categories))
	for _, total := range summary.Categories {
		amounts[total.Category] = total.Amount
	}
	writeJSON(w, r, http.StatusOK, amounts)
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
	tx, err := h.service.CreateTransaction(r.Context(), request.toTransaction())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, transactionResponse(tx))
}

// importTransactions decodes the whole array before the first write. The status
// is 200 even when elements are rejected; an interrupted import returns 504.
func (h *handler) importTransactions(w http.ResponseWriter, r *http.Request) {
	workers := service.DefaultImportWorkers
	if query := r.URL.Query(); query.Has("workers") {
		value, err := strconv.Atoi(query.Get("workers"))
		if err != nil {
			writeError(w, r, http.StatusBadRequest,
				fmt.Sprintf("workers must be an integer between 1 and %d", service.MaxImportWorkers))
			return
		}
		workers = value
	}
	var requests []CreateTransactionRequest
	if err := decodeJSON(r.Body, &requests); err != nil {
		writeDecodeError(w, r, err)
		return
	}
	if requests == nil {
		writeError(w, r, http.StatusBadRequest, "invalid JSON: request body must be an array of transactions")
		return
	}
	transactions := make([]service.Transaction, 0, len(requests))
	for _, request := range requests {
		transactions = append(transactions, request.toTransaction())
	}
	summary, err := h.service.ImportTransactions(r.Context(), transactions, workers)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("bulk import interrupted: accepted=%d rejected=%d unprocessed=%d: %v",
				summary.Accepted, summary.Rejected, int64(len(transactions))-summary.Accepted-summary.Rejected, err)
		}
		writeServiceError(w, r, err)
		return
	}
	response := BulkImportResponse{
		Accepted: summary.Accepted,
		Rejected: summary.Rejected,
		Errors:   make([]BulkImportError, 0, len(summary.Errors)),
	}
	for _, importError := range summary.Errors {
		status, message := serviceErrorResponse(importError.Err)
		if status == http.StatusInternalServerError {
			log.Printf("handle %s %s element %d: %v", r.Method, r.URL.Path, importError.Index, importError.Err)
		}
		response.Errors = append(response.Errors, BulkImportError{Index: importError.Index, Error: message})
	}
	writeJSON(w, r, http.StatusOK, response)
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	transactions, err := h.service.ListTransactions(r.Context())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
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
	if err := h.service.SetBudget(r.Context(), b); err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusCreated, budgetResponse(b))
}

func (h *handler) listBudgets(w http.ResponseWriter, r *http.Request) {
	budgets, err := h.service.ListBudgets(r.Context())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	response := make([]BudgetResponse, 0, len(budgets))
	for _, b := range budgets {
		response = append(response, budgetResponse(b))
	}
	writeJSON(w, r, http.StatusOK, response)
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch status, message := serviceErrorResponse(err); status {
	case http.StatusGatewayTimeout:
		writeTimeoutError(w, r)
	case http.StatusInternalServerError:
		writeInternalError(w, r, err)
	default:
		writeError(w, r, status, message)
	}
}

// serviceErrorResponse maps a service error to its public status and message;
// bulk import uses the same messages for rejected elements.
func serviceErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, timeoutMessage
	case errors.Is(err, service.ErrBudgetExceeded):
		return http.StatusConflict, "budget exceeded"
	case errors.Is(err, service.ErrInvalidInput):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}
