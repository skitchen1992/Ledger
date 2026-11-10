package api

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestReportSummaryIncludesBothDatesAndPreservesGatewayHistory(t *testing.T) {
	handler := newTestHandler()
	for _, body := range []string{
		`{"category":"еда","amount":0.1,"date":"2026-11-01"}`,
		`{"category":"еда","amount":0.2,"date":"2026-11-10"}`,
		`{"category":"транспорт","amount":789,"date":"2026-11-05"}`,
		`{"category":"еда","amount":100,"date":"2026-10-31"}`,
		`{"category":"еда","amount":200,"date":"2026-11-11"}`,
		`{"category":"еда","amount":300}`,
		`{"category":"еда","amount":400,"date":"любая дата"}`,
		`{"category":"еда","amount":500,"date":"2026-11-00"}`,
	} {
		request(t, handler, http.MethodPost, "/api/transactions", body, http.StatusCreated)
	}
	response := request(t, handler, http.MethodGet, "/api/reports/summary?from=2026-11-01&to=2026-11-10", "", http.StatusOK)
	if got, want := decodeResponse[map[string]float64](t, response), (map[string]float64{"еда": 0.3, "транспорт": 789}); !reflect.DeepEqual(got, want) {
		t.Errorf("summary = %v, want %v", got, want)
	}
	response = request(t, handler, http.MethodGet, "/api/reports/summary?from=2026-11-10&to=2026-11-10", "", http.StatusOK)
	if got, want := decodeResponse[map[string]float64](t, response), (map[string]float64{"еда": 0.2}); !reflect.DeepEqual(got, want) {
		t.Errorf("single day summary = %v, want %v", got, want)
	}
	response = request(t, handler, http.MethodGet, "/api/transactions", "", http.StatusOK)
	if history := decodeResponse[[]transactionJSON](t, response); len(history) != 8 {
		t.Fatalf("report changed history: got %d transactions, want 8", len(history))
	}
}

func TestReportSummaryEmptyAndRepositoryFailure(t *testing.T) {
	repository := newMemoryRepository()
	handler := reportingHandler(repository)
	response := request(t, handler, http.MethodGet, "/api/reports/summary?from=2026-11-01&to=2026-11-10", "", http.StatusOK)
	if got := response.Body.String(); got != "{}\n" {
		t.Fatalf("empty summary = %q, want {}", got)
	}
	repository.state.err = errors.New("SQL failed")
	response = request(t, handler, http.MethodGet, "/api/reports/summary?from=2026-11-01&to=2026-11-10", "", http.StatusInternalServerError)
	assertJSONError(t, response, "internal server error")
}

func TestReportSummaryValidatesDatesAndMethods(t *testing.T) {
	handler := newTestHandler()
	for _, tc := range []struct {
		name, query, wantError string
	}{
		{name: "missing both", wantError: "from must be a valid YYYY-MM-DD date"},
		{name: "missing from", query: "to=2026-11-10", wantError: "from must be a valid YYYY-MM-DD date"},
		{name: "missing to", query: "from=2026-11-01", wantError: "to must be a valid YYYY-MM-DD date"},
		{name: "impossible from", query: "from=2026-02-30&to=2026-11-10", wantError: "from must be a valid YYYY-MM-DD date"},
		{name: "invalid to", query: "from=2026-11-01&to=tomorrow", wantError: "to must be a valid YYYY-MM-DD date"},
		{name: "year zero", query: "from=0000-01-01&to=2026-11-10", wantError: "from must be a valid YYYY-MM-DD date"},
		{name: "reverse interval", query: "from=2026-11-10&to=2026-11-01", wantError: "invalid input: from must not be after to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, "/api/reports/summary?"+tc.query, "", http.StatusBadRequest)
			assertJSONError(t, response, tc.wantError)
		})
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			response := request(t, handler, method, "/api/reports/summary", "", http.StatusMethodNotAllowed)
			assertJSONError(t, response, "method not allowed")
			if got := response.Header().Get("Allow"); got != http.MethodGet {
				t.Errorf("Allow = %q, want GET", got)
			}
		})
	}
}
