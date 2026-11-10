package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/service"
)

type transactionJSON struct {
	ID          int     `json:"id"`
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

type budgetJSON struct {
	Category string  `json:"category"`
	Limit    float64 `json:"limit"`
}

func TestEmptyCollectionsAndIndependentHandlers(t *testing.T) {
	first := newTestHandler()
	for _, path := range []string{"/api/transactions", "/api/budgets"} {
		response := request(t, first, http.MethodGet, path, "", http.StatusOK)
		if got := strings.TrimSpace(response.Body.String()); got != "[]" {
			t.Errorf("GET %s = %q, want []", path, got)
		}
	}
	request(t, first, http.MethodPost, "/api/budgets", `{"category":"еда","limit":1}`, http.StatusCreated)
	request(t, first, http.MethodPost, "/api/transactions", `{"category":"еда","amount":1}`, http.StatusCreated)

	second := newTestHandler()
	for _, path := range []string{"/api/transactions", "/api/budgets"} {
		response := request(t, second, http.MethodGet, path, "", http.StatusOK)
		if got := strings.TrimSpace(response.Body.String()); got != "[]" {
			t.Errorf("independent handler GET %s = %q, want []", path, got)
		}
	}
	response := request(t, second, http.MethodPost, "/api/transactions", `{"category":"еда","amount":2}`, http.StatusCreated)
	if got := decodeResponse[transactionJSON](t, response); got.ID != 1 {
		t.Errorf("independent handler assigned ID %d, want 1", got.ID)
	}
}

func TestTransactionsAPI(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		status    int
		wantError string
	}{
		{
			name:   "ok",
			body:   `{"amount":80.5,"category":"транспорт","description":"метро","date":"2025-09-11"}`,
			status: http.StatusCreated,
		},
		{
			name:      "exceeded",
			body:      `{"amount":4550.01,"category":"еда","description":"покупки","date":"2025-09-11"}`,
			status:    http.StatusConflict,
			wantError: "budget exceeded",
		},
		{
			name:   "bad_json",
			body:   `{"amount":10,"category":"еда"`,
			status: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each HTTP scenario uses the real service over isolated test repositories.
			handler := newTestHandler()
			budget := budgetJSON{Category: "еда", Limit: 5000}
			response := request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":5000}`, http.StatusCreated)
			if got := decodeResponse[budgetJSON](t, response); got != budget {
				t.Fatalf("created budget = %+v, want %+v", got, budget)
			}
			response = request(t, handler, http.MethodGet, "/api/budgets", "", http.StatusOK)
			if got, want := decodeResponse[[]budgetJSON](t, response), []budgetJSON{budget}; !reflect.DeepEqual(got, want) {
				t.Fatalf("budgets = %+v, want %+v", got, want)
			}

			first := transactionJSON{ID: 1, Amount: 450, Category: "еда", Description: "ланч", Date: "2025-09-10"}
			response = request(t, handler, http.MethodPost, "/api/transactions", `{"amount":450,"category":"еда","description":"ланч","date":"2025-09-10","id":99}`, http.StatusCreated)
			assertTransactionResponse(t, response, first)

			response = request(t, handler, http.MethodPost, "/api/transactions", tc.body, tc.status)
			wantHistory := []transactionJSON{first}
			if tc.status == http.StatusCreated {
				second := transactionJSON{ID: 2, Amount: 80.5, Category: "транспорт", Description: "метро", Date: "2025-09-11"}
				assertTransactionResponse(t, response, second)
				wantHistory = append(wantHistory, second)
			} else {
				assertJSONError(t, response, tc.wantError)
			}
			response = request(t, handler, http.MethodGet, "/api/transactions?check=1", "", http.StatusOK)
			if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, wantHistory) {
				t.Fatalf("history = %+v, want %+v", got, wantHistory)
			}
			if tc.status != http.StatusCreated {
				response = request(t, handler, http.MethodPost, "/api/transactions", `{"amount":1,"category":"еда"}`, http.StatusCreated)
				assertTransactionResponse(t, response, transactionJSON{ID: 2, Amount: 1, Category: "еда"})
			}
		})
	}
}

func TestInvalidTransactionsLeaveHistoryAndNextIDUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "array root", body: `[{"amount":10,"category":"еда"}]`},
		{name: "null root", body: `null`},
		{name: "missing amount", body: `{"category":"еда"}`},
		{name: "null amount", body: `{"amount":null,"category":"еда"}`},
		{name: "zero amount", body: `{"amount":0,"category":"еда"}`},
		{name: "negative amount", body: `{"amount":-1,"category":"еда"}`},
		{name: "nonfinite amount", body: `{"amount":1e999,"category":"еда"}`},
		{name: "string amount", body: `{"amount":"10","category":"еда"}`},
		{name: "missing category", body: `{"amount":10}`},
		{name: "null category", body: `{"amount":10,"category":null}`},
		{name: "blank category", body: `{"amount":10,"category":" \t\n"}`},
		{name: "wrong description type", body: `{"amount":10,"category":"еда","description":5}`},
		{name: "wrong date type", body: `{"amount":10,"category":"еда","date":true}`},
		{name: "second document", body: `{"amount":10,"category":"еда"} {}`},
		{name: "trailing null", body: `{"amount":10,"category":"еда"} null`},
		{name: "trailing garbage", body: `{"amount":10,"category":"еда"} !`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHandler()
			request(t, handler, http.MethodPost, "/api/transactions", `{"amount":5,"category":"книги"}`, http.StatusCreated)
			response := request(t, handler, http.MethodPost, "/api/transactions", tc.body, http.StatusBadRequest)
			assertJSONError(t, response, "")
			response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
			want := []transactionJSON{{ID: 1, Amount: 5, Category: "книги"}}
			if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, want) {
				t.Fatalf("rejection changed history: got %+v, want %+v", got, want)
			}
			response = request(t, handler, http.MethodPost, "/api/transactions", `{"amount":7,"category":"еда"}`, http.StatusCreated)
			if got := decodeResponse[transactionJSON](t, response); got.ID != 2 {
				t.Errorf("ID after rejected transaction = %d, want 2", got.ID)
			}
		})
	}
}

func TestBudgetUpdatesPreserveHistoryAndCumulativeSpending(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/transactions", `{"amount":60,"category":"еда"}`, http.StatusCreated)
	for _, tc := range []struct {
		body   string
		amount string
		status int
		limit  float64
	}{
		{body: `{"category":"еда","limit":100}`, amount: "40", status: http.StatusCreated, limit: 100},
		{body: `{"category":"еда","limit":150}`, amount: "50", status: http.StatusCreated, limit: 150},
		{body: `{"category":"еда","limit":100}`, amount: "1", status: http.StatusConflict, limit: 100},
		{body: `{"category":"еда","limit":200}`, amount: "50", status: http.StatusCreated, limit: 200},
	} {
		response := request(t, handler, http.MethodPost, "/api/budgets", tc.body, http.StatusCreated)
		if got := decodeResponse[budgetJSON](t, response); got != (budgetJSON{Category: "еда", Limit: tc.limit}) {
			t.Errorf("budget response = %+v, want category еда and limit %v", got, tc.limit)
		}
		response = request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":`+tc.amount+`}`, tc.status)
		if tc.status == http.StatusConflict {
			assertJSONError(t, response, "budget exceeded")
		}
	}
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"транспорт","limit":20}`, http.StatusCreated)
	request(t, handler, http.MethodPost, "/api/transactions", `{"category":"транспорт","amount":20}`, http.StatusCreated)
	request(t, handler, http.MethodPost, "/api/transactions", `{"category":"книги","amount":10000}`, http.StatusCreated)
	response := request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":1}`, http.StatusConflict)
	assertJSONError(t, response, "budget exceeded")
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	want := []transactionJSON{
		{ID: 1, Category: "еда", Amount: 60},
		{ID: 2, Category: "еда", Amount: 40},
		{ID: 3, Category: "еда", Amount: 50},
		{ID: 4, Category: "еда", Amount: 50},
		{ID: 5, Category: "транспорт", Amount: 20},
		{ID: 6, Category: "книги", Amount: 10000},
	}
	if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, want) {
		t.Errorf("history after budget updates = %+v, want %+v", got, want)
	}
	response = request(t, handler, http.MethodGet, "/api/budgets", "", http.StatusOK)
	budgets := decodeResponse[[]budgetJSON](t, response)
	if len(budgets) != 2 {
		t.Fatalf("budgets = %+v, want two categories without duplicate updates", budgets)
	}
	got := make(map[string]float64, len(budgets))
	for _, budget := range budgets {
		got[budget.Category] = budget.Limit
	}
	if want := map[string]float64{"еда": 200, "транспорт": 20}; !reflect.DeepEqual(got, want) {
		t.Errorf("budgets = %v, want %v", got, want)
	}
}

func TestFractionalBudgetAllowsExactLimitAndRejectsExcess(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":0.3}`, http.StatusCreated)
	request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":0.1}`, http.StatusCreated)
	request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":0.2}`, http.StatusCreated)
	response := request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":0.0000001}`, http.StatusConflict)
	assertJSONError(t, response, "budget exceeded")
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	want := []transactionJSON{{ID: 1, Category: "еда", Amount: 0.1}, {ID: 2, Category: "еда", Amount: 0.2}}
	if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, want) {
		t.Errorf("history = %+v, want %+v", got, want)
	}
}

func TestGatewayPreservesOptionalDatesAndArbitraryPrecision(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":0.00003}`, http.StatusCreated)
	want := []transactionJSON{
		{ID: 1, Category: "еда", Amount: 0.00001},
		{ID: 2, Category: "еда", Amount: 0.00002, Date: "произвольная дата"},
		{ID: 3, Category: "книги", Amount: 1000000000000, Date: "2020-01-01"},
	}
	for _, tx := range want {
		body, err := json.Marshal(tx)
		if err != nil {
			t.Fatal(err)
		}
		response := request(t, handler, http.MethodPost, "/api/transactions", string(body), http.StatusCreated)
		assertTransactionResponse(t, response, tx)
	}
	response := request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":0.000001}`, http.StatusConflict)
	assertJSONError(t, response, "budget exceeded")
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, want) {
		t.Errorf("history = %+v, want insertion order and unchanged values %+v", got, want)
	}
}

func TestRepositoryFailuresReturnGeneric500AndPreserveState(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/budgets"},
		{method: http.MethodGet, path: "/api/transactions"},
		{method: http.MethodPost, path: "/api/budgets", body: `{"category":"еда","limit":100}`},
		{method: http.MethodPost, path: "/api/transactions", body: `{"category":"еда","amount":1}`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			repository := newMemoryRepository()
			handler := NewHandler(service.NewReporting(service.New(repository, repository, repository), repository, nil))
			request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":10}`, http.StatusCreated)
			request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":4}`, http.StatusCreated)
			repository.state.err = errors.New("database unavailable")
			response := request(t, handler, tc.method, tc.path, tc.body, http.StatusInternalServerError)
			assertJSONError(t, response, "internal server error")
			repository.state.err = nil
			response = request(t, handler, http.MethodGet, "/api/budgets", "", http.StatusOK)
			if got, want := decodeResponse[[]budgetJSON](t, response), []budgetJSON{{Category: "еда", Limit: 10}}; !reflect.DeepEqual(got, want) {
				t.Errorf("failed operation changed budgets: got %+v, want %+v", got, want)
			}
			response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
			if got, want := decodeResponse[[]transactionJSON](t, response), []transactionJSON{{ID: 1, Category: "еда", Amount: 4}}; !reflect.DeepEqual(got, want) {
				t.Errorf("failed operation changed history: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestInvalidBudgetsPreserveBudgetAndHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "invalid JSON", body: `{!`},
		{name: "null root", body: `null`},
		{name: "array root", body: `[]`},
		{name: "missing limit", body: `{"category":"еда"}`},
		{name: "null limit", body: `{"category":"еда","limit":null}`},
		{name: "zero limit", body: `{"category":"еда","limit":0}`},
		{name: "negative limit", body: `{"category":"еда","limit":-1}`},
		{name: "nonfinite limit", body: `{"category":"еда","limit":1e999}`},
		{name: "string limit", body: `{"category":"еда","limit":"100"}`},
		{name: "missing category", body: `{"limit":100}`},
		{name: "null category", body: `{"category":null,"limit":100}`},
		{name: "blank category", body: `{"category":" \t\n","limit":100}`},
		{name: "second document", body: `{"category":"еда","limit":100} {}`},
		{name: "trailing null", body: `{"category":"еда","limit":100} null`},
		{name: "trailing garbage", body: `{"category":"еда","limit":100} !`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHandler()
			request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":10}`, http.StatusCreated)
			request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":4}`, http.StatusCreated)
			response := request(t, handler, http.MethodPost, "/api/budgets", tc.body, http.StatusBadRequest)
			assertJSONError(t, response, "")
			response = request(t, handler, http.MethodGet, "/api/budgets", "", http.StatusOK)
			if got, want := decodeResponse[[]budgetJSON](t, response), []budgetJSON{{Category: "еда", Limit: 10}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("rejected budget changed stored budgets: got %+v, want %+v", got, want)
			}
			request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":6}`, http.StatusCreated)
			response = request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":1}`, http.StatusConflict)
			assertJSONError(t, response, "budget exceeded")
			response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
			want := []transactionJSON{{ID: 1, Category: "еда", Amount: 4}, {ID: 2, Category: "еда", Amount: 6}}
			if got := decodeResponse[[]transactionJSON](t, response); !reflect.DeepEqual(got, want) {
				t.Errorf("rejected budget changed history: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestRoutingErrorsUseJSON(t *testing.T) {
	handler := newTestHandler()
	for _, path := range []string{"/", "/api", "/api/unknown", "/api/transactions/", "/api/budgets/extra"} {
		t.Run(path, func(t *testing.T) {
			assertJSONError(t, request(t, handler, http.MethodGet, path, "", http.StatusNotFound), "")
		})
	}
	for _, path := range []string{"/api/transactions", "/api/budgets"} {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			t.Run(method+" "+path, func(t *testing.T) {
				response := request(t, handler, method, path, "", http.StatusMethodNotAllowed)
				assertJSONError(t, response, "")
				allow := strings.ReplaceAll(response.Header().Get("Allow"), " ", "")
				if allow != "GET,POST" && allow != "POST,GET" {
					t.Errorf("Allow = %q, want GET and POST", response.Header().Get("Allow"))
				}
			})
		}
	}
}

func TestUnexpectedBodyReadFailureReturns500WithoutChangingState(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "transaction before JSON", path: "/api/transactions"},
		{name: "transaction after JSON", path: "/api/transactions", body: `{"category":"еда","amount":1}`},
		{name: "budget before JSON", path: "/api/budgets"},
		{name: "budget after JSON", path: "/api/budgets", body: `{"category":"еда","limit":100}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHandler()
			request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":10}`, http.StatusCreated)
			request(t, handler, http.MethodPost, "/api/transactions", `{"category":"еда","amount":4}`, http.StatusCreated)
			response := httptest.NewRecorder()
			reader := io.MultiReader(strings.NewReader(tc.body), brokenBodyReader{})
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, tc.path, reader))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", response.Code, response.Body.String())
			}
			assertContentType(t, response)
			assertJSONError(t, response, "internal server error")
			response = request(t, handler, http.MethodGet, "/api/budgets", "", http.StatusOK)
			if got, want := decodeResponse[[]budgetJSON](t, response), []budgetJSON{{Category: "еда", Limit: 10}}; !reflect.DeepEqual(got, want) {
				t.Errorf("body read failure changed budgets: got %+v, want %+v", got, want)
			}
			response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
			if got, want := decodeResponse[[]transactionJSON](t, response), []transactionJSON{{ID: 1, Category: "еда", Amount: 4}}; !reflect.DeepEqual(got, want) {
				t.Errorf("body read failure changed history: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestConcurrentRequestsKeepBudgetAndAssignedIDsConsistent(t *testing.T) {
	handler := newTestHandler()
	request(t, handler, http.MethodPost, "/api/budgets", `{"category":"еда","limit":20}`, http.StatusCreated)
	type result struct {
		method   string
		path     string
		response *httptest.ResponseRecorder
	}
	results := make(chan result, 80)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 80; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			method, path, body := http.MethodPost, "/api/transactions", `{"category":"еда","amount":1}`
			if i >= 40 {
				path = "/api/budgets"
				body = `{"category":"еда","limit":20}`
				if i%2 == 0 {
					method, body = http.MethodGet, ""
				}
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
			results <- result{method: method, path: path, response: response}
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	created, conflicts := 0, 0
	ids := make(map[int]bool)
	for result := range results {
		response := result.response
		assertContentType(t, response)
		if result.path == "/api/budgets" {
			wantStatus := http.StatusCreated
			if result.method == http.MethodGet {
				wantStatus = http.StatusOK
			}
			if response.Code != wantStatus {
				t.Errorf("concurrent %s %s status = %d, want %d: %s", result.method, result.path, response.Code, wantStatus, response.Body.String())
			}
			continue
		}
		switch response.Code {
		case http.StatusCreated:
			created++
			tx := decodeResponse[transactionJSON](t, response)
			if ids[tx.ID] {
				t.Errorf("two successful requests returned the same ID %d", tx.ID)
			}
			ids[tx.ID] = true
			if tx.Amount != 1 || tx.Category != "еда" {
				t.Errorf("created transaction = %+v, want amount 1 and category еда", tx)
			}
		case http.StatusConflict:
			conflicts++
			assertJSONError(t, response, "budget exceeded")
		default:
			t.Errorf("concurrent transaction status = %d, want 201 or 409: %s", response.Code, response.Body.String())
		}
	}
	if created != 20 || conflicts != 20 {
		t.Errorf("created = %d, conflicts = %d, want 20 each", created, conflicts)
	}
	response := request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	history := decodeResponse[[]transactionJSON](t, response)
	if len(history) != 20 {
		t.Fatalf("history length = %d, want 20", len(history))
	}
	for i, tx := range history {
		if tx.ID != i+1 || !ids[tx.ID] || tx.Amount != 1 || tx.Category != "еда" {
			t.Errorf("history[%d] = %+v; want sequential ID, matching creation response, amount 1, category еда", i, tx)
		}
	}
}

func TestEveryRequestLogsMethodPathAndDuration(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	handler := newTestHandler()
	durationPattern := regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?(?:ns|µs|us|ms|s|m|h)\b`)
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodGet, path: "/api/transactions", status: http.StatusOK},
		{method: http.MethodGet, path: "/api/budgets", status: http.StatusOK},
		{method: http.MethodPost, path: "/api/transactions", status: http.StatusBadRequest},
		{method: http.MethodDelete, path: "/api/budgets", status: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/unknown", status: http.StatusNotFound},
	} {
		output.Reset()
		request(t, handler, tc.method, tc.path, "", tc.status)
		line := output.String()
		if !strings.Contains(line, tc.method) || !strings.Contains(line, tc.path) {
			t.Errorf("request log = %q, want method %s and path %s", line, tc.method, tc.path)
		}
		if duration, err := time.ParseDuration(durationPattern.FindString(line)); err != nil || duration < 0 {
			t.Errorf("request log = %q, want nonnegative duration", line)
		}
	}
}

func TestResponseWriteFailureIsLoggedWithoutRetry(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	response := &brokenResponseWriter{header: make(http.Header)}
	newTestHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/transactions", nil))
	if response.writes != 1 || response.headers != 1 {
		t.Errorf("body writes = %d, header writes = %d; want one each", response.writes, response.headers)
	}
	if got := output.String(); !strings.Contains(got, "connection lost") {
		t.Errorf("log = %q, want the response write failure", got)
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s status = %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	assertContentType(t, response)
	return response
}

func assertContentType(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
}

func decodeResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("response contains trailing data: %q, error %v", response.Body.String(), err)
	}
	return value
}

func assertTransactionResponse(t *testing.T, response *httptest.ResponseRecorder, want transactionJSON) {
	t.Helper()
	if got := decodeResponse[transactionJSON](t, response); got != want {
		t.Errorf("created transaction = %+v, want %+v", got, want)
	}
	fields := decodeResponse[map[string]json.RawMessage](t, response)
	if len(fields) != 5 {
		t.Errorf("transaction JSON fields = %v, want five public fields", fields)
	}
	for _, key := range []string{"id", "amount", "category", "description", "date"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("transaction JSON is missing lowercase field %q", key)
		}
	}
}

func assertJSONError(t *testing.T, response *httptest.ResponseRecorder, message string) {
	t.Helper()
	value := decodeResponse[map[string]string](t, response)
	if len(value) != 1 || value["error"] == "" {
		t.Fatalf("error JSON = %v, want one nonempty error field", value)
	}
	if message != "" && value["error"] != message {
		t.Errorf("error = %q, want %q", value["error"], message)
	}
}

type brokenResponseWriter struct {
	header  http.Header
	writes  int
	headers int
}

func (w *brokenResponseWriter) Header() http.Header { return w.header }

func (w *brokenResponseWriter) WriteHeader(int) { w.headers++ }

func (w *brokenResponseWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("connection lost")
}

type brokenBodyReader struct{}

func (brokenBodyReader) Read([]byte) (int, error) {
	return 0, errors.New("request source unavailable")
}
