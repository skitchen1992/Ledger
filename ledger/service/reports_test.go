package service_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
	"github.com/skitchen1992/Ledger/ledger/service"
)

type reportRepositoryStub struct {
	list func(context.Context, string, string) ([]string, error)
	sum  func(context.Context, string, string, string) (*big.Rat, error)
}

func (r reportRepositoryStub) ListReportCategories(ctx context.Context, from, to string) ([]string, error) {
	return r.list(ctx, from, to)
}

func (r reportRepositoryStub) SumReportCategory(ctx context.Context, category, from, to string) (*big.Rat, error) {
	return r.sum(ctx, category, from, to)
}

type reportCacheStub struct {
	get func(context.Context, string) ([]byte, error)
	set func(context.Context, string, []byte, time.Duration) error
}

func (c reportCacheStub) Get(ctx context.Context, key string) ([]byte, error) {
	if c.get == nil {
		return nil, domain.ErrCacheMiss
	}
	return c.get(ctx, key)
}

func (c reportCacheStub) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if c.set == nil {
		return nil
	}
	return c.set(ctx, key, data, ttl)
}

type reportResult struct {
	report service.ReportSummary
	err    error
}

func TestReportCalculatesEveryCategoryConcurrentlyAndAddsExactDecimals(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := reportRepositoryStub{
		list: func(context.Context, string, string) ([]string, error) {
			return []string{"travel", "food", "books"}, nil
		},
		sum: func(ctx context.Context, category, _, _ string) (*big.Rat, error) {
			started <- category
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]*big.Rat{"books": big.NewRat(1, 10), "food": big.NewRat(2, 10), "travel": big.NewRat(3, 10)}[category], nil
		},
	}
	s := service.NewReporting(service.New(nil, nil, nil), repo, nil)
	result := make(chan reportResult, 1)
	go func() {
		report, err := s.GetReportSummary(ctx, "2026-01-01", "2026-01-31")
		result <- reportResult{report, err}
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("all category queries must start before any category query finishes")
		}
	}
	close(release)
	want := service.ReportSummary{From: "2026-01-01", To: "2026-01-31", Total: 0.6,
		Categories: []domain.CategoryTotal{{Category: "books", Amount: 0.1}, {Category: "food", Amount: 0.2}, {Category: "travel", Amount: 0.3}}}
	select {
	case got := <-result:
		if got.err != nil || !reflect.DeepEqual(got.report, want) {
			t.Fatalf("report must be sorted with an exact decimal grand total: got=%#v, err=%v", got.report, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("report did not finish after its category queries completed")
	}
}

// The logger is synchronized because the report's heartbeat writes concurrently.
type reportLog struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	running chan struct{}
}

func (l *reportLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	n, err := l.buffer.Write(data)
	l.mu.Unlock()
	if bytes.Contains(data, []byte("report calculation running")) {
		select {
		case l.running <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (l *reportLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.String()
}

func TestReportCancellationStopsWorkersAndHeartbeatBeforeReturning(t *testing.T) {
	output := &reportLog{running: make(chan struct{}, 1)}
	previousOutput := log.Writer()
	log.SetOutput(output)
	defer log.SetOutput(previousOutput)
	for _, mode := range []string{"client cancellation", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{}, 2)
			var active atomic.Int32
			repo := reportRepositoryStub{
				list: func(context.Context, string, string) ([]string, error) { return []string{"food", "travel"}, nil },
				sum: func(ctx context.Context, _, _, _ string) (*big.Rat, error) {
					active.Add(1)
					defer active.Add(-1)
					started <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			wantError := context.Canceled
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 650*time.Millisecond)
				wantError = context.DeadlineExceeded
			}
			defer cancel()
			s := service.NewReporting(service.New(nil, nil, nil), repo, nil)
			result := make(chan reportResult, 1)
			go func() {
				report, err := s.GetReportSummary(ctx, "2026-01-01", "2026-01-31")
				result <- reportResult{report, err}
			}()
			for range 2 {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("category query did not start")
				}
			}
			select {
			case <-output.running:
			case <-time.After(time.Second):
				t.Fatal("heartbeat did not report the running calculation")
			}
			if mode == "client cancellation" {
				cancel()
			}
			select {
			case got := <-result:
				if !errors.Is(got.err, wantError) || !reflect.DeepEqual(got.report, service.ReportSummary{}) {
					t.Fatalf("canceled report must return its cause and no partial result: got=%#v, err=%v", got.report, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("report did not return promptly after cancellation")
			}
			if remaining := active.Load(); remaining != 0 {
				t.Fatalf("report returned with %d query workers still active", remaining)
			}
			finishedLogs := output.String()
			if !strings.Contains(finishedLogs, "heartbeat stopped") || !strings.Contains(finishedLogs, "ctx.Done()") {
				t.Fatalf("report must log ticker cleanup and context cancellation before returning: %s", finishedLogs)
			}
			<-time.After(450 * time.Millisecond)
			if got := output.String(); got != finishedLogs {
				t.Fatalf("heartbeat continued logging after the report returned: %s", got)
			}
		})
	}
}

func TestReportQueryFailureCancelsPeersAndDiscardsCompletedCategories(t *testing.T) {
	failure := errors.New("database sum failed")
	started := make(chan struct{}, 3)
	fail := make(chan struct{})
	var active atomic.Int32
	repo := reportRepositoryStub{
		list: func(context.Context, string, string) ([]string, error) {
			return []string{"broken", "food", "travel"}, nil
		},
		sum: func(ctx context.Context, category, _, _ string) (*big.Rat, error) {
			active.Add(1)
			defer active.Add(-1)
			started <- struct{}{}
			if category == "food" {
				return big.NewRat(7, 1), nil
			}
			if category == "broken" {
				select {
				case <-fail:
					return nil, failure
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	cache := reportCacheStub{set: func(context.Context, string, []byte, time.Duration) error {
		t.Error("an incomplete report must not be cached")
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := service.NewReporting(service.New(nil, nil, nil), repo, cache)
	result := make(chan reportResult, 1)
	go func() {
		report, err := s.GetReportSummary(ctx, "2026-01-01", "2026-01-31")
		result <- reportResult{report, err}
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("category query did not start")
		}
	}
	close(fail)
	select {
	case got := <-result:
		if !errors.Is(got.err, failure) || !reflect.DeepEqual(got.report, service.ReportSummary{}) {
			t.Fatalf("failure must preserve the SQL cause without partial categories: got=%#v, err=%v", got.report, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed query did not cancel the other category query")
	}
	if remaining := active.Load(); remaining != 0 {
		t.Fatalf("report returned with %d query workers still active", remaining)
	}
}

func TestReportValidatesIntervalBeforeAccessingStorage(t *testing.T) {
	s := service.NewReporting(service.New(nil, nil, nil), reportRepositoryStub{}, reportCacheStub{
		get: func(context.Context, string) ([]byte, error) {
			t.Fatal("invalid interval reached the cache")
			return nil, nil
		},
	})
	for _, dates := range [][2]string{{"", "2026-01-31"}, {"2026-02-30", "2026-03-01"}, {"2026-01-01", "2026-1-2"}, {"2026-01-31", "2026-01-01"}} {
		t.Run(dates[0]+" to "+dates[1], func(t *testing.T) {
			if report, err := s.GetReportSummary(context.Background(), dates[0], dates[1]); !errors.Is(err, service.ErrInvalidInput) || !reflect.DeepEqual(report, service.ReportSummary{}) {
				t.Fatalf("invalid report interval: got=%#v, err=%v", report, err)
			}
		})
	}
}

func TestReportEmptyIntervalReturnsEmptyCategoriesAndStopsHeartbeat(t *testing.T) {
	output := &reportLog{running: make(chan struct{}, 1)}
	previousOutput := log.Writer()
	log.SetOutput(output)
	defer log.SetOutput(previousOutput)
	repo := reportRepositoryStub{list: func(context.Context, string, string) ([]string, error) { return nil, nil }}
	s := service.NewReporting(service.New(nil, nil, nil), repo, nil)
	got, err := s.GetReportSummary(context.Background(), "2026-01-01", "2026-01-01")
	want := service.ReportSummary{From: "2026-01-01", To: "2026-01-01", Categories: []domain.CategoryTotal{}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("empty interval must return a non-nil empty category list: got=%#v, err=%v", got, err)
	}
	if !strings.Contains(output.String(), "heartbeat stopped; calculation completed") {
		t.Fatalf("successful report must join its heartbeat before returning: %s", output.String())
	}
}

func TestReportCacheNeverTurnsRequestCancellationIntoSuccess(t *testing.T) {
	for _, stage := range []string{"before cache hit", "during cache hit", "during cache write"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cache := reportCacheStub{
				get: func(context.Context, string) ([]byte, error) {
					if stage == "during cache write" {
						return nil, domain.ErrCacheMiss
					}
					cancel()
					return []byte(`{"from":"2026-01-01","to":"2026-01-01","total":999,"categories":[]}`), nil
				},
				set: func(context.Context, string, []byte, time.Duration) error { cancel(); return context.Canceled },
			}
			if stage == "before cache hit" {
				cancel()
			}
			repo := reportRepositoryStub{list: func(context.Context, string, string) ([]string, error) { return nil, nil }}
			s := service.NewReporting(service.New(nil, nil, nil), repo, cache)
			got, err := s.GetReportSummary(ctx, "2026-01-01", "2026-01-01")
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, service.ReportSummary{}) {
				t.Fatalf("request cancellation during %s was swallowed: got=%#v, err=%v", stage, got, err)
			}
		})
	}
}

func TestReportCacheHitKeepsSummaryAndInvalidValuesFallBackToCalculation(t *testing.T) {
	for _, initial := range []string{"missing", "unavailable", "invalid JSON", "wrong interval"} {
		t.Run(initial, func(t *testing.T) {
			var cached []byte
			amount := int64(2)
			repo := reportRepositoryStub{
				list: func(context.Context, string, string) ([]string, error) { return []string{"food"}, nil },
				sum:  func(context.Context, string, string, string) (*big.Rat, error) { return big.NewRat(amount, 10), nil },
			}
			cache := reportCacheStub{
				get: func(context.Context, string) ([]byte, error) {
					if cached != nil {
						return cached, nil
					}
					switch initial {
					case "unavailable":
						return nil, errors.New("cache unavailable")
					case "invalid JSON":
						return []byte("{"), nil
					case "wrong interval":
						return []byte(`{"from":"2000-01-01","to":"2000-01-01","total":999,"categories":[]}`), nil
					default:
						return nil, domain.ErrCacheMiss
					}
				},
				set: func(_ context.Context, key string, data []byte, ttl time.Duration) error {
					if key != "report:summary:2026-01-01:2026-01-31" || ttl != 30*time.Second {
						t.Errorf("wrong report cache location/lifetime: key=%q, ttl=%s", key, ttl)
					}
					cached = append([]byte(nil), data...)
					return nil
				},
			}
			s := service.NewReporting(service.New(nil, nil, nil), repo, cache)
			want := service.ReportSummary{From: "2026-01-01", To: "2026-01-31", Total: 0.2,
				Categories: []domain.CategoryTotal{{Category: "food", Amount: 0.2}}}
			for attempt := 0; attempt < 2; attempt++ {
				got, err := s.GetReportSummary(context.Background(), want.From, want.To)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("report must fall back to calculation then use its cached value: attempt=%d got=%#v, err=%v", attempt, got, err)
				}
				// A cached hit must keep the first summary even when storage changes.
				amount = 9
			}
		})
	}
}

func TestReportDoesNotReturnNonJSONMonetaryTotals(t *testing.T) {
	for _, scenario := range []string{"category overflow", "grand total overflow"} {
		t.Run(scenario, func(t *testing.T) {
			repo := reportRepositoryStub{
				list: func(context.Context, string, string) ([]string, error) { return []string{"food", "travel"}, nil },
				sum: func(context.Context, string, string, string) (*big.Rat, error) {
					amount := new(big.Rat).SetFloat64(math.MaxFloat64)
					if scenario == "category overflow" {
						amount.Mul(amount, big.NewRat(2, 1))
					}
					return amount, nil
				},
			}
			s := service.NewReporting(service.New(nil, nil, nil), repo, nil)
			got, err := s.GetReportSummary(context.Background(), "2026-01-01", "2026-01-31")
			if err == nil || !reflect.DeepEqual(got, service.ReportSummary{}) {
				t.Fatalf("overflow must be an error without an unencodable partial report: got=%#v, err=%v", got, err)
			}
		})
	}
}
