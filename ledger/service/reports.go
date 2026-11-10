package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/skitchen1992/Ledger/ledger/domain"
)

const reportTTL = 30 * time.Second

type reporting struct {
	Service
	reports domain.ReportRepository
	cache   domain.ReportCache
}

func NewReporting(base Service, reports domain.ReportRepository, cache domain.ReportCache) ReportingService {
	return &reporting{Service: base, reports: reports, cache: cache}
}

// GetReportSummary includes both dates and tolerates cache failures.
func (s *reporting) GetReportSummary(ctx context.Context, from, to string) (ReportSummary, error) {
	start, err := parseDate(from)
	if err != nil {
		return ReportSummary{}, err
	}
	end, err := parseDate(to)
	if err != nil {
		return ReportSummary{}, err
	}
	if start.After(end) {
		return ReportSummary{}, fmt.Errorf("%w: from must not be after to", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ReportSummary{}, err
	}
	key := "report:summary:" + from + ":" + to
	if s.cache != nil {
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		data, cacheErr := s.cache.Get(cacheCtx, key)
		cacheCancel()
		if err := ctx.Err(); err != nil {
			return ReportSummary{}, err
		}
		if cacheErr == nil {
			var cached ReportSummary
			if json.Unmarshal(data, &cached) == nil && cached.From == from && cached.To == to && cached.Categories != nil {
				if err := ctx.Err(); err != nil {
					return ReportSummary{}, err
				}
				log.Printf("report cache hit key=%s", key)
				return cached, nil
			}
			log.Printf("report cache invalid JSON/value key=%s; reading PostgreSQL", key)
		} else if !errors.Is(cacheErr, domain.ErrCacheMiss) {
			log.Printf("report cache read failed key=%s: %v", key, cacheErr)
		}
	}
	log.Printf("report cache miss key=%s", key)
	report, err := s.calculateReport(ctx, from, to)
	if err != nil {
		return ReportSummary{}, err
	}
	if s.cache != nil {
		data, err := json.Marshal(report)
		if err != nil {
			return ReportSummary{}, fmt.Errorf("encode report summary: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return ReportSummary{}, err
		}
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		if err := s.cache.Set(cacheCtx, key, data, reportTTL); err != nil {
			log.Printf("report cache write failed key=%s: %v", key, err)
		}
		cacheCancel()
	}
	if err := ctx.Err(); err != nil {
		return ReportSummary{}, err
	}
	return report, nil
}

func (s *reporting) calculateReport(ctx context.Context, from, to string) (ReportSummary, error) {
	workersCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go reportHeartbeat(workersCtx, done, heartbeatDone)
	defer func() {
		close(done)
		<-heartbeatDone
	}()

	if err := ctx.Err(); err != nil {
		return ReportSummary{}, err
	}
	categories, err := s.reports.ListReportCategories(workersCtx, from, to)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ReportSummary{}, ctxErr
	}
	if err != nil {
		cancel(err)
		return ReportSummary{}, fmt.Errorf("list report categories: %w", err)
	}
	sort.Strings(categories)
	amounts := make([]*big.Rat, len(categories))
	var workers sync.WaitGroup
	for index, category := range categories {
		workers.Add(1)
		go func(index int, category string) {
			defer workers.Done()
			if workersCtx.Err() != nil {
				return
			}
			amount, err := s.reports.SumReportCategory(workersCtx, category, from, to)
			if err != nil {
				cancel(fmt.Errorf("sum report category %q: %w", category, err))
				return
			}
			// Each worker owns its slot; only the caller merges after Wait.
			amounts[index] = amount
		}(index, category)
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return ReportSummary{}, err
	}
	if err := context.Cause(workersCtx); err != nil {
		return ReportSummary{}, err
	}
	report := ReportSummary{From: from, To: to, Categories: make([]domain.CategoryTotal, 0, len(categories))}
	total := new(big.Rat)
	for index, category := range categories {
		if err := ctx.Err(); err != nil {
			return ReportSummary{}, err
		}
		total.Add(total, amounts[index])
		amount, _ := amounts[index].Float64()
		if math.IsInf(amount, 0) {
			return ReportSummary{}, fmt.Errorf("report category %q total exceeds float64 range", category)
		}
		report.Categories = append(report.Categories, domain.CategoryTotal{Category: category, Amount: amount})
	}
	report.Total, _ = total.Float64()
	if math.IsInf(report.Total, 0) {
		return ReportSummary{}, errors.New("report total exceeds float64 range")
	}
	return report, nil
}

func reportHeartbeat(ctx context.Context, done <-chan struct{}, stopped chan<- struct{}) {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer func() {
		ticker.Stop()
		if ctx.Err() != nil {
			log.Printf("report heartbeat stopped; calculation interrupted by ctx.Done(): %v", context.Cause(ctx))
		} else {
			log.Printf("report heartbeat stopped; calculation completed")
		}
		close(stopped)
	}()
	for {
		select {
		case <-ticker.C:
			log.Printf("report calculation running")
		case <-ctx.Done():
			return
		case <-done:
			return
		}
	}
}
