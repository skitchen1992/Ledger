package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestAddAndListTransactions(t *testing.T) {
	var ledger Ledger
	if got := ledger.ListTransactions(); len(got) != 0 {
		t.Fatalf("new ledger contains transactions: %+v", got)
	}

	for _, tx := range []Transaction{
		{ID: 99, Amount: 125.50, Category: "еда", Description: "Обед", Date: "2026-11-01"},
		{ID: 99, Amount: 80, Category: "транспорт", Description: "Метро", Date: "2026-11-02"},
		{Amount: 900, Category: "книги", Description: "Книга", Date: "2026-11-03"},
	} {
		if err := ledger.AddTransaction(tx); err != nil {
			t.Fatalf("AddTransaction(%+v): %v", tx, err)
		}
	}

	want := []Transaction{
		{ID: 1, Amount: 125.50, Category: "еда", Description: "Обед", Date: "2026-11-01"},
		{ID: 2, Amount: 80, Category: "транспорт", Description: "Метро", Date: "2026-11-02"},
		{ID: 3, Amount: 900, Category: "книги", Description: "Книга", Date: "2026-11-03"},
	}
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTransactions() = %+v, want %+v", got, want)
	}
}

func TestAddTransactionRejectsInvalidInputWithoutChangingHistory(t *testing.T) {
	for _, tt := range []struct {
		name string
		tx   Transaction
	}{
		{name: "zero amount", tx: Transaction{Category: "еда"}},
		{name: "negative amount", tx: Transaction{Category: "еда", Amount: -1}},
		{name: "NaN amount", tx: Transaction{Category: "еда", Amount: math.NaN()}},
		{name: "positive infinity", tx: Transaction{Category: "еда", Amount: math.Inf(1)}},
		{name: "negative infinity", tx: Transaction{Category: "еда", Amount: math.Inf(-1)}},
		{name: "empty category", tx: Transaction{Amount: 10}},
		{name: "blank category", tx: Transaction{Category: " \t\n", Amount: 10}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ledger Ledger
			if err := ledger.AddTransaction(Transaction{Category: "книги", Amount: 10}); err != nil {
				t.Fatal(err)
			}
			if err := ledger.AddTransaction(tt.tx); err == nil {
				t.Fatal("invalid transaction was accepted")
			}
			want := []Transaction{{ID: 1, Category: "книги", Amount: 10}}
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("rejection changed history: got %+v, want %+v", got, want)
			}
			if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 20}); err != nil {
				t.Fatal(err)
			}
			want = append(want, Transaction{ID: 2, Category: "еда", Amount: 20})
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("rejection changed subsequent IDs: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestListTransactionsReturnsIndependentCopy(t *testing.T) {
	var ledger Ledger
	tx := Transaction{Amount: 100, Category: "еда", Description: "Обед", Date: "2026-11-01"}
	if err := ledger.AddTransaction(tx); err != nil {
		t.Fatal(err)
	}
	snapshot := ledger.ListTransactions()
	snapshot[0] = Transaction{ID: 500, Amount: 999, Category: "изменено"}
	tx.ID = 1
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, []Transaction{tx}) {
		t.Fatalf("editing a returned slice changed history: %+v", got)
	}

	var another Ledger
	if got := another.ListTransactions(); len(got) != 0 {
		t.Fatalf("independent ledger shares history: %+v", got)
	}
}

func TestAddTransactionChecksCategoryBudget(t *testing.T) {
	for _, tt := range []struct {
		name     string
		limit    float64
		previous []float64
		amount   float64
		wantErr  bool
	}{
		{name: "below cumulative limit", limit: 100, previous: []float64{30, 40}, amount: 29},
		{name: "equal to cumulative limit", limit: 100, previous: []float64{30, 40}, amount: 30},
		{name: "equal to fractional limit", limit: 0.30, previous: []float64{0.10}, amount: 0.20},
		{name: "equal to small fractional limit", limit: 3e-7, previous: []float64{1e-7}, amount: 2e-7},
		{name: "above fractional limit", limit: 0.30, previous: []float64{0.10}, amount: 0.21, wantErr: true},
		{name: "smallest representable excess", limit: 0.30, amount: math.Nextafter(0.30, math.Inf(1)), wantErr: true},
		{name: "above cumulative limit", limit: 100, previous: []float64{30, 40}, amount: 31, wantErr: true},
		{name: "above limit without history", limit: 100, amount: 101, wantErr: true},
		{name: "zero limit", limit: 0, amount: 1, wantErr: true},
		{name: "sum overflows", limit: math.MaxFloat64, previous: []float64{math.MaxFloat64}, amount: math.MaxFloat64, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ledger Ledger
			ledger.SetBudget(Budget{Category: "еда", Limit: tt.limit})
			for _, amount := range tt.previous {
				if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: amount}); err != nil {
					t.Fatal(err)
				}
			}
			want := ledger.ListTransactions()
			tx := Transaction{Category: "еда", Amount: tt.amount}
			err := ledger.AddTransaction(tx)
			if tt.wantErr {
				if !errors.Is(err, ErrBudgetExceeded) {
					t.Fatalf("AddTransaction() error = %v, want ErrBudgetExceeded", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				tx.ID = len(want) + 1
				want = append(want, tx)
			}
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("history = %+v, want %+v", got, want)
			}

			// Отклонение не занимает ID; категория без бюджета остаётся доступна.
			if err := ledger.AddTransaction(Transaction{Category: "книги", Amount: 1000}); err != nil {
				t.Fatal(err)
			}
			want = append(want, Transaction{ID: len(want) + 1, Category: "книги", Amount: 1000})
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("subsequent history = %+v, want %+v", got, want)
			}
		})
	}
}

func TestBudgetsAreIndependentBetweenCategoriesAndLedgers(t *testing.T) {
	var ledger Ledger
	ledger.SetBudget(Budget{Category: "еда", Limit: 100})
	ledger.SetBudget(Budget{Category: "транспорт", Limit: 200})
	for _, tx := range []Transaction{
		{Category: "еда", Amount: 100},
		{Category: "транспорт", Amount: 200},
		{Category: "книги", Amount: 10000},
	} {
		if err := ledger.AddTransaction(tx); err != nil {
			t.Fatalf("AddTransaction(%+v): %v", tx, err)
		}
	}
	want := []Transaction{
		{ID: 1, Category: "еда", Amount: 100},
		{ID: 2, Category: "транспорт", Amount: 200},
		{ID: 3, Category: "книги", Amount: 10000},
	}
	for _, category := range []string{"еда", "транспорт"} {
		if err := ledger.AddTransaction(Transaction{Category: category, Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("exhausted category %q: error = %v, want ErrBudgetExceeded", category, err)
		}
	}
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}

	var another Ledger
	if err := another.AddTransaction(Transaction{Category: "еда", Amount: 1000}); err != nil {
		t.Fatalf("independent ledger inherited a budget: %v", err)
	}
}

func TestSetBudgetKeepsExistingExpensesWhenSettingAndUpdating(t *testing.T) {
	var ledger Ledger
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 60}); err != nil {
		t.Fatal(err)
	}
	ledger.SetBudget(Budget{Category: "еда", Limit: 100})
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 40}); err != nil {
		t.Fatal(err)
	}
	ledger.SetBudget(Budget{Category: "еда", Limit: 150})
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 50}); err != nil {
		t.Fatal(err)
	}
	ledger.SetBudget(Budget{Category: "еда", Limit: 100})
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("lowered budget: error = %v, want ErrBudgetExceeded", err)
	}
	ledger.SetBudget(Budget{Category: "еда", Limit: 200})
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 50}); err != nil {
		t.Fatal(err)
	}
	want := []Transaction{
		{ID: 1, Category: "еда", Amount: 60},
		{ID: 2, Category: "еда", Amount: 40},
		{ID: 3, Category: "еда", Amount: 50},
		{ID: 4, Category: "еда", Amount: 50},
	}
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("budget update changed history: got %+v, want %+v", got, want)
	}
}

func TestLoadBudgetsAddsUpdatesAndKeepsHistory(t *testing.T) {
	var ledger Ledger
	ledger.SetBudget(Budget{Category: "еда", Limit: 500})
	ledger.SetBudget(Budget{Category: "подарки", Limit: 10})
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 50}); err != nil {
		t.Fatal(err)
	}
	input := `[
		{"category":"еда","limit":100},
		{"category":"транспорт","limit":200},
		{"category":"книги","limit":0},
		{"category":"еда","limit":150}
	]`
	if err := ledger.LoadBudgets(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	want := []Transaction{{ID: 1, Category: "еда", Amount: 50}}
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("loading budgets changed history: got %+v, want %+v", got, want)
	}
	for _, tx := range []Transaction{
		{Category: "еда", Amount: 100},
		{Category: "транспорт", Amount: 200},
		{Category: "подарки", Amount: 10},
	} {
		if err := ledger.AddTransaction(tx); err != nil {
			t.Fatalf("AddTransaction(%+v): %v", tx, err)
		}
		tx.ID = len(want) + 1
		want = append(want, tx)
	}
	for _, category := range []string{"еда", "транспорт", "книги", "подарки"} {
		if err := ledger.AddTransaction(Transaction{Category: category, Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("loaded category %q: error = %v, want ErrBudgetExceeded", category, err)
		}
	}
	if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
}

func TestLoadBudgetsAcceptsEmptyArrayWithoutClearingBudgets(t *testing.T) {
	var ledger Ledger
	ledger.SetBudget(Budget{Category: "еда", Limit: 0})
	if err := ledger.LoadBudgets(strings.NewReader(" \n [] \t\n")); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("empty array cleared an existing budget: %v", err)
	}
	if got := ledger.ListTransactions(); len(got) != 0 {
		t.Fatalf("rejected transaction was saved: %+v", got)
	}
}

func TestLoadBudgetsRejectsInvalidInputAtomically(t *testing.T) {
	readErr := errors.New("budget source unavailable")
	for _, tt := range []struct {
		name       string
		reader     io.Reader
		wantCause  error
		wantSyntax bool
		wantType   bool
	}{
		{name: "empty source", reader: strings.NewReader(""), wantCause: io.EOF},
		{name: "whitespace source", reader: strings.NewReader(" \n\t"), wantCause: io.EOF},
		{name: "null root", reader: strings.NewReader("null")},
		{name: "object root", reader: strings.NewReader(`{"category":"еда","limit":5}`), wantType: true},
		{name: "string root", reader: strings.NewReader(`"budgets"`), wantType: true},
		{name: "invalid syntax", reader: strings.NewReader(`[{!]`), wantSyntax: true},
		{name: "incomplete array", reader: strings.NewReader(`[{"category":"еда","limit":5}`), wantCause: io.ErrUnexpectedEOF},
		{name: "null entry", reader: strings.NewReader(`[null]`)},
		{name: "missing category", reader: strings.NewReader(`[{"limit":5}]`)},
		{name: "blank category", reader: strings.NewReader(`[{"category":" \t\n","limit":5}]`)},
		{name: "missing limit", reader: strings.NewReader(`[{"category":"еда"}]`)},
		{name: "null limit", reader: strings.NewReader(`[{"category":"еда","limit":null}]`)},
		{name: "negative limit", reader: strings.NewReader(`[{"category":"еда","limit":-1}]`)},
		{name: "string limit", reader: strings.NewReader(`[{"category":"еда","limit":"5"}]`), wantType: true},
		{name: "nonfinite limit", reader: strings.NewReader(`[{"category":"еда","limit":1e999}]`), wantType: true},
		{name: "last entry invalid", reader: strings.NewReader(`[{"category":"еда","limit":5},{"category":"книги","limit":0},{"category":"транспорт","limit":-1}]`)},
		{name: "extra JSON value", reader: strings.NewReader(`[{"category":"еда","limit":5},{"category":"книги","limit":0}] null`)},
		{name: "trailing garbage", reader: strings.NewReader(`[{"category":"еда","limit":5},{"category":"книги","limit":0}] !`), wantSyntax: true},
		{name: "source read error", reader: budgetErrorReader{err: readErr}, wantCause: readErr},
		{name: "read error during array", reader: io.MultiReader(strings.NewReader(`[{"category":"еда","limit":`), budgetErrorReader{err: readErr}), wantCause: readErr},
		{name: "read error after array", reader: io.MultiReader(strings.NewReader(`[{"category":"еда","limit":5},{"category":"книги","limit":0}]`), budgetErrorReader{err: readErr}), wantCause: readErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ledger Ledger
			ledger.SetBudget(Budget{Category: "еда", Limit: 100})
			if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 10}); err != nil {
				t.Fatal(err)
			}
			err := ledger.LoadBudgets(tt.reader)
			if err == nil {
				t.Fatal("invalid budgets were accepted")
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("LoadBudgets() error = %v, want wrapped %v", err, tt.wantCause)
			}
			if tt.wantSyntax {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) {
					t.Fatalf("LoadBudgets() error = %v, want wrapped *json.SyntaxError", err)
				}
			}
			if tt.wantType {
				var typeErr *json.UnmarshalTypeError
				if !errors.As(err, &typeErr) {
					t.Fatalf("LoadBudgets() error = %v, want wrapped *json.UnmarshalTypeError", err)
				}
			}
			want := []Transaction{{ID: 1, Category: "еда", Amount: 10}}
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("loading failure changed history: got %+v, want %+v", got, want)
			}
			if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 90}); err != nil {
				t.Fatalf("loading failure changed the existing budget: %v", err)
			}
			if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("loading failure cleared the existing budget: %v", err)
			}
			if err := ledger.AddTransaction(Transaction{Category: "книги", Amount: 1}); err != nil {
				t.Fatalf("loading failure added a new budget: %v", err)
			}
			want = append(want,
				Transaction{ID: 2, Category: "еда", Amount: 90},
				Transaction{ID: 3, Category: "книги", Amount: 1},
			)
			if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
				t.Fatalf("subsequent history = %+v, want %+v", got, want)
			}
		})
	}
}

type budgetErrorReader struct {
	err error
}

func (r budgetErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}
