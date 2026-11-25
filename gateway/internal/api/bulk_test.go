package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

type bulkErrorJSON struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

type bulkJSON struct {
	Accepted int64           `json:"accepted"`
	Rejected int64           `json:"rejected"`
	Errors   []bulkErrorJSON `json:"errors"`
}

func TestBulkImportReturns200WithSummaryOfAcceptedAndRejectedElements(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":10}`, http.StatusCreated)
	body := `[
		{"amount":4,"category":"еда","description":"ланч","date":"2026-11-01","id":99},
		{"amount":0,"category":"еда"},
		{"amount":5,"category":" "},
		{"amount":7,"category":"еда"},
		{"amount":6,"category":"еда","date":"любая дата"},
		null,
		{"amount":0.00001,"category":"книги"}
	]`
	// One worker processes the array in input order, so budget rejections are deterministic.
	response := request(t, handler, http.MethodPost, "/api/transactions/bulk?workers=1", body, http.StatusOK)
	want := bulkJSON{Accepted: 3, Rejected: 4, Errors: []bulkErrorJSON{
		{Index: 1, Error: "transaction amount must be a positive finite number"},
		{Index: 2, Error: "transaction category must not be empty"},
		{Index: 3, Error: "budget exceeded"},
		{Index: 5, Error: "transaction category must not be empty"},
	}}
	if got := decodeResponse[bulkJSON](t, response); !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	history := []transactionJSON{
		{ID: 1, Amount: 4, Category: "еда", Description: "ланч", Date: "2026-11-01"},
		{ID: 2, Amount: 6, Category: "еда", Date: "любая дата"},
		{ID: 3, Amount: 0.00001, Category: "книги"},
	}
	if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, history) {
		t.Fatalf("history = %+v, want accepted elements only %+v", got, history)
	}
}

func TestBulkImportWithDefaultWorkersKeepsBudgetUnderConcurrency(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":25}`, http.StatusCreated)
	elements := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		category := "еда"
		if i%3 == 0 {
			category = "транспорт"
		}
		elements = append(elements, `{"amount":1,"category":"`+category+`"}`)
	}
	response := request(t, handler, http.MethodPost, "/api/transactions/bulk", "["+strings.Join(elements, ",")+"]", http.StatusOK)
	summary := decodeResponse[bulkJSON](t, response)
	// 40 food elements compete for 25 units; 20 unbudgeted transport elements all pass.
	if summary.Accepted != 45 || summary.Rejected != 15 || len(summary.Errors) != 15 {
		t.Fatalf("summary = %+v, want 45 accepted and 15 rejected", summary)
	}
	for i, element := range summary.Errors {
		if element.Index%3 == 0 || element.Error != "budget exceeded" || (i > 0 && element.Index <= summary.Errors[i-1].Index) {
			t.Fatalf("errors must be ordered budget rejections of food elements: %+v", summary.Errors)
		}
	}
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	history := decodeResponse[[]transactionJSON](t, response)
	food, ids := 0, make(map[int]bool)
	for _, tx := range history {
		if tx.Category == "еда" {
			food++
		}
		if ids[tx.ID] {
			t.Errorf("duplicate ID %d in history", tx.ID)
		}
		ids[tx.ID] = true
	}
	if len(history) != 45 || food != 25 {
		t.Fatalf("history has %d rows and %d food rows, want 45 and 25", len(history), food)
	}
}

func TestBulkImportEmptyArrayReturnsEmptyErrors(t *testing.T) {
	response := request(t, newTestHandler(), http.MethodPost, "/api/transactions/bulk?workers=32", `[]`, http.StatusOK)
	if got := response.Body.String(); got != `{"accepted":0,"rejected":0,"errors":[]}`+"\n" {
		t.Fatalf("empty import body = %q", got)
	}
}

func TestBulkImportRejectsInvalidRequestsBeforeAnyWrite(t *testing.T) {
	valid := `[{"amount":1,"category":"еда"}]`
	for _, tc := range []struct {
		name, query, body, wantError string
	}{
		{name: "empty body", body: ""},
		{name: "invalid JSON", body: `[{"amount":1,"category":"еда"}`},
		{name: "object root", body: `{"amount":1,"category":"еда"}`},
		{name: "null root", body: `null`, wantError: "invalid JSON: request body must be an array of transactions"},
		{name: "wrong element type", body: `[{"amount":1,"category":"еда"},{"amount":"2","category":"еда"}]`},
		{name: "scalar element", body: `[{"amount":1,"category":"еда"},5]`},
		{name: "second document", body: valid + ` []`},
		{name: "not a number", query: "workers=four", body: valid, wantError: "workers must be an integer between 1 and 32"},
		{name: "empty workers", query: "workers=", body: valid, wantError: "workers must be an integer between 1 and 32"},
		{name: "zero workers", query: "workers=0", body: valid, wantError: "invalid input: workers must be between 1 and 32"},
		{name: "negative workers", query: "workers=-2", body: valid, wantError: "invalid input: workers must be between 1 and 32"},
		{name: "too many workers", query: "workers=33", body: valid, wantError: "invalid input: workers must be between 1 and 32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHandler()
			request(t, handler, http.MethodPost, "/api/transactions", `{"amount":5,"category":"книги"}`, http.StatusCreated)
			response := request(t, handler, http.MethodPost, "/api/transactions/bulk?"+tc.query, tc.body, http.StatusBadRequest)
			assertJSONError(t, response, tc.wantError)
			response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
			if got, want := decodeResponse[[]transactionJSON](t, response), []transactionJSON{{ID: 1, Amount: 5, Category: "книги"}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("rejected request changed history: %+v", got)
			}
		})
	}
}

func TestBulkImportRoutingAndMethods(t *testing.T) {
	handler := newTestHandler()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			response := request(t, handler, method, "/api/transactions/bulk", "", http.StatusMethodNotAllowed)
			assertJSONError(t, response, "method not allowed")
			if got := response.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
		})
	}
	assertJSONError(t, request(t, handler, http.MethodPost, "/api/transactions/bulk/", "[]", http.StatusNotFound), "not found")
}

func TestBulkImportHidesRepositoryFailuresPerElement(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	repository := newMemoryRepository()
	repository.state.err = errors.New("SQL password=secret failed")
	response := request(t, reportingHandler(repository), http.MethodPost, "/api/transactions/bulk?workers=2",
		`[{"amount":1,"category":"еда"},{"amount":0,"category":"еда"}]`, http.StatusOK)
	want := bulkJSON{Accepted: 0, Rejected: 2, Errors: []bulkErrorJSON{
		{Index: 0, Error: "internal server error"},
		{Index: 1, Error: "transaction amount must be a positive finite number"},
	}}
	if got := decodeResponse[bulkJSON](t, response); !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	if got := output.String(); !strings.Contains(got, "element 0: SQL password=secret failed") {
		t.Errorf("log = %q, want the hidden repository cause for element 0", got)
	}
}

// slowCategoryRepository blocks units of work for the "slow" category until
// the request context ends, as a locked PostgreSQL row would.
type slowCategoryRepository struct {
	memoryRepository
	started chan struct{}
}

func (r slowCategoryRepository) WithinCategory(ctx context.Context, category string, apply func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	if category != "slow" {
		return r.memoryRepository.WithinCategory(ctx, category, apply)
	}
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return fmt.Errorf("lock category: %w", ctx.Err())
}

func TestBulkImportTimeoutReturns504AndKeepsAlreadyProcessedElements(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	repository := slowCategoryRepository{memoryRepository: newMemoryRepository(), started: make(chan struct{}, 1)}
	application := service.NewReporting(service.New(repository, repository, repository), repository, nil)
	handler, err := NewHandlerWithTimeout(application, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	body := `[{"amount":1,"category":"еда"},{"amount":0,"category":"еда"},{"amount":1,"category":"slow"},{"amount":2,"category":"еда"}]`
	response := request(t, handler, http.MethodPost, "/api/transactions/bulk?workers=1", body, http.StatusGatewayTimeout)
	assertJSONError(t, response, timeoutMessage)
	if got := response.Header().Get("Connection"); got != "close" {
		t.Errorf("Connection = %q, want close for timeout response", got)
	}
	select {
	case <-repository.started:
	default:
		t.Fatal("the slow element was never processed")
	}
	if got := output.String(); !strings.Contains(got, "bulk import interrupted: accepted=1 rejected=1 unprocessed=2") {
		t.Errorf("log = %q, want the partial summary", got)
	}
	// Elements committed before the timeout stay saved; the rest leave no rows.
	history := request(t, reportingHandler(repository.memoryRepository), http.MethodGet, "/api/transactions", "", http.StatusOK)
	if got, want := decodeResponse[[]transactionJSON](t, history), []transactionJSON{{ID: 1, Amount: 1, Category: "еда"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history after timeout = %+v, want %+v", got, want)
	}
}
