//go:build cfworker

package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	port := "8080"
	if v := os.Getenv("PORT"); v != "" {
		port = v
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	CFWorkerURL = os.Getenv("CF_WORKER_URL")
	if CFWorkerURL == "" {
		log.Fatal("CF_WORKER_URL environment variable is not set.\n" +
			"Set it to your Cloudflare Worker URL, e.g.:\n" +
			"  https://cf-site-manager.anonchat-notlak3.workers.dev")
	}
	fmt.Printf("CF Worker connected: %s\n", CFWorkerURL)

	batchSize := 20
	if v := os.Getenv("WORKER_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batchSize = n
		}
	}

	stopWorker := make(chan struct{})
	api := newCFAPIClient()
	worker := NewSiteCheckWorker(api, batchSize)
	go worker.Run(stopWorker)
	fmt.Println("Site check worker started")

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		fmt.Println("\nShutting down...")
		close(stopWorker)
		os.Exit(0)
	}()

	StartServer(":" + port)
}
