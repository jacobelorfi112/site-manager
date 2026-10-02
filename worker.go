//go:build cfworker

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CFWorkerURL is the Cloudflare Worker that replaces the PostgreSQL database.
// Set via the CF_WORKER_URL env var.
var CFWorkerURL string

// cfSite is the JSON structure matching the CF Worker's site response.
type cfSite struct {
	ID                int64  `json:"id"`
	URL               string `json:"url"`
	Status            string `json:"status"`
	CheckoutPrice     float64
	EnabledCardBrands string `json:"enabled_card_brands"`
	SingleCurrency    bool   `json:"single_currency"`
}

// cfAPIClient wraps HTTP calls to the CF Worker.
type cfAPIClient struct {
	http *http.Client
}

func newCFAPIClient() *cfAPIClient {
	return &cfAPIClient{http: &http.Client{Timeout: 30 * time.Second}}
}

// claimPendingSites fetches N pending sites from the CF Worker.
func (c *cfAPIClient) claimPendingSites(batch int) ([]cfSite, error) {
	resp, err := c.http.Get(CFWorkerURL + "/sites/claim?batch=" + strconv.Itoa(batch))
	if err != nil {
		return nil, fmt.Errorf("claim request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claim returned %d: %s", resp.StatusCode, string(body)[:min(len(body), 200)])
	}
	var r struct {
		Sites []cfSite `json:"sites"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("claim parse: %w", err)
	}
	sites := make([]cfSite, len(r.Sites))
	for i, s := range r.Sites {
		sites[i] = cfSite{ID: s.ID, URL: s.URL}
	}
	return sites, nil
}

// postResult posts the check result back to the CF Worker.
func (c *cfAPIClient) postResult(url, status, errorCode, errorMsg string, price float64, cardBrands string, singleCurrency bool) error {
	payload := map[string]interface{}{
		"url":                url,
		"status":             status,
		"error_code":         errorCode,
		"error_msg":          errorMsg,
		"checkout_price":     price,
		"enabled_card_brands": cardBrands,
		"single_currency":    singleCurrency,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.http.Post(CFWorkerURL+"/sites/result", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("result post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("result returned %d", resp.StatusCode)
	}
	return nil
}

// resetStuckChecking resets sites stuck in "checking" for too long.
// The CF Worker handles this via a timestamp check — we just call the endpoint.
func (c *cfAPIClient) resetStuckChecking() error {
	resp, err := c.http.Post(CFWorkerURL+"/sites/recheck-all", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

// SiteCheckWorker continuously pulls pending sites from the CF Worker and runs
// a full checkout with a test card. If the site returns INCORRECT_NUMBER,
// the checkout flow works → site is marked working.
type SiteCheckWorker struct {
	api       *cfAPIClient
	batchSize int
}

// NewSiteCheckWorker creates a background worker (CF Worker API mode).
func NewSiteCheckWorker(api *cfAPIClient, batchSize int) *SiteCheckWorker {
	if batchSize <= 0 {
		batchSize = 20
	}
	return &SiteCheckWorker{api: api, batchSize: batchSize}
}

// Run starts the worker loop. Call in a goroutine.
func (w *SiteCheckWorker) Run(stop <-chan struct{}) {
	log.Println("[worker] Site check worker started (CF Worker API mode)")

	ticker := time.NewTicker(2 * time.Second)
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
	sites, err := w.api.claimPendingSites(w.batchSize)
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
		go func(s cfSite) {
			defer wg.Done()
			w.checkSite(s)
		}(site)
	}
	wg.Wait()
}

// checkSite runs the checkout against the site with a test card.
func (w *SiteCheckWorker) checkSite(site cfSite) {
	storeURL := site.URL
	const testCardEntry = "5524860214037312|10|28|950"

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[worker] PANIC checking %s: %v", storeURL, r)
			w.api.postResult(storeURL, "error", "PANIC", fmt.Sprintf("%v", r), 0, "", false)
		}
	}()

	res, err := runCheckoutForCard(storeURL, testCardEntry, "")
	if res != nil {
		price := parseAmountString(res.Amount)

		if res.Status == BoDeclined || res.Status == BoApproved || res.Status == BoCharged ||
			res.StatusCode == "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED" {
			log.Printf("[worker] WORKING: %s ($%.2f) [%s]", storeURL, price, res.StatusCode)
			w.api.postResult(storeURL, "working", "CHECKOUT_VERIFIED",
				fmt.Sprintf("checkout works (%s)", res.StatusCode), price,
				res.EnabledCardBrands, res.SingleCurrency)
			return
		}
	}

	if err != nil {
		errMsg := err.Error()
		if res != nil && res.StatusCode != "" {
			errMsg = res.StatusCode + ": " + errMsg
		}

		// Structural gateway failures — the store has NO card payment gateway.
		// These can NEVER charge, regardless of card quality. Mark as dead
		// immediately so they don't waste retry slots.
		if res != nil && res.StatusCode != "" {
			if strings.Contains(res.StatusCode, "PAYMENTS_METHOD") ||
				strings.Contains(res.StatusCode, "PAYMENTS_PROPOSED_GATEWAY_UNAVAILABLE") ||
				strings.Contains(res.StatusCode, "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED") {
				log.Printf("[worker] DEAD (no card gateway): %s (%s)", storeURL, errMsg)
				w.api.postResult(storeURL, "dead", res.StatusCode, errMsg, 0, "", false)
				return
			}
		}

		if isTransientErr(err) {
			log.Printf("[worker] RETRYABLE: %s (%s)", storeURL, errMsg)
			w.api.postResult(storeURL, "error", "TRANSIENT", errMsg, 0, "", false)
			return
		}
		if res != nil && res.Retryable {
			log.Printf("[worker] RETRYABLE: %s (%s)", storeURL, errMsg)
			w.api.postResult(storeURL, "error", "RETRYABLE", errMsg, 0, "", false)
			return
		}
		log.Printf("[worker] DEAD: %s (%s)", storeURL, errMsg)
		w.api.postResult(storeURL, "dead", "CHECK_FAILED", errMsg, 0, "", false)
		return
	}
	if res == nil {
		log.Printf("[worker] ERROR: %s (nil result)", storeURL)
		w.api.postResult(storeURL, "error", "NIL_RESULT", "nil result from checkout", 0, "", false)
		return
	}

	price := parseAmountString(res.Amount)

	if res.Status == BoDeclined || res.Status == BoApproved || res.Status == BoCharged {
		log.Printf("[worker] WORKING: %s ($%.2f) [%s]", storeURL, price, res.StatusCode)
		w.api.postResult(storeURL, "working", "CHECKOUT_VERIFIED",
			fmt.Sprintf("full checkout works ($%.2f)", price), price,
			res.EnabledCardBrands, res.SingleCurrency)
		return
	}

	errMsg := res.StatusCode
	if errMsg == "" {
		errMsg = "unknown"
	}
	log.Printf("[worker] DEAD: %s (%s)", storeURL, errMsg)
	w.api.postResult(storeURL, "dead", "CHECK_FAILED", errMsg, 0, "", false)
}

// parseAmountString extracts a float amount from a currency-prefixed string.
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