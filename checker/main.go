package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// testCard is the fixed card used for site validation.
// CARD_DECLINED / INCORRECT_NUMBER / CAPTCHA_REQUIRED / GENERIC_ERROR → working.
const testCard = "4242424242424242|03|30|000"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	db, err := NewDB()
	if err != nil {
		log.Fatalf("[main] DB connect failed: %v", err)
	}
	defer db.Close()
	log.Println("[main] Database connected")

	concurrency := 5
	if v := os.Getenv("CHECKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	batchSize := concurrency * 2
	if v := os.Getenv("CHECKER_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batchSize = n
		}
	}

	port := "8080"
	if v := os.Getenv("PORT"); v != "" {
		port = v
	}

	// Start dashboard HTTP server
	go StartDashboard(":"+port, db)
	log.Printf("[main] Dashboard listening on :%s", port)

	// Graceful shutdown
	stop := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Println("[main] Shutting down...")
		close(stop)
	}()

	runWorker(db, concurrency, batchSize, stop)
}

func runWorker(db *DB, concurrency, batchSize int, stop <-chan struct{}) {
	log.Printf("[worker] Starting (concurrency=%d, batchSize=%d)", concurrency, batchSize)

	// Reset anything stuck from a previous run
	if n, err := db.ResetStuck(); err == nil && n > 0 {
		log.Printf("[worker] Reset %d stuck sites", n)
	}

	sem := make(chan struct{}, concurrency)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if n, err := db.ResetStuck(); err == nil && n > 0 {
				log.Printf("[worker] Reset %d stuck sites", n)
			}

			sites, err := db.ClaimPending(batchSize)
			if err != nil {
				log.Printf("[worker] ClaimPending error: %v", err)
				continue
			}
			if len(sites) == 0 {
				continue
			}

			log.Printf("[worker] Checking %d sites...", len(sites))
			var wg sync.WaitGroup
			for _, site := range sites {
				wg.Add(1)
				sem <- struct{}{}
				go func(s Site) {
					defer wg.Done()
					defer func() { <-sem }()
					checkSite(db, s)
				}(site)
			}
			wg.Wait()
		}
	}
}

func checkSite(db *DB, site Site) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[checker] PANIC on %s: %v", site.URL, r)
			db.MarkError(site.ID, "PANIC", fmt.Sprintf("%v", r))
		}
	}()

	proxyURL := os.Getenv("PROXY_URL") // optional single proxy

	result, err := runCheckoutForCard(site.URL, testCard, proxyURL)

	if err != nil && result == nil {
		log.Printf("[checker] %s → ERROR (nil result): %v", site.URL, err)
		db.MarkError(site.ID, "INTERNAL_ERROR", err.Error())
		return
	}

	code := ""
	if result != nil {
		code = result.StatusCode
	}

	// These codes mean the site is alive and processing payments — mark working
	switch code {
	case "CARD_DECLINED", "INCORRECT_NUMBER", "CAPTCHA_REQUIRED", "GENERIC_ERROR":
		price := 0.0
		if result != nil && result.Amount != "" {
			if f, e := strconv.ParseFloat(result.Amount, 64); e == nil {
				price = f
			}
		}
		log.Printf("[checker] WORKING: %s (%s, $%.2f)", site.URL, code, price)
		db.MarkWorking(site.ID, price)

	default:
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		log.Printf("[checker] DEAD: %s (%s) %s", site.URL, code, msg)
		db.MarkDead(site.ID, code, msg)
	}
}
