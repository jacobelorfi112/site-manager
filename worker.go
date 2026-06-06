package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// SiteCheckWorker continuously pulls pending sites from the DB and runs
// a full checkout with a test card. If the site returns INCORRECT_NUMBER,
// the checkout flow works → site is marked working.
type SiteCheckWorker struct {
	db        *DB
	browser   *Browser
	batchSize int
}

// Test card used for site validation — triggers INCORRECT_NUMBER if checkout works.
var workerTestCard = CardInfo{
	Raw:    "5524860214037312|10|28|950",
	Number: "5524 8602 1403 7312",
	ExpMM:  "10",
	ExpYY:  "28",
	CVV:    "950",
	Last4:  "7312",
}

// NewSiteCheckWorker creates a background worker.
func NewSiteCheckWorker(db *DB, browser *Browser, batchSize int) *SiteCheckWorker {
	if batchSize <= 0 {
		batchSize = 3
	}
	return &SiteCheckWorker{db: db, browser: browser, batchSize: batchSize}
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

// checkSite runs a full checkout with a test card via the browser.
// INCORRECT_NUMBER means the checkout flow works end-to-end → site is working.
func (w *SiteCheckWorker) checkSite(site Site) {
	storeURL := site.URL
	buyer := DefaultBuyer()

	entry := CardEntry{
		Card:  workerTestCard,
		Store: storeURL,
		Index: 0,
	}
	entry.Card.FullName = buyer.FirstName + " " + buyer.LastName

	// Acquire a tab slot
	w.browser.tabSem <- struct{}{}
	defer func() { <-w.browser.tabSem }()

	// Recover from panics
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[worker] PANIC checking %s: %v", storeURL, r)
			w.db.UpdateSiteResult(site.ID, StatusError, "PANIC", fmt.Sprintf("%v", r), 0)
		}
	}()

	// Open a new tab
	page, err := w.browser.browser.Page(proto.TargetCreateTarget{URL: ""})
	if err != nil {
		log.Printf("[worker] Tab create failed for %s: %v", storeURL, err)
		w.db.UpdateSiteResult(site.ID, StatusError, "TAB_CREATE_FAILED", err.Error(), 0)
		return
	}
	defer page.Close()
	page = page.Timeout(180 * time.Second)

	// Run the full checkout
	result := runCheckout(page, entry, buyer)
	log.Printf("[worker] %s → %s (%s) in %.1fs", storeURL, result.Status, result.ErrorCode, result.ElapsedSeconds)

	// INCORRECT_NUMBER or CARD_DECLINED = checkout flow works, site is valid
	if result.ErrorCode == "INCORRECT_NUMBER" || result.ErrorCode == "CARD_DECLINED" {
		price := result.CheckoutPrice
		log.Printf("[worker] WORKING: %s ($%.2f)", storeURL, price)
		w.db.UpdateSiteResult(site.ID, StatusWorking, "CHECKOUT_VERIFIED", fmt.Sprintf("full checkout works ($%.2f)", price), price)
		return
	}

	// Site is not working — delete it
	errMsg := result.ErrorCode
	if result.ErrorMessage != "" {
		errMsg = result.ErrorMessage
	}
	log.Printf("[worker] REMOVING: %s (%s)", storeURL, errMsg)
	w.db.DeleteSite(site.ID)
}
