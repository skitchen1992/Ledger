package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skitchen1992/Ledger/ledger"
	"github.com/skitchen1992/Ledger/ledger/internal/store"
)

func TestRoutingUsesJSONAndMethodAllow(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
		allow, body  string
	}{
		{http.MethodGet, "/ping", http.StatusOK, "", `"pong"`},
		{http.MethodGet, "/missing", http.StatusNotFound, "", `{"error":"not found"}`},
		{http.MethodPost, "/ping", http.StatusMethodNotAllowed, "GET", `{"error":"method not allowed"}`},
		{http.MethodDelete, "/api/budgets", http.StatusMethodNotAllowed, "GET, POST", `{"error":"method not allowed"}`},
		{http.MethodPut, "/api/transactions", http.StatusMethodNotAllowed, "GET, POST", `{"error":"method not allowed"}`},
		{http.MethodPost, "/api/reports/summary", http.StatusMethodNotAllowed, "GET", `{"error":"method not allowed"}`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewHandler(nil).ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || strings.TrimSpace(response.Body.String()) != tc.body {
				t.Fatalf("response = %d %s, want %d %s", response.Code, response.Body, tc.status, tc.body)
			}
			if got := response.Header().Get("Allow"); got != tc.allow {
				t.Errorf("Allow = %q, want %q", got, tc.allow)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
		})
	}
}

func TestInvalidRequestsAreRejectedBeforeStorage(t *testing.T) {
	for _, path := range []string{"/api/budgets", "/api/transactions"} {
		for _, body := range []string{
			"", "null", "[]", "{", "{}", `{"category":" "}`, `{"amount":"bad","limit":"bad"}`,
			`{"category":"food","amount":1,"limit":1} {}`,
			`{"category":"food","amount":1,"limit":1} trailing`,
		} {
			t.Run(path+" "+body, func(t *testing.T) {
				response := httptest.NewRecorder()
				// A nil store makes an accidental persistence call fail this test.
				NewHandler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
				if response.Code != http.StatusBadRequest {
					t.Fatalf("response = %d %s, want 400", response.Code, response.Body)
				}
				var payload struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.Error == "" {
					t.Fatalf("invalid JSON error response: %s (%v)", response.Body, err)
				}
			})
		}
	}
}

func TestPersistenceValidationReturnsBadRequest(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/transactions", `{"category":"food","amount":1,"date":"2026-02-30"}`},
		{http.MethodPost, "/api/transactions", `{"category":"food","amount":1}`},
		{http.MethodPost, "/api/transactions", `{"category":"food","amount":0.001,"date":"2026-11-06"}`},
		{http.MethodPost, "/api/budgets", `{"category":"food","limit":1000000000000}`},
		{http.MethodGet, "/api/reports/summary", ""},
		{http.MethodGet, "/api/reports/summary?from=2026-11-07&to=2026-11-06", ""},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			response := httptest.NewRecorder()
			// Real Store validation must reject input before using its nil DB.
			NewHandler(store.New(nil, nil)).ServeHTTP(response,
				httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s, want 400", response.Code, response.Body)
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestBodyReadFailureIsLoggedWithoutLeakingCause(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	const cause = "private source unavailable"
	body := io.MultiReader(strings.NewReader(`{"category":"food","limit":10}`), failingReader{errors.New(cause)})
	response := httptest.NewRecorder()
	NewHandler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/budgets", body))
	if response.Code != http.StatusInternalServerError || strings.TrimSpace(response.Body.String()) != `{"error":"internal server error"}` {
		t.Fatalf("response = %d %s, want generic 500", response.Code, response.Body)
	}
	for _, want := range []string{cause, "method=POST", "path=/api/budgets", "duration="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q does not contain %q", logs.String(), want)
		}
	}
}

func TestStoreErrorsMapToHTTPStatuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"budget exceeded", fmt.Errorf("food: %w", ledger.ErrBudgetExceeded), http.StatusConflict, `{"error":"budget exceeded"}`},
		{"invalid input", fmt.Errorf("%w: date must be YYYY-MM-DD", store.ErrInvalidInput), http.StatusBadRequest, ""},
		{"database failure", errors.New("database connection password=private"), http.StatusInternalServerError, `{"error":"internal server error"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeStoreError(response, httptest.NewRequest(http.MethodPost, "/api/transactions", nil), tc.err)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.body != "" && strings.TrimSpace(response.Body.String()) != tc.body {
				t.Fatalf("response = %s, want %s", response.Body, tc.body)
			}
		})
	}
}
