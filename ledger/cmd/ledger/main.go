package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skitchen1992/Ledger/ledger/internal/cache"
	"github.com/skitchen1992/Ledger/ledger/internal/db"
	"github.com/skitchen1992/Ledger/ledger/internal/httpapi"
	"github.com/skitchen1992/Ledger/ledger/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("start Ledger: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	pool, err := db.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	log.Print("Ledger connected to PostgreSQL")
	if err := checkSchema(ctx, pool); err != nil {
		return err
	}

	redisClient, err := cache.Open(ctx)
	if err != nil {
		return err
	}
	defer redisClient.Close()
	log.Print("Ledger connected to Redis")

	addr := os.Getenv("LEDGER_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen for Ledger HTTP requests on %q: %w", addr, err)
	}
	log.Printf("Ledger service started: HTTP listening on %s", listener.Addr())
	return serve(ctx, listener, httpapi.NewHandler(store.New(pool, redisClient)))
}

func checkSchema(ctx context.Context, pool *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, query := range []string{
		"SELECT id, category, limit_amount FROM budgets LIMIT 0",
		"SELECT id, amount, category, description, date FROM expenses LIMIT 0",
	} {
		rows, err := pool.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("check Ledger schema (apply Goose migrations before starting): %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close Ledger schema query: %w", err)
		}
	}
	return nil
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	defer listener.Close()
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Ledger HTTP requests: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			server.Close()
			return fmt.Errorf("shut down Ledger HTTP server: %w", err)
		}
		if err := <-finished; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Ledger HTTP requests: %w", err)
		}
		return nil
	}
}
