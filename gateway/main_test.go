package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
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

func TestRequestTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		unset       bool
		want        time.Duration
		wantError   bool
	}{
		{name: "default", unset: true, want: 2 * time.Second},
		{name: "milliseconds", value: "50ms", want: 50 * time.Millisecond},
		{name: "seconds", value: "3s", want: 3 * time.Second},
		{name: "zero", value: "0s", wantError: true},
		{name: "negative", value: "-1ms", wantError: true},
		{name: "empty", value: "", wantError: true},
		{name: "no unit", value: "50", wantError: true},
		{name: "invalid", value: "invalid", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("REQUEST_TIMEOUT", tc.value)
			if tc.unset {
				if err := os.Unsetenv("REQUEST_TIMEOUT"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := requestTimeoutFromEnv()
			if (err != nil) != tc.wantError || got != tc.want {
				t.Errorf("timeout = %s, err = %v; want %s, error=%t", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestRunRejectsInvalidTimeoutBeforeConnecting(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "0s")
	if err := run(context.Background()); err == nil || err.Error() != "REQUEST_TIMEOUT must be a positive duration, for example 2s or 50ms" {
		t.Errorf("run with invalid request timeout = %v", err)
	}
}
