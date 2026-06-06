package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Browser manages a single headless Chrome with many concurrent tabs.
type Browser struct {
	browser *rod.Browser
	tabSem  chan struct{} // limits concurrent tabs
	mu      sync.Mutex
	busy    bool
}

// NewBrowser launches ONE Chrome instance and returns a Browser.
// maxTabs controls how many pages (tabs) can run concurrently.
// On Railway (Linux), uses headless /usr/bin/chromium.
// Locally (Windows/Mac), uses your installed Chrome with a visible window.
func NewBrowser(maxTabs int) *Browser {
	isRailway := os.Getenv("RAILWAY_ENVIRONMENT") != "" || fileExists("/usr/bin/chromium")

	l := launcher.New().
		Headless(isRailway).
		Set("no-sandbox").
		Set("disable-gpu").
		Set("disable-dev-shm-usage").
		Set("disable-blink-features", "AutomationControlled").
		Set("disable-extensions").
		Set("disable-default-apps").
		Set("disable-background-networking").
		Set("disable-sync").
		Set("metrics-recording-only").
		Set("no-first-run").
		Set("disable-breakpad").
		Set("disable-component-update").
		Set("disable-domain-reliability").
		Set("disable-features", "AudioServiceOutOfProcess,IsolateOrigins,site-per-process").
		Set("disable-site-isolation-trials").
		Set("window-size", "1366,768")

	if isRailway {
		l = l.Bin("/usr/bin/chromium")
		fmt.Println("Railway detected — headless chromium")
	} else {
		fmt.Println("Local mode — visible Chrome window")
	}

	u := l.MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()

	// Remove default blank page to start clean
	pages, _ := browser.Pages()
	for _, p := range pages {
		p.Close()
	}

	fmt.Printf("Browser ready (max %d concurrent tabs)\n", maxTabs)

	return &Browser{
		browser: browser,
		tabSem:  make(chan struct{}, maxTabs),
	}
}

// Close shuts down the browser.
func (b *Browser) Close() {
	if b.browser != nil {
		b.browser.Close()
	}
}

// ResultCallback is called for each card result as soon as it's ready.
// May be nil if the caller doesn't need streaming.
type ResultCallback func(CheckResult)

// RunBatch runs all card entries in parallel as tabs in the single browser.
// If onResult is non-nil, it is called for each card the moment it finishes.
func (b *Browser) RunBatch(entries []CardEntry, buyer BuyerInfo, perStoreLimit int, onResult ResultCallback) ([]CheckResult, error) {
	b.mu.Lock()
	if b.busy {
		b.mu.Unlock()
		return nil, fmt.Errorf("batch already in progress")
	}
	b.busy = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.busy = false
		b.mu.Unlock()
	}()

	n := len(entries)
	results := make([]CheckResult, n)

	storeSem := &storeSemaphore{
		limit: perStoreLimit,
		sems:  make(map[string]chan struct{}),
	}

	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		entry.Card.FullName = buyer.FirstName + " " + buyer.LastName

		go func(idx int, e CardEntry) {
			defer wg.Done()

			// Limit concurrent tabs
			b.tabSem <- struct{}{}
			defer func() { <-b.tabSem }()

			// Per-store concurrency
			storeSem.Acquire(e.Store)
			defer storeSem.Release(e.Store)

			// Recover from any panics (Rod Must* methods can panic)
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("[%d/%s] PANIC recovered: %v\n", e.Index, e.Card.Last4, r)
					results[idx] = CheckResult{
						Index:        e.Index,
						CardLast4:    e.Card.Last4,
						Store:        e.Store,
						Status:       "error",
						ErrorCode:    "PANIC",
						ErrorMessage: fmt.Sprintf("%v", r),
					}
					if onResult != nil {
						onResult(results[idx])
					}
				}
			}()

			// Create a new page (tab) in the browser
			page, err := b.browser.Page(proto.TargetCreateTarget{URL: ""})
			if err != nil {
				results[idx] = CheckResult{
					Index:        e.Index,
					CardLast4:    e.Card.Last4,
					Store:        e.Store,
					Status:       "error",
					ErrorCode:    "TAB_CREATE_FAILED",
					ErrorMessage: err.Error(),
				}
				if onResult != nil {
					onResult(results[idx])
				}
				return
			}
			defer page.Close()

			// Per-tab timeout
			page = page.Timeout(180 * time.Second)

			results[idx] = runCheckout(page, e, buyer)
			fmt.Printf("[%d/%s] DONE — %s (%s) in %.1fs\n",
				e.Index, e.Card.Last4, results[idx].Status, results[idx].ErrorCode, results[idx].ElapsedSeconds)

			if onResult != nil {
				onResult(results[idx])
			}
		}(i, entry)
	}

	wg.Wait()
	return results, nil
}

// storeSemaphore limits concurrent access per store domain.
type storeSemaphore struct {
	mu    sync.Mutex
	limit int
	sems  map[string]chan struct{}
}

func (s *storeSemaphore) getSem(store string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.sems[store]; ok {
		return ch
	}
	ch := make(chan struct{}, s.limit)
	s.sems[store] = ch
	return ch
}

func (s *storeSemaphore) Acquire(store string) {
	s.getSem(store) <- struct{}{}
}

func (s *storeSemaphore) Release(store string) {
	<-s.getSem(store)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
