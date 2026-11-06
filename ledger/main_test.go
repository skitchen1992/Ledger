package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunPrintsDemoTransactions(t *testing.T) {
	const want = "Ledger service started\n" +
		"1 | 1500.00 руб. | еда | Продукты | 2026-11-06\n" +
		"2 | 80.00 руб. | транспорт | Метро | 2026-11-06\n" +
		"3 | 950.00 руб. | книги | Учебник Go | 2026-11-06\n"
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want {
		t.Fatalf("run() output = %q, want %q", got, want)
	}
}

type failingOutput struct {
	failOn             string
	err                error
	failed             bool
	writesAfterFailure int
}

func (w *failingOutput) Write(p []byte) (int, error) {
	if w.failed {
		w.writesAfterFailure++
		return 0, w.err
	}
	if strings.Contains(string(p), w.failOn) {
		w.failed = true
		return 0, w.err
	}
	return len(p), nil
}

func TestRunStopsOnOutputError(t *testing.T) {
	writeErr := errors.New("output unavailable")
	for _, failOn := range []string{"Ledger service started", "1 |", "2 |", "3 |"} {
		t.Run(failOn, func(t *testing.T) {
			out := &failingOutput{failOn: failOn, err: writeErr}
			err := run(out)
			if !errors.Is(err, writeErr) || err == writeErr {
				t.Fatalf("run() error = %v, want wrapped output error", err)
			}
			if out.writesAfterFailure != 0 {
				t.Fatalf("writes after output error = %d, want 0", out.writesAfterFailure)
			}
		})
	}
}
