package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skitchen1992/Ledger/ledger/app"
	"github.com/skitchen1992/Ledger/ledger/internal/httpapi"
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
	ledgerService, closeFn, err := app.NewReporting(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	addr := os.Getenv("LEDGER_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen for Ledger HTTP requests on %q: %w", addr, err)
	}
	log.Printf("Ledger service started: HTTP listening on %s", listener.Addr())
	return serve(ctx, listener, httpapi.NewHandler(ledgerService))
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
