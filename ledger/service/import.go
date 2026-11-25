package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	// DefaultImportWorkers is used by callers that do not choose a pool size.
	DefaultImportWorkers = 4
	// MaxImportWorkers bounds the goroutines started by one bulk import.
	MaxImportWorkers = 32
)

// ImportError links a rejected input element to the CreateTransaction error.
type ImportError struct {
	Index int
	Err   error
}

// ImportSummary describes processed elements of a bulk import. Errors are
// ordered by input index. After cancellation the summary contains only the
// elements whose outcome is known; interrupted elements are rolled back.
type ImportSummary struct {
	Accepted int64
	Rejected int64
	Errors   []ImportError
}

type importJob struct {
	index       int
	transaction Transaction
}

type importResult struct {
	index int
	err   error
}

// ImportTransactions creates transactions concurrently with a pool of workers.
// Each element goes through CreateTransaction: validation, the category budget
// check and the insert share one unit of work, so a rejected element leaves no
// row and concurrent elements of one category cannot exceed its budget.
// Processing order between workers is not defined; workers=1 keeps input order.
// If ctx is canceled before every element is processed, the partial summary is
// returned together with an error wrapping ctx.Err().
func (s *Ledger) ImportTransactions(ctx context.Context, transactions []Transaction, workers int) (ImportSummary, error) {
	if workers < 1 || workers > MaxImportWorkers {
		return ImportSummary{}, fmt.Errorf("%w: workers must be between 1 and %d", ErrInvalidInput, MaxImportWorkers)
	}
	if workers > len(transactions) {
		workers = len(transactions)
	}

	jobs := make(chan importJob)
	results := make(chan importResult)

	// The collector is the only reader of results and the only owner of the
	// errors slice. Counters are changed and read only through sync/atomic.
	var accepted, rejected int64
	var importErrors []ImportError
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for result := range results {
			if result.err == nil {
				atomic.AddInt64(&accepted, 1)
				continue
			}
			atomic.AddInt64(&rejected, 1)
			importErrors = append(importErrors, ImportError{Index: result.index, Err: result.err})
		}
		sort.Slice(importErrors, func(i, j int) bool { return importErrors[i].Index < importErrors[j].Index })
	}()

	var pool sync.WaitGroup
	for range workers {
		pool.Add(1)
		go func() {
			defer pool.Done()
			s.importWorker(ctx, jobs, results)
		}()
	}

	sendImportJobs(ctx, jobs, transactions)
	close(jobs)
	pool.Wait()
	close(results)
	<-collected

	summary := ImportSummary{
		Accepted: atomic.LoadInt64(&accepted),
		Rejected: atomic.LoadInt64(&rejected),
		Errors:   importErrors,
	}
	if processed := summary.Accepted + summary.Rejected; processed < int64(len(transactions)) {
		return summary, fmt.Errorf("bulk import interrupted after %d of %d transactions: %w",
			processed, len(transactions), ctx.Err())
	}
	return summary, nil
}

// sendImportJobs stops sending at cancellation; the caller closes jobs.
func sendImportJobs(ctx context.Context, jobs chan<- importJob, transactions []Transaction) {
	for index, transaction := range transactions {
		select {
		case <-ctx.Done():
			return
		case jobs <- importJob{index: index, transaction: transaction}:
		}
	}
}

// importWorker reports every element with a known outcome. The send to results
// is unconditional because the collector drains results until it is closed:
// an element saved just before cancellation must still be counted.
func (s *Ledger) importWorker(ctx context.Context, jobs <-chan importJob, results chan<- importResult) {
	for job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, err := s.CreateTransaction(ctx, job.transaction)
		if err != nil && importInterrupted(ctx, err) {
			return
		}
		results <- importResult{index: job.index, err: err}
	}
}

// importInterrupted reports whether a failure was caused by cancellation rather
// than by the element itself. A canceled SQL transaction may fail with a
// driver error (for example sql.ErrTxDone) instead of ctx.Err(), so only
// business rejections remain definitive after the context is done.
func importInterrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrBudgetExceeded)
}
