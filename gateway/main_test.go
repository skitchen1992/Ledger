package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPing(t *testing.T) {
	handler := newHandler()
	for _, target := range []string{"/ping", "/ping?check=1"} {
		t.Run(target, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Body.String(); got != "pong" {
				t.Errorf("body = %q, want pong", got)
			}
			if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
			}
		})
	}
}

func TestPingRejectsOtherMethods(t *testing.T) {
	handler := newHandler()
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodHead, http.MethodOptions,
	} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, "/ping", nil))
			if response.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", response.Code)
			}
			if got := response.Header().Get("Allow"); got != http.MethodGet {
				t.Errorf("Allow = %q, want GET", got)
			}
		})
	}
}

func TestUnknownPaths(t *testing.T) {
	handler := newHandler()
	for _, path := range []string{"/", "/unknown", "/ping/", "/ping/extra", "/pingpong"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", response.Code)
			}
		})
	}
}

func TestPingLogsWriteFailureWithoutRetry(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	response := &failingResponseWriter{header: make(http.Header)}
	newHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if response.writes != 1 || response.headers != 1 {
		t.Errorf("response writes = %d, header writes = %d; want one each", response.writes, response.headers)
	}
	if got := output.String(); !strings.Contains(got, "write /ping response: connection lost") {
		t.Errorf("log = %q, want write failure with its cause", got)
	}
}

type failingResponseWriter struct {
	header  http.Header
	writes  int
	headers int
}

func (w *failingResponseWriter) Header() http.Header { return w.header }

func (w *failingResponseWriter) WriteHeader(int) { w.headers++ }

func (w *failingResponseWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("connection lost")
}
