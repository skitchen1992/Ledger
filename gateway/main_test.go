package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/skitchen1992/Ledger/gateway/internal/api"
)

func TestServePingAndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, listener, api.NewHandler(nil)) }()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "\"pong\"\n" {
		t.Errorf("GET /ping = %d %q, want 200 and JSON pong", response.StatusCode, body)
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Gateway did not stop after context cancellation")
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("listener after shutdown: %v, want net.ErrClosed", err)
	}
}

func TestServeReturnsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if err := serve(context.Background(), listener, api.NewHandler(nil)); !errors.Is(err, net.ErrClosed) {
		t.Errorf("serve closed listener: %v, want net.ErrClosed", err)
	}
}
