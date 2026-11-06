package main

import (
	"log"
	"net/http"
	"time"

	"github.com/skitchen1992/Ledger/gateway/internal/api"
)

func main() {
	server := &http.Server{
		Addr:              ":8080",
		Handler:           api.NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("start Gateway HTTP server: %v", err)
	}
}
