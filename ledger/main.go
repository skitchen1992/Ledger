package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out io.Writer) error {
	ledger := Ledger{transactions: make([]Transaction, 0)}
	if _, err := fmt.Fprintln(out, "Ledger service started"); err != nil {
		return fmt.Errorf("print startup message: %w", err)
	}

	for _, tx := range []Transaction{
		{Amount: 1500, Category: "еда", Description: "Продукты", Date: "2026-11-06"},
		{Amount: 80, Category: "транспорт", Description: "Метро", Date: "2026-11-06"},
		{Amount: 950, Category: "книги", Description: "Учебник Go", Date: "2026-11-06"},
	} {
		if err := ledger.AddTransaction(tx); err != nil {
			return fmt.Errorf("add transaction %q: %w", tx.Description, err)
		}
	}

	for _, tx := range ledger.ListTransactions() {
		if _, err := fmt.Fprintf(out, "%d | %.2f руб. | %s | %s | %s\n",
			tx.ID, tx.Amount, tx.Category, tx.Description, tx.Date); err != nil {
			return fmt.Errorf("print transaction %d: %w", tx.ID, err)
		}
	}
	return nil
}
