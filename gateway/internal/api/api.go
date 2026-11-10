package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeTimeoutError(w, r)
	case errors.Is(err, service.ErrBudgetExceeded):
		writeError(w, r, http.StatusConflict, "budget exceeded")
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, err.Error())
	default:
		writeInternalError(w, r, err)
	}
}
