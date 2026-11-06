package main

import (
	"math"
	"reflect"
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
