package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	key := "report:summary:" + from + ":" + to
	if s.cache != nil {
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		data, cacheErr := s.cache.Get(cacheCtx, key)
		cacheCancel()
		if cacheErr == nil {
			var cached ReportSummary
			if json.Unmarshal(data, &cached) == nil && cached.From == from && cached.To == to && cached.Categories != nil {
				log.Printf("report cache hit key=%s", key)
				return cached, nil
			}
			log.Printf("report cache invalid JSON/value key=%s; reading PostgreSQL", key)
		} else if !errors.Is(cacheErr, domain.ErrCacheMiss) {
			log.Printf("report cache read failed key=%s: %v", key, cacheErr)
		}
	}
	log.Printf("report cache miss key=%s", key)
	report, err := s.reports.GetReportSummary(ctx, from, to)
	if err != nil {
		return ReportSummary{}, err
	}
	if s.cache != nil {
		data, err := json.Marshal(report)
		if err != nil {
			return ReportSummary{}, fmt.Errorf("encode report summary: %w", err)
		}
		cacheCtx, cacheCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cacheCancel()
		if err := s.cache.Set(cacheCtx, key, data, reportTTL); err != nil {
			log.Printf("report cache write failed key=%s: %v", key, err)
		}
	}
	return report, nil
}
