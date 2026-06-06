package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// maxCardsPerRequest is the hard limit on cards in a single POST /check.
const maxCardsPerRequest = 10000

// perStoreDefaultLimit caps concurrent checks against a single Shopify store.
const perStoreDefaultLimit = 10

// StartServer starts the HTTP API server on the given address.
func StartServer(addr string, browser *Browser, db *DB) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		handleCheck(w, r, browser)
	})

	// Register site management routes if DB is available
	if db != nil {
		RegisterSiteRoutes(mux, db)
		log.Println("Site management API enabled")
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 900 * time.Second, // long — large batches can take many minutes
		IdleTimeout:  120 * time.Second,
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

func handleCheck(w http.ResponseWriter, r *http.Request, browser *Browser) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req CheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	if len(req.Cards) == 0 {
		http.Error(w, `{"error":"cards array is empty"}`, http.StatusBadRequest)
		return
	}
	if len(req.Cards) > maxCardsPerRequest {
		http.Error(w, fmt.Sprintf(`{"error":"max %d cards per request"}`, maxCardsPerRequest), http.StatusBadRequest)
		return
	}

	// Use provided buyer or defaults
	buyer := DefaultBuyer()
	if req.Buyer != nil {
		if req.Buyer.Email != "" {
			buyer.Email = req.Buyer.Email
		}
		if req.Buyer.FirstName != "" {
			buyer.FirstName = req.Buyer.FirstName
		}
		if req.Buyer.LastName != "" {
			buyer.LastName = req.Buyer.LastName
		}
		if req.Buyer.Address1 != "" {
			buyer.Address1 = req.Buyer.Address1
		}
		if req.Buyer.Address2 != "" {
			buyer.Address2 = req.Buyer.Address2
		}
		if req.Buyer.City != "" {
			buyer.City = req.Buyer.City
		}
		if req.Buyer.State != "" {
			buyer.State = req.Buyer.State
		}
		if req.Buyer.StateName != "" {
			buyer.StateName = req.Buyer.StateName
		}
		if req.Buyer.Zip != "" {
			buyer.Zip = req.Buyer.Zip
		}
		if req.Buyer.Country != "" {
			buyer.Country = req.Buyer.Country
		}
		if req.Buyer.CountryName != "" {
			buyer.CountryName = req.Buyer.CountryName
		}
		if req.Buyer.Phone != "" {
			buyer.Phone = req.Buyer.Phone
		}
	}

	// Parse card entries
	entries := make([]CardEntry, 0, len(req.Cards))
	needsSites := false
	for i, raw := range req.Cards {
		entry, err := ParseCardEntry(raw, i)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"card %d: %s"}`, i, err.Error()), http.StatusBadRequest)
			return
		}
		entry.Card.FullName = buyer.FirstName + " " + buyer.LastName
		if entry.Store == "" {
			needsSites = true
		}
		entries = append(entries, entry)
	}

	// Fetch working sites for cards without a store URL
	var validSites []string
	if needsSites {
		var err error
		validSites, err = FetchWorkingSites()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to fetch sites: %s"}`, err.Error()), http.StatusServiceUnavailable)
			return
		}
		log.Printf("Fetched %d working sites (price <= $15)", len(validSites))
		// Round-robin distribute sites across cards for even load
		siteIdx := 0
		for i := range entries {
			if entries[i].Store == "" {
				entries[i].Store = validSites[siteIdx%len(validSites)]
				siteIdx++
			}
		}
	}

	start := time.Now()
	log.Printf("Starting batch: %d cards", len(entries))

	// Stream results as NDJSON — each line is a JSON object, flushed immediately.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")

	var streamMu sync.Mutex
	streamResult := func(cr CheckResult) {
		line, _ := json.Marshal(cr)
		streamMu.Lock()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()
		streamMu.Unlock()
	}

	results, err := browser.RunBatch(entries, buyer, perStoreDefaultLimit, streamResult)
	if err != nil {
		// Already started streaming, send error as NDJSON line
		errLine, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "%s\n", errLine)
		flusher.Flush()
		return
	}

	// Retry failed cards (CHECKOUT_FAILED) with a different site, up to 2 retries
	if len(validSites) > 1 {
		for retry := 0; retry < 2; retry++ {
			var retryEntries []CardEntry
			var retryIndexes []int
			for i, r := range results {
				if r.ErrorCode == "CHECKOUT_FAILED" && entries[i].Store != "" {
					newSite := DifferentSite(validSites, entries[i].Store)
					entries[i].Store = newSite
					retryEntries = append(retryEntries, entries[i])
					retryIndexes = append(retryIndexes, i)
				}
			}
			if len(retryEntries) == 0 {
				break
			}
			log.Printf("Retry %d: %d cards with new sites", retry+1, len(retryEntries))
			retryResults, retryErr := browser.RunBatch(retryEntries, buyer, perStoreDefaultLimit, streamResult)
			if retryErr != nil {
				break
			}
			for j, ri := range retryIndexes {
				results[ri] = retryResults[j]
			}
		}
	}

	elapsed := time.Since(start).Seconds()

	completed := 0
	for _, r := range results {
		if r.Status != "" {
			completed++
		}
	}

	// Send final summary line
	summary := map[string]interface{}{
		"_summary":        true,
		"total":           len(entries),
		"completed":       completed,
		"elapsed_seconds": elapsed,
	}
	line, _ := json.Marshal(summary)
	fmt.Fprintf(w, "%s\n", line)
	flusher.Flush()

	log.Printf("Batch complete: %d/%d in %.1fs", completed, len(entries), elapsed)
}
