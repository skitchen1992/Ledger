package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/skitchen1992/Ledger/ledger"
)

func main() {
	if err := run("budgets.json", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(budgetsPath string, out io.Writer) error {
	var store ledger.Ledger
	file, err := os.Open(budgetsPath)
	if err != nil {
		return fmt.Errorf("open budgets file %q: %w", budgetsPath, err)
	}
	defer file.Close()
	if err := store.LoadBudgets(bufio.NewReader(file)); err != nil {
		return fmt.Errorf("load budgets from %q: %w", budgetsPath, err)
	}

	if _, err := fmt.Fprintln(out, "Ledger service started"); err != nil {
		return fmt.Errorf("print startup message: %w", err)
	}

	// Демонстрационные значения проверяются через общий интерфейс и не сохраняются.
	for _, value := range []ledger.Validatable{
		ledger.Transaction{Amount: 100, Category: "пример", Date: "2026-11-06"},
		ledger.Budget{Category: "пример", Limit: 100},
	} {
		if err := ledger.CheckValid(value); err != nil {
			return fmt.Errorf("validate %T: %w", value, err)
		}
		if _, err := fmt.Fprintf(out, "Проверка %T: данные корректны\n", value); err != nil {
			return fmt.Errorf("print validation result: %w", err)
		}
	}

	for _, tx := range []ledger.Transaction{
		{Amount: 1500, Category: "еда", Description: "Продукты", Date: "2026-11-06"},
		{Amount: 2000, Category: "еда", Description: "Покупки на неделю", Date: "2026-11-06"},
		{Amount: 2000, Category: "еда", Description: "Ужин", Date: "2026-11-06"},
		{Amount: 1500, Category: "еда", Description: "Обед", Date: "2026-11-06"},
		{Amount: 700, Category: "транспорт", Description: "Проездной", Date: "2026-11-06"},
		{Amount: 950, Category: "книги", Description: "Учебник Go", Date: "2026-11-06"},
	} {
		if err := store.AddTransaction(tx); err != nil {
			if !errors.Is(err, ledger.ErrBudgetExceeded) {
				return fmt.Errorf("add transaction %q: %w", tx.Description, err)
			}
			if _, writeErr := fmt.Fprintf(out, "Отказ: %s — %.2f руб. (%s): %v\n",
				tx.Category, tx.Amount, tx.Description, err); writeErr != nil {
				return fmt.Errorf("print rejected transaction: %w", writeErr)
			}
			continue
		}
		if _, err := fmt.Fprintf(out, "Добавлено: %s — %.2f руб. (%s)\n",
			tx.Category, tx.Amount, tx.Description); err != nil {
			return fmt.Errorf("print accepted transaction: %w", err)
		}
	}

	saved := store.ListTransactions()
	if _, err := fmt.Fprintf(out, "\nСохранено транзакций: %d\n", len(saved)); err != nil {
		return fmt.Errorf("print transaction count: %w", err)
	}
	for _, tx := range saved {
		if _, err := fmt.Fprintf(out, "%d | %.2f руб. | %s | %s | %s\n",
			tx.ID, tx.Amount, tx.Category, tx.Description, tx.Date); err != nil {
			return fmt.Errorf("print transaction %d: %w", tx.ID, err)
		}
	}
	return nil
}
