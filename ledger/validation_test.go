package ledger

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestTransactionValidate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		tx        Transaction
		wantField string
	}{
		{name: "valid", tx: Transaction{Category: "еда", Amount: 10, Date: "2026-11-01"}},
		{name: "smallest positive amount", tx: Transaction{Category: "еда", Amount: math.SmallestNonzeroFloat64}},
		{name: "largest finite amount", tx: Transaction{Category: "еда", Amount: math.MaxFloat64}},
		{name: "string date has no format restriction", tx: Transaction{Category: "еда", Amount: 10, Date: "вчера"}},
		{name: "zero amount", tx: Transaction{Category: "еда"}, wantField: "amount"},
		{name: "negative amount", tx: Transaction{Category: "еда", Amount: -1}, wantField: "amount"},
		{name: "NaN amount", tx: Transaction{Category: "еда", Amount: math.NaN()}, wantField: "amount"},
		{name: "positive infinity", tx: Transaction{Category: "еда", Amount: math.Inf(1)}, wantField: "amount"},
		{name: "negative infinity", tx: Transaction{Category: "еда", Amount: math.Inf(-1)}, wantField: "amount"},
		{name: "empty category", tx: Transaction{Amount: 10}, wantField: "category"},
		{name: "blank category", tx: Transaction{Category: " \t\n", Amount: 10}, wantField: "category"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tx.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("Validate() = %v, want an error describing %s", err, tt.wantField)
			}
		})
	}
}

func TestBudgetValidate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		budget    Budget
		wantField string
	}{
		{name: "valid", budget: Budget{Category: "еда", Limit: 100}},
		{name: "smallest positive limit", budget: Budget{Category: "еда", Limit: math.SmallestNonzeroFloat64}},
		{name: "largest finite limit", budget: Budget{Category: "еда", Limit: math.MaxFloat64}},
		{name: "zero limit", budget: Budget{Category: "еда"}, wantField: "limit"},
		{name: "negative limit", budget: Budget{Category: "еда", Limit: -1}, wantField: "limit"},
		{name: "NaN limit", budget: Budget{Category: "еда", Limit: math.NaN()}, wantField: "limit"},
		{name: "positive infinity", budget: Budget{Category: "еда", Limit: math.Inf(1)}, wantField: "limit"},
		{name: "negative infinity", budget: Budget{Category: "еда", Limit: math.Inf(-1)}, wantField: "limit"},
		{name: "empty category", budget: Budget{Limit: 100}, wantField: "category"},
		{name: "blank category", budget: Budget{Category: " \t\n", Limit: 100}, wantField: "category"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.budget.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("Validate() = %v, want an error describing %s", err, tt.wantField)
			}
		})
	}
}

func TestCheckValidSupportsTransactionsAndBudgets(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   Validatable
		wantErr bool
	}{
		{name: "valid transaction", value: Transaction{Category: "еда", Amount: 10}},
		{name: "invalid transaction", value: Transaction{Category: "еда"}, wantErr: true},
		{name: "valid budget", value: Budget{Category: "еда", Limit: 100}},
		{name: "invalid budget", value: Budget{Category: "еда"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckValid(tt.value); (err != nil) != tt.wantErr {
				t.Fatalf("CheckValid(%+v) = %v, want error: %t", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestSetBudgetRejectsInvalidInputWithoutChangingState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		budget Budget
	}{
		{name: "zero limit", budget: Budget{Category: "еда"}},
		{name: "negative limit", budget: Budget{Category: "еда", Limit: -1}},
		{name: "NaN limit", budget: Budget{Category: "еда", Limit: math.NaN()}},
		{name: "positive infinity", budget: Budget{Category: "еда", Limit: math.Inf(1)}},
		{name: "negative infinity", budget: Budget{Category: "еда", Limit: math.Inf(-1)}},
		{name: "empty category", budget: Budget{Limit: 100}},
		{name: "blank category", budget: Budget{Category: " \t\n", Limit: 100}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, existingBudget := range []bool{false, true} {
				name := "new ledger"
				if existingBudget {
					name = "existing budget and history"
				}
				t.Run(name, func(t *testing.T) {
					var ledger Ledger
					if existingBudget {
						if err := ledger.SetBudget(Budget{Category: "еда", Limit: 100}); err != nil {
							t.Fatal(err)
						}
						if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 60}); err != nil {
							t.Fatal(err)
						}
					}
					want := ledger.ListTransactions()
					if err := ledger.SetBudget(tt.budget); err == nil {
						t.Fatal("invalid budget was accepted")
					}
					if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
						t.Fatalf("rejection changed history: got %+v, want %+v", got, want)
					}
					if existingBudget {
						wantBudgets := map[string]Budget{"еда": {Category: "еда", Limit: 100}}
						if !reflect.DeepEqual(ledger.budgets, wantBudgets) {
							t.Fatalf("rejection changed budgets: got %+v, want %+v", ledger.budgets, wantBudgets)
						}
					} else if len(ledger.budgets) != 0 {
						t.Fatalf("rejection added a budget: %+v", ledger.budgets)
					}
					amount := 1000.0
					if existingBudget {
						amount = 40
					}
					if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: amount}); err != nil {
						t.Fatalf("rejection changed available budget: %v", err)
					}
					want = append(want, Transaction{ID: len(want) + 1, Category: "еда", Amount: amount})
					if existingBudget {
						if err := ledger.AddTransaction(Transaction{Category: "еда", Amount: 1}); !errors.Is(err, ErrBudgetExceeded) {
							t.Fatalf("rejection changed exhausted budget: error = %v, want ErrBudgetExceeded", err)
						}
					}
					if got := ledger.ListTransactions(); !reflect.DeepEqual(got, want) {
						t.Fatalf("subsequent history = %+v, want %+v", got, want)
					}
				})
			}
		})
	}
}
