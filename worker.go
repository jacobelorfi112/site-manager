package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SiteCheckWorker continuously pulls pending sites from the DB and runs
// a full checkout with a test card. If the site returns INCORRECT_NUMBER,
// the checkout flow works → site is marked working.
type SiteCheckWorker struct {
	db        *DB
	batchSize int
}

// NewSiteCheckWorker creates a background worker.
func NewSiteCheckWorker(db *DB, batchSize int) *SiteCheckWorker {
	if batchSize <= 0 {
		batchSize = 3
	}
	return &SiteCheckWorker{db: db, batchSize: batchSize}
}

// Run starts the worker loop. Call in a goroutine.
func (w *SiteCheckWorker) Run(stop <-chan struct{}) {
	log.Println("[worker] Site check worker started")

	// Reset any sites stuck in "checking" from previous crashes
	if n, err := w.db.ResetStuckChecking(); err == nil && n > 0 {
		log.Printf("[worker] Reset %d stuck sites back to pending", n)
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			log.Println("[worker] Shutting down")
			return
		case <-ticker.C:
			w.processBatch()
		}
	}
}

func (w *SiteCheckWorker) processBatch() {
	// Reset stuck sites periodically
	if n, err := w.db.ResetStuckChecking(); err == nil && n > 0 {
		log.Printf("[worker] Reset %d stuck sites", n)
	}

	sites, err := w.db.ClaimPendingSites(w.batchSize)
	if err != nil {
		log.Printf("[worker] Error claiming sites: %v", err)
		return
	}
	if len(sites) == 0 {
		return
	}

	log.Printf("[worker] Checking %d sites...", len(sites))

	var wg sync.WaitGroup
	for _, site := range sites {
		wg.Add(1)
		go func(s Site) {
			defer wg.Done()
			w.checkSite(s)
		}(site)
	}
	wg.Wait()
}

// checkSite runs bo-main's TLS Shopify checkout against the site with a test
// card (proxyless). If the checkout gets far enough to reject the card
// (BoDeclined), the full pipeline works → site is working. Dead/broken sites
// are removed.
func (w *SiteCheckWorker) checkSite(site Site) {
	storeURL := site.URL
	// Fake card — triggers a card decision (BoDeclined) only if the checkout works.
	const testCardEntry = "5524860214037312|10|28|950"

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[worker] PANIC checking %s: %v", storeURL, r)
			w.db.UpdateSiteResult(site.ID, StatusError, "PANIC", fmt.Sprintf("%v", r), 0)
		}
	}()

	res, err := runCheckoutForCard(storeURL, testCardEntry, "")
	if err != nil {
		errMsg := err.Error()
		if res != nil && res.StatusCode != "" {
			errMsg = res.StatusCode + ": " + errMsg
		}
		log.Printf("[worker] %s → ERROR: %s", storeURL, errMsg)
		w.db.DeleteSite(site.ID)
		return
	}
	if res == nil {
		log.Printf("[worker] %s → ERROR: nil result", storeURL)
		w.db.DeleteSite(site.ID)
		return
	}

	price := parseAmountString(res.Amount)

	// BoDeclined = card rejected at payment → checkout pipeline works → site valid.
	if res.Status == BoDeclined {
		log.Printf("[worker] WORKING: %s ($%.2f) [%s]", storeURL, price, res.StatusCode)
		w.db.UpdateSiteResult(site.ID, StatusWorking, "CHECKOUT_VERIFIED", fmt.Sprintf("full checkout works ($%.2f)", price), price)
		return
	}

	// BoApproved (3DS) / BoCharged (order placed) also prove the pipeline works.
	if res.Status == BoApproved || res.Status == BoCharged {
		log.Printf("[worker] WORKING: %s ($%.2f) [%s]", storeURL, price, res.StatusCode)
		w.db.UpdateSiteResult(site.ID, StatusWorking, "CHECKOUT_VERIFIED", fmt.Sprintf("checkout works (%s)", res.StatusCode), price)
		return
	}

	errMsg := res.StatusCode
	if errMsg == "" {
		errMsg = "unknown"
	}
	log.Printf("[worker] REMOVING: %s (%s)", storeURL, errMsg)
	w.db.DeleteSite(site.ID)
}

// parseAmountString extracts a float amount from a currency-prefixed string
// like "$12.34", "12.34", or "USD 12.34".
func parseAmountString(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.TrimLeft(s, "$€£¥")
	num := ""
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			num += string(r)
		} else if num != "" {
			break
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return f
}
