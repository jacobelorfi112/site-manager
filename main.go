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
	// Max concurrent tabs in the single Chrome browser
	maxTabs := 30
	if v := os.Getenv("MAX_TABS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxTabs = n
		}
	}
	// Legacy env var support
	if v := os.Getenv("CHROME_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxTabs = n
		}
	}

	// HTTP port — Railway sets PORT env var
	port := "8080"
	if v := os.Getenv("PORT"); v != "" {
		port = v
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Connect to PostgreSQL (optional — if DATABASE_URL not set, site management is disabled)
	var db *DB
	if os.Getenv("DATABASE_URL") != "" {
		var err error
		db, err = NewDB()
		if err != nil {
			log.Fatalf("Failed to connect to database: %v", err)
		}
		defer db.Close()
		fmt.Println("Database connected, site management enabled")
	} else {
		fmt.Println("DATABASE_URL not set — site management disabled (card checking still works)")
	}

	fmt.Printf("Starting browser with max %d tabs...\n", maxTabs)
	browser := NewBrowser(maxTabs)
	defer browser.Close()

	// Worker batch size
	batchSize := 3
	if v := os.Getenv("WORKER_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batchSize = n
		}
	}

	// Start background site check worker if DB is available
	stopWorker := make(chan struct{})
	if db != nil {
		worker := NewSiteCheckWorker(db, browser, batchSize)
		go worker.Run(stopWorker)
		fmt.Println("Site check worker started")
	}

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		fmt.Println("\nShutting down...")
		close(stopWorker)
		browser.Close()
		if db != nil {
			db.Close()
		}
		os.Exit(0)
	}()

	StartServer(":"+port, browser, db)
}
