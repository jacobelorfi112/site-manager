package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const testCard = "4242424242424242|03|30|000"

// ProxyPool holds working proxies and rotates through them.
type ProxyPool struct {
	mu      sync.RWMutex
	proxies []string
	idx     atomic.Int64
}

func NewProxyPool(db *DB) *ProxyPool {
	p := &ProxyPool{}
	p.Reload(db)
	go func() {
		for range time.Tick(2 * time.Minute) {
			p.Reload(db)
		}
	}()
	return p
}

func (p *ProxyPool) Reload(db *DB) {
	proxies := db.GetWorkingProxies()
	p.mu.Lock()
	p.proxies = proxies
	p.mu.Unlock()
	if len(proxies) > 0 {
		log.Printf("[proxy-pool] Refreshed: %d working proxies", len(proxies))
	}
}

// Get returns the next proxy in round-robin, or "" if none available.
func (p *ProxyPool) Get() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.proxies) == 0 {
		return ""
	}
	idx := int(p.idx.Add(1)-1) % len(p.proxies)
	return p.proxies[idx]
}

// Remove deletes a dead proxy from the DB and local pool.
func (p *ProxyPool) Remove(db *DB, proxyURL string) {
	db.DeleteProxy(proxyURL)
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := p.proxies[:0]
	for _, px := range p.proxies {
		if px != proxyURL {
			filtered = append(filtered, px)
		}
	}
	p.proxies = filtered
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	db, err := NewDB()
	if err != nil {
		log.Fatalf("[main] DB connect failed: %v", err)
	}
	defer db.Close()
	log.Println("[main] Database connected")

	pool := NewProxyPool(db)

	concurrency := 20
	if v := os.Getenv("CHECKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	batchSize := concurrency * 3
	if v := os.Getenv("CHECKER_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			batchSize = n
		}
	}

	port := "8080"
	if v := os.Getenv("PORT"); v != "" {
		port = v
	}

	go StartDashboard(":"+port, db)
	log.Printf("[main] Dashboard listening on :%s", port)

	// Start proxy manager Telegram bot
	go StartProxyBot(db, pool)

	stop := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Println("[main] Shutting down...")
		close(stop)
	}()

	runWorker(db, pool, concurrency, batchSize, stop)
}

func runWorker(db *DB, pool *ProxyPool, concurrency, batchSize int, stop <-chan struct{}) {
	log.Printf("[worker] Starting (concurrency=%d, batchSize=%d)", concurrency, batchSize)

	if n, err := db.ResetStuck(); err == nil && n > 0 {
		log.Printf("[worker] Reset %d stuck sites", n)
	}

	sem := make(chan struct{}, concurrency)
	stuckTicker := time.NewTicker(30 * time.Second)
	defer stuckTicker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-stuckTicker.C:
			if n, err := db.ResetStuck(); err == nil && n > 0 {
				log.Printf("[worker] Reset %d stuck sites", n)
			}
		default:
		}

		sites, err := db.ClaimPending(batchSize)
		if err != nil {
			log.Printf("[worker] ClaimPending error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(sites) == 0 {
			// Nothing pending — short sleep then retry
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
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
				checkSite(db, s, pool)
			}(site)
		}
		wg.Wait()
	}
}

func checkSite(db *DB, site Site, pool *ProxyPool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[checker] PANIC on %s: %v", site.URL, r)
			db.MarkError(site.ID, "PANIC", fmt.Sprintf("%v", r))
		}
	}()

	envProxy := os.Getenv("PROXY_URL")

	// First attempt: no proxy (or env proxy)
	result, err := runCheckoutForCard(site.URL, testCard, envProxy)

	// If Step 0 failed with a retryable error (e.g. 404, geo-block), retry with a pool proxy
	if err != nil && result != nil && result.Retryable && result.StatusCode == "" {
		proxy := pool.Get()
		if proxy != "" {
			log.Printf("[checker] %s -> Step 0 failed, retrying with proxy %s", site.URL, proxy)
			retryResult, retryErr := runCheckoutForCard(site.URL, testCard, proxy)
			if retryErr == nil || (retryResult != nil && retryResult.StatusCode != "") {
				result = retryResult
				err = retryErr
			} else {
				// Proxy couldn't help either — it might be dead, remove it
				log.Printf("[checker] %s -> proxy retry also failed (%v), dropping proxy", site.URL, retryErr)
				pool.Remove(db, proxy)
			}
		}
	}

	if err != nil && result == nil {
		log.Printf("[checker] %s → ERROR (nil result): %v", site.URL, err)
		db.MarkError(site.ID, "INTERNAL_ERROR", err.Error())
		return
	}

	code := ""
	if result != nil {
		code = result.StatusCode
	}

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
