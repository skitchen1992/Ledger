package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const demoBudgetsJSON = `[{"category":"еда","limit":5000},{"category":"транспорт","limit":1000}]`

func writeBudgetFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budgets.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBudgetDemo(t *testing.T) {
	var out bytes.Buffer
	if err := run(writeBudgetFile(t, demoBudgetsJSON), &out); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.HasPrefix(output, "Ledger service started\n") {
		t.Errorf("missing startup message: %q", output)
	}
	if got := strings.Count(output, "Добавлено:"); got != 5 {
		t.Errorf("accepted transactions = %d, want 5; output:\n%s", got, output)
	}
	if got := strings.Count(output, "Отказ:"); got != 1 {
		t.Errorf("rejected transactions = %d, want 1; output:\n%s", got, output)
	}
	if !strings.Contains(output, "Отказ: еда — 2000.00 руб. (Ужин):") || !strings.Contains(output, "budget exceeded") {
		t.Errorf("missing budget rejection for dinner; output:\n%s", output)
	}
	const wantSaved = "Сохранено транзакций: 5\n" +
		"1 | 1500.00 руб. | еда | Продукты | 2026-11-06\n" +
		"2 | 2000.00 руб. | еда | Покупки на неделю | 2026-11-06\n" +
		"3 | 1500.00 руб. | еда | Обед | 2026-11-06\n" +
		"4 | 700.00 руб. | транспорт | Проездной | 2026-11-06\n" +
		"5 | 950.00 руб. | книги | Учебник Go | 2026-11-06\n"
	if !strings.HasSuffix(output, wantSaved) {
		t.Fatalf("saved transactions differ from accepted transactions; output:\n%s", output)
	}
}

func TestRunUsesLoadedBudgets(t *testing.T) {
	for _, tt := range []struct {
		name     string
		json     string
		accepted int
		rejected int
	}{
		{name: "lower food limit", json: `[{"category":"еда","limit":1000}]`, accepted: 2, rejected: 4},
		{name: "zero food limit", json: `[{"category":"еда","limit":0}]`, accepted: 2, rejected: 4},
		{name: "empty budgets", json: `[]`, accepted: 6, rejected: 0},
		{name: "no implicit food budget", json: `[{"category":"транспорт","limit":1000}]`, accepted: 6, rejected: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(writeBudgetFile(t, tt.json), &out); err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(out.String(), "Добавлено:"); got != tt.accepted {
				t.Errorf("accepted = %d, want %d; output:\n%s", got, tt.accepted, out.String())
			}
			if got := strings.Count(out.String(), "Отказ:"); got != tt.rejected {
				t.Errorf("rejected = %d, want %d; output:\n%s", got, tt.rejected, out.String())
			}
		})
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
	for _, failOn := range []string{
		"Ledger service started", "Добавлено:", "Отказ:", "Сохранено транзакций:",
		"1 |", "2 |", "3 |", "4 |", "5 |",
	} {
		t.Run(failOn, func(t *testing.T) {
			out := &failingOutput{failOn: failOn, err: writeErr}
			err := run(writeBudgetFile(t, demoBudgetsJSON), out)
			if !errors.Is(err, writeErr) || err == writeErr {
				t.Fatalf("run() error = %v, want wrapped output error", err)
			}
			if out.writesAfterFailure != 0 {
				t.Fatalf("writes after output error = %d, want 0", out.writesAfterFailure)
			}
		})
	}
}

func TestRunReportsFileErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		var out bytes.Buffer
		err := run(filepath.Join(t.TempDir(), "missing.json"), &out)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("run() error = %v, want original open error", err)
		}
		if out.Len() != 0 {
			t.Errorf("demo ran after open failure: %q", out.String())
		}
	})
	t.Run("directory cannot be read as file", func(t *testing.T) {
		var out bytes.Buffer
		err := run(t.TempDir(), &out)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != "read" {
			t.Fatalf("run() error = %v, want original read error", err)
		}
		if out.Len() != 0 {
			t.Errorf("demo ran after read failure: %q", out.String())
		}
	})
}

func TestRunReportsJSONErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		json      string
		wantCause func(error) bool
	}{
		{
			name: "invalid syntax", json: `[{"category":!}]`,
			wantCause: func(err error) bool {
				var syntaxErr *json.SyntaxError
				return errors.As(err, &syntaxErr)
			},
		},
		{
			name: "truncated array", json: `[{"category":"еда",`,
			wantCause: func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) },
		},
		{
			name: "wrong limit type", json: `[{"category":"еда","limit":"5000"}]`,
			wantCause: func(err error) bool {
				var typeErr *json.UnmarshalTypeError
				return errors.As(err, &typeErr)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(writeBudgetFile(t, tt.json), &out)
			if !tt.wantCause(err) {
				t.Fatalf("run() error = %v, want original JSON error", err)
			}
			if out.Len() != 0 {
				t.Errorf("demo ran after parsing failure: %q", out.String())
			}
		})
	}
}
