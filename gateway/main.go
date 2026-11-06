package main

import (
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	server := &http.Server{
		Addr:              ":8080",
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("start Gateway HTTP server: %v", err)
	}
}

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		status, body := http.StatusOK, "pong"
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			status, body = http.StatusMethodNotAllowed, "Method Not Allowed\n"
		}
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			log.Printf("write /ping response: %v", err)
		}
	})
	return mux
}
