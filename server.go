package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// StartServer starts the HTTP API server on the given address.
// In CF Worker mode, the Go binary only serves /health and the checker worker.
// The dashboard and site management API live on the CF Worker.
func StartServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)

	srv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	log.Printf("Server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
