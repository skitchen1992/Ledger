package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsInvalidDatabaseConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://%not-a-valid-url")
	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("run() error = %v, want an explanatory PostgreSQL error", err)
	}
}

func TestRunPreservesStartupCancellation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want startup cancellation", err)
	}
}

func TestServeDrainsActiveRequestBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.Write([]byte("finished"))
		}))
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	requestDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			requestDone <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err == nil && string(body) != "finished" {
			err = errors.New("active request did not finish its response")
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("serve returned before the active request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve() error = %v, want graceful shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after the active request finished")
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	err = serve(context.Background(), listener, http.NotFoundHandler())
	if !errors.Is(err, net.ErrClosed) || !strings.Contains(err.Error(), "serve Ledger HTTP") {
		t.Fatalf("serve() error = %v, want contextual listener failure", err)
	}
}
