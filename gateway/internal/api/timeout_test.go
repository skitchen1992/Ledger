package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

func reportingHandler(repository memoryRepository) http.Handler {
	return NewHandler(service.NewReporting(service.New(repository, repository, repository), repository, nil))
}

type waitingRepository struct {
	memoryRepository
	started chan struct{}
	active  atomic.Int32
}

func (r *waitingRepository) wait(ctx context.Context) error {
	r.active.Add(1)
	defer r.active.Add(-1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return fmt.Errorf("query canceled: %w", ctx.Err())
}

func (r *waitingRepository) ListBudgets(ctx context.Context) ([]domain.Budget, error) {
	return nil, r.wait(ctx)
}

func (r *waitingRepository) ListTransactions(ctx context.Context) ([]domain.Transaction, error) {
	return nil, r.wait(ctx)
}

func (r *waitingRepository) WithinCategory(ctx context.Context, _ string, _ func(domain.BudgetRepository, domain.ExpenseRepository) error) error {
	return r.wait(ctx)
}

func (r *waitingRepository) SumReportCategory(ctx context.Context, _, _, _ string) (*big.Rat, error) {
	return nil, r.wait(ctx)
}

func TestAllAPIOperationsStopAtRequestTimeout(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/budgets", ""},
		{http.MethodGet, "/api/transactions", ""},
		{http.MethodPost, "/api/budgets", `{"category":"еда","limit":100}`},
		{http.MethodPost, "/api/transactions", `{"category":"еда","amount":1}`},
		{http.MethodGet, "/api/reports/summary?from=2026-11-01&to=2026-11-10", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			repository := &waitingRepository{memoryRepository: newMemoryRepository(), started: make(chan struct{}, 2)}
			healthy := reportingHandler(repository.memoryRepository)
			request(t, healthy, http.MethodPost, "/api/transactions", `{"category":"еда","amount":1,"date":"2026-11-01"}`, http.StatusCreated)
			request(t, healthy, http.MethodPost, "/api/transactions", `{"category":"книги","amount":2,"date":"2026-11-10"}`, http.StatusCreated)
			application := service.NewReporting(service.New(repository, repository, repository), repository, nil)
			handler, err := NewHandlerWithTimeout(application, 20*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			response := request(t, handler, tc.method, tc.path, tc.body, http.StatusGatewayTimeout)
			assertJSONError(t, response, timeoutMessage)
			if active := repository.active.Load(); active != 0 {
				t.Errorf("%d queries still running after response", active)
			}
			response = request(t, healthy, http.MethodGet, "/api/transactions", "", http.StatusOK)
			if got := decodeResponse[[]transactionJSON](t, response); len(got) != 2 {
				t.Errorf("timed out operation changed history: %+v", got)
			}
		})
	}
}

func TestRequestUsesEarlierParentDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			repository := &waitingRepository{memoryRepository: newMemoryRepository(), started: make(chan struct{}, 1)}
			application := service.NewReporting(service.New(repository, repository, repository), repository, nil)
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			response := httptest.NewRecorder()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				NewHandler(application).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/transactions", nil).WithContext(ctx))
			}()
			select {
			case <-repository.started:
			case <-time.After(time.Second):
				t.Fatal("query did not start")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("request ignored the parent context")
			}
			if response.Code != http.StatusGatewayTimeout {
				t.Errorf("status = %d, want 504: %s", response.Code, response.Body.String())
			}
			assertJSONError(t, response, timeoutMessage)
			if active := repository.active.Load(); active != 0 {
				t.Errorf("%d queries still running after cancellation", active)
			}
		})
	}
}

func TestMiddlewareDiscardsPartialResponseAndWaitsForHandler(t *testing.T) {
	finished := false
	handler := timeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Partial", "must disappear")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"partial":true}`))
		<-r.Context().Done()
		finished = true
	}), time.Millisecond)
	response := request(t, handler, http.MethodGet, "/api/test", "", http.StatusGatewayTimeout)
	assertJSONError(t, response, timeoutMessage)
	if !finished {
		t.Error("handler remains active after the timeout response")
	}
	if got := response.Header().Get("X-Partial"); got != "" {
		t.Errorf("partial header survived timeout: %q", got)
	}
}

func TestSlowRequestBodyTimesOutWithoutMutatingLedger(t *testing.T) {
	repository := newMemoryRepository()
	application := service.NewReporting(service.New(repository, repository, repository), repository, nil)
	handler, err := NewHandlerWithTimeout(application, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(connection, "POST /api/transactions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"category\":\"еда\",")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusGatewayTimeout || string(body) != `{"error":"request timed out or canceled"}`+"\n" {
		t.Errorf("slow body response = %d %s, want 504 timeout JSON", response.StatusCode, body)
	}
	history := request(t, reportingHandler(repository), http.MethodGet, "/api/transactions", "", http.StatusOK)
	if got := decodeResponse[[]transactionJSON](t, history); len(got) != 0 {
		t.Errorf("slow invalid body changed history: %+v", got)
	}
}

func TestTimeoutConstructorRejectsNonPositiveDurations(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		if handler, err := NewHandlerWithTimeout(nil, timeout); err == nil || handler != nil {
			t.Errorf("timeout %s created handler=%v, err=%v", timeout, handler, err)
		}
	}
}

func TestSuccessfulRequestDeadlineDoesNotAffectReusedConnection(t *testing.T) {
	const timeout = 100 * time.Millisecond
	handler, err := NewHandlerWithTimeout(nil, timeout)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport := &http.Transport{MaxConnsPerHost: 1}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for attempt := 0; attempt < 2; attempt++ {
		reused := false
		request, err := http.NewRequest(http.MethodGet, server.URL+"/ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "\"pong\"\n" {
			t.Fatalf("request %d: status=%d body=%q err=%v", attempt, response.StatusCode, body, readErr)
		}
		if attempt == 1 && !reused {
			t.Fatal("second request did not exercise the existing connection")
		}
		if attempt == 0 {
			<-time.After(timeout + 50*time.Millisecond)
		}
	}
}

func TestServiceCancellationErrorsUseTimeoutResponse(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			repository := newMemoryRepository()
			repository.state.err = fmt.Errorf("repository: %w", cause)
			response := request(t, reportingHandler(repository), http.MethodGet, "/api/budgets", "", http.StatusGatewayTimeout)
			assertJSONError(t, response, timeoutMessage)
		})
	}
}
