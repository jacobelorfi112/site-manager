package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

const siteManagerURL = "https://shopify-site-manager-production.up.railway.app/sites/working"
const maxCheckoutPrice = 15.0

type siteManagerResponse struct {
	Sites []siteEntry `json:"sites"`
	Total int         `json:"total"`
}

type siteEntry struct {
	ID            int     `json:"id"`
	URL           string  `json:"url"`
	Status        string  `json:"status"`
	CheckoutPrice float64 `json:"checkout_price"`
}

// FetchWorkingSites fetches working sites, filters by price, then validates
// each one by hitting /products.json to confirm it returns real JSON.
func FetchWorkingSites() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(siteManagerURL)
	if err != nil {
		return nil, fmt.Errorf("fetch sites: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("site manager returned %d", resp.StatusCode)
	}

	var data siteManagerResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("parse sites: %w", err)
	}

	var candidates []string
	for _, s := range data.Sites {
		if s.CheckoutPrice > 0 && s.CheckoutPrice <= maxCheckoutPrice {
			candidates = append(candidates, s.URL)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no working sites with price <= %.0f found", maxCheckoutPrice)
	}

	// Validate sites concurrently by checking /products.json
	validated := validateSites(candidates)
	if len(validated) == 0 {
		return nil, fmt.Errorf("none of the %d candidate sites returned valid products JSON", len(candidates))
	}

	return validated, nil
}

// validateSites checks each site's /products.json in parallel and returns
// only those that respond with valid JSON containing at least one product.
func validateSites(urls []string) []string {
	type result struct {
		url   string
		valid bool
	}

	client := &http.Client{Timeout: 8 * time.Second}
	results := make([]result, len(urls))
	var wg sync.WaitGroup

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, siteURL string) {
			defer wg.Done()
			results[idx] = result{url: siteURL}

			resp, err := client.Get(siteURL + "/products.json")
			if err != nil {
				log.Printf("  site validate: %s — fetch error: %v", siteURL, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				log.Printf("  site validate: %s — status %d", siteURL, resp.StatusCode)
				return
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.Contains(ct, "json") {
				log.Printf("  site validate: %s — non-JSON content-type: %s", siteURL, ct)
				return
			}

			// Quick decode to confirm it has products
			var data struct {
				Products []struct {
					Variants []struct {
						ID int64 `json:"id"`
					} `json:"variants"`
				} `json:"products"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				log.Printf("  site validate: %s — JSON decode error: %v", siteURL, err)
				return
			}
			if len(data.Products) == 0 {
				log.Printf("  site validate: %s — no products", siteURL)
				return
			}

			results[idx].valid = true
		}(i, u)
	}

	wg.Wait()

	var valid []string
	for _, r := range results {
		if r.valid {
			valid = append(valid, r.url)
		}
	}
	log.Printf("Site validation: %d/%d passed", len(valid), len(urls))
	return valid
}

// RandomSite picks a random site URL from the list.
func RandomSite(sites []string) string {
	return sites[rand.Intn(len(sites))]
}

// DifferentSite picks a random site that is not the given one.
// Falls back to any random site if only one is available.
func DifferentSite(sites []string, exclude string) string {
	if len(sites) <= 1 {
		return sites[0]
	}
	for i := 0; i < 10; i++ {
		s := sites[rand.Intn(len(sites))]
		if s != exclude {
			return s
		}
	}
	// fallback: pick first that's different
	for _, s := range sites {
		if s != exclude {
			return s
		}
	}
	return sites[0]
}
