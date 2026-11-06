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

	"github.com/skitchen1992/Ledger/gateway/internal/api"
	"github.com/skitchen1992/Ledger/ledger/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("start Gateway: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	service, closeFn, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	addr := os.Getenv("GATEWAY_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen for Gateway HTTP requests: %w", err)
	}
	log.Printf("Gateway HTTP listening on %s", listener.Addr())
	return serve(ctx, listener, api.NewHandler(service))
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Gateway HTTP requests: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			server.Close()
			return fmt.Errorf("shut down Gateway HTTP server: %w", err)
		}
		if err := <-finished; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Gateway HTTP requests: %w", err)
		}
		return nil
	}
}
