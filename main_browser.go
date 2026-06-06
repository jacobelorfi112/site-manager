//go:build ignore
// +build ignore

// This file is the original standalone browser checkout tool for local Windows use.
// It is excluded from the server build. Run it directly with: go run main_browser.go

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// ---- STORE CONFIG ----
var browserStoreURL = "https://stateline-designs.myshopify.com"
var cardRaw = "4553880085498769|12|26|588" // format: number|MM|YY|CVV

// ---- BUYER DETAILS ----
const (
	buyerEmail       = "jamesanderson2242@gmail.com"
	buyerFirstName   = "JAMES"
	buyerLastName    = "ANDERSON"
	buyerAddress1    = "111-55 166th St"
	buyerAddress2    = "" // apartment, suite, etc. (leave empty if none)
	buyerCity        = "Jamaica"
	buyerState       = "NY"       // state/province code
	buyerStateName   = "New York" // state display name (for custom dropdowns)
	buyerZip         = "11433"
	buyerCountry     = "US"            // country code
	buyerCountryName = "United States" // country display name (for custom dropdowns)
	buyerPhone       = "9175551234"    // phone number (some sites require it)
)

// Parsed card details (from cardRaw)
var (
	cardNumber string
	cardExpMM  string
	cardExpYY  string
	cardCVV    string
	cardName   string
)

func init() {
	parts := strings.SplitN(cardRaw, "|", 4)
	if len(parts) != 4 {
		log.Fatalf("Invalid card format %q — expected number|MM|YY|CVV", cardRaw)
	}
	// Format card number with spaces every 4 digits
	raw := strings.TrimSpace(parts[0])
	var spaced []string
	for i := 0; i < len(raw); i += 4 {
		end := i + 4
		if end > len(raw) {
			end = len(raw)
		}
		spaced = append(spaced, raw[i:end])
	}
	cardNumber = strings.Join(spaced, " ")
	cardExpMM = strings.TrimSpace(parts[1])
	cardExpYY = strings.TrimSpace(parts[2])
	cardCVV = strings.TrimSpace(parts[3])
	cardName = buyerFirstName + " " + buyerLastName
}

// Product data parsed from /products.json
type BrowserVariant struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Price string `json:"price"`
}
type BrowserProduct struct {
	ID       int64            `json:"id"`
	Title    string           `json:"title"`
	Handle   string           `json:"handle"`
	Variants []BrowserVariant `json:"variants"`
}
type BrowserProductsResponse struct {
	Products []BrowserProduct `json:"products"`
}

func main() {
	startTime := time.Now()

	// ───────────────────────────────────────────────────────
	// Launch browser IMMEDIATELY while HTTP work runs in parallel
	// ───────────────────────────────────────────────────────
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.WindowSize(1366, 768),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, ctxCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer ctxCancel()

	ctx, timeoutCancel := context.WithTimeout(ctx, 300*time.Second)
	defer timeoutCancel()

	// Remove navigator.webdriver flag
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		if _, ok := ev.(*target.EventTargetCreated); ok {
			go func() {
				_ = chromedp.Run(ctx,
					chromedp.Evaluate(`Object.defineProperty(navigator, 'webdriver', {get: () => undefined})`, nil),
				)
			}()
		}
	})

	// Force browser to launch now by navigating to about:blank
	fmt.Println("Launching browser...")
	if err := chromedp.Run(ctx, chromedp.Navigate("about:blank")); err != nil {
		log.Fatal("Failed to launch browser:", err)
	}
	fmt.Printf("Browser ready in %.1fs\n", time.Since(startTime).Seconds())

	// ───────────────────────────────────────────────────────
	// Create checkout via HTTP in parallel (browser is already open)
	// ───────────────────────────────────────────────────────
	type checkoutResult struct {
		url       string
		cookies   []*http.Cookie
		variantID int64
	}
	chResult := make(chan checkoutResult, 1)
	go func() {
		u, c, vid := createCheckoutViaHTTP()
		chResult <- checkoutResult{url: u, cookies: c, variantID: vid}
	}()

	// Wait for checkout URL
	result := <-chResult
	checkoutURL := result.url
	cookies := result.cookies
	variantID := result.variantID
	fmt.Printf("\n⚡ Checkout URL ready in %.1fs: %s\n\n", time.Since(startTime).Seconds(), checkoutURL)

	// ───────────────────────────────────────────────────────
	// Inject cookies and navigate to checkout
	// ───────────────────────────────────────────────────────
	fmt.Println("Navigating to checkout...")

	// Extract domain for cookie injection (supports custom domains, not just .myshopify.com)
	parsedStore, _ := url.Parse(browserStoreURL)
	cookieDomain := parsedStore.Hostname()
	// Use parent domain for cookie scope (e.g. "store.com" from "www.store.com")
	parts := strings.Split(cookieDomain, ".")
	if len(parts) > 2 {
		cookieDomain = strings.Join(parts[len(parts)-2:], ".")
	}
	cookieDomain = "." + cookieDomain

	// Inject cookies via CDP before navigating (works on about:blank)
	for _, c := range cookies {
		expr := network.SetCookie(c.Name, c.Value).WithDomain(cookieDomain).WithPath("/")
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return expr.Do(ctx)
		})); err != nil {
			fmt.Printf("Warning: could not set cookie %s: %v\n", c.Name, err)
		}
	}

	// Navigate directly to checkout URL (cookies already set)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(checkoutURL),
	); err != nil {
		log.Fatal("Failed to navigate to checkout:", err)
	}

	// Wait for the checkout form (with short timeout — homepage redirect should be detected quickly)
	fmt.Println("Waiting for checkout form...")
	checkoutReady := false
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := chromedp.Run(waitCtx,
		chromedp.WaitVisible(`input#email, input[autocomplete="email"], input[name="email"]`, chromedp.ByQuery),
	); err == nil {
		checkoutReady = true
	}
	waitCancel()

	// Check if we actually landed on checkout or got redirected to homepage
	var currentURL string
	if err := chromedp.Run(ctx, chromedp.Location(&currentURL)); err == nil {
		fmt.Printf("Checkout URL: %s\n", currentURL)
	}

	if !checkoutReady || (!strings.Contains(currentURL, "/checkouts/") && !strings.Contains(currentURL, "/checkout")) {
		fmt.Println("\n⚠️  Landed on homepage instead of checkout — recreating cart in browser...")
		// Add item to cart via browser navigation
		addURL := fmt.Sprintf("%s/cart/add?id=%d&quantity=1", browserStoreURL, variantID)
		if err := chromedp.Run(ctx,
			chromedp.Navigate(addURL),
			chromedp.Sleep(1500*time.Millisecond),
		); err != nil {
			log.Fatal("Failed to add to cart in browser:", err)
		}
		// Navigate to checkout
		if err := chromedp.Run(ctx,
			chromedp.Navigate(browserStoreURL+"/checkout"),
		); err != nil {
			log.Fatal("Failed to navigate to checkout:", err)
		}
		fmt.Println("Waiting for checkout form (retry)...")
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(`input#email, input[autocomplete="email"], input[name="email"]`, chromedp.ByQuery),
		); err != nil {
			log.Fatal("Checkout form not found on retry:", err)
		}
		if err := chromedp.Run(ctx, chromedp.Location(&currentURL)); err == nil {
			fmt.Printf("Checkout URL (retry): %s\n", currentURL)
		}
	}

	// ───────────────────────────────────────────────────────
	// Step 4: Fill all checkout form fields via JS (fast)
	// ───────────────────────────────────────────────────────
	fmt.Println("Filling checkout form...")

	// Fill all fields via JS + React event dispatch (much faster than per-char typing)
	// Handles all Shopify checkout variants: one-page, multi-step, with/without phone, address2, etc.
	fillJS := fmt.Sprintf(`(()=>{
		function setNativeValue(el, val) {
			const nativeInputValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			nativeInputValueSetter.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles: true}));
			el.dispatchEvent(new Event('change', {bubbles: true}));
			el.dispatchEvent(new Event('blur', {bubbles: true}));
		}
		function fill(sel, val) {
			if (!val) return;
			const el = document.querySelector(sel);
			if (el) { el.focus(); setNativeValue(el, val); }
		}
		function setSelect(sel, val) {
			const el = document.querySelector(sel);
			if (el) {
				// Use native setter to bypass React controlled component
				const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
				nativeSetter.call(el, val);
				el.dispatchEvent(new Event('input', {bubbles: true}));
				el.dispatchEvent(new Event('change', {bubbles: true}));
				el.dispatchEvent(new Event('blur', {bubbles: true}));
				return true;
			}
			return false;
		}
		// For custom dropdowns that aren't native <select> elements
		function setCustomSelect(labelText, val, displayVal) {
			// Find the dropdown trigger by nearby label text
			const labels = document.querySelectorAll('label, legend, .field__label');
			for(const lbl of labels){
				const lt = lbl.textContent.toLowerCase().trim();
				if(!lt.includes(labelText)) continue;
				// Look for a select, button, or [role="combobox"] near this label
				const parent = lbl.closest('.field, .form-group, fieldset, [data-address-field]') || lbl.parentElement;
				if(!parent) continue;
				const sel = parent.querySelector('select');
				if(sel){
					const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
					nativeSetter.call(sel, val);
					sel.dispatchEvent(new Event('input', {bubbles:true}));
					sel.dispatchEvent(new Event('change', {bubbles:true}));
					sel.dispatchEvent(new Event('blur', {bubbles:true}));
					return true;
				}
				// Custom dropdown: click trigger then click option
				const trigger = parent.querySelector('button, [role="combobox"], [role="listbox"]');
				if(trigger){
					trigger.click();
					setTimeout(()=>{
						const opts = document.querySelectorAll('[role="option"], li[data-value]');
						for(const o of opts){
							if(o.dataset.value === val || o.textContent.trim() === displayVal){
								o.click(); return;
							}
						}
					}, 200);
					return true;
				}
			}
			return false;
		}
		const filled = [];
		// Email
		fill('input#email, input[autocomplete="email"], input[name="email"], input[type="email"]', %q);
		filled.push('email');
		// Country (set before address so state dropdown populates)
		if(!setSelect('select[autocomplete="country"], select[name="countryCode"], select[name="country"]', %q)){
			setCustomSelect('country', %q, %q);
		}
		filled.push('country');
		// Name
		fill('input[autocomplete="given-name"], input[name="firstName"], input[name="first_name"]', %q);
		fill('input[autocomplete="family-name"], input[name="lastName"], input[name="last_name"]', %q);
		filled.push('name');
		// Address
		fill('input[autocomplete="address-line1"], input[name="address1"], input[name="address"]', %q);
		fill('input[autocomplete="address-line2"], input[name="address2"]', %q);
		filled.push('address');
		// City
		fill('input[autocomplete="address-level2"], input[name="city"]', %q);
		filled.push('city');
		// State/Province
		if(!setSelect('select[autocomplete="address-level1"], select[name="zone"], select[name="province"]', %q)){
			setCustomSelect('state', %q, %q);
		}
		// Some sites use an input instead of select for state
		fill('input[autocomplete="address-level1"], input[name="zone"], input[name="province"]', %q);
		filled.push('state');
		// ZIP/Postal code
		fill('input[autocomplete="postal-code"], input[name="postalCode"], input[name="zip"]', %q);
		filled.push('zip');
		// Phone (many sites require this)
		fill('input[autocomplete="tel"], input[name="phone"], input[type="tel"]', %q);
		filled.push('phone');
		return 'filled: ' + filled.join(', ');
	})()`, buyerEmail, buyerCountry, buyerCountry, buyerCountryName, buyerFirstName, buyerLastName, buyerAddress1, buyerAddress2, buyerCity, buyerState, buyerState, buyerStateName, buyerState, buyerZip, buyerPhone)

	var fillResult string
	if err := chromedp.Run(ctx, chromedp.Evaluate(fillJS, &fillResult)); err != nil {
		log.Fatal("Failed to fill form via JS:", err)
	}
	fmt.Printf("Form fill: %s\n", fillResult)

	// Dismiss any address autocomplete dropdown
	chromedp.Run(ctx, chromedp.KeyEvent("\x1b"))

	// Close any address suggestion popover by clicking body
	chromedp.Run(ctx, chromedp.Evaluate(`document.body.click()`, nil))

	// Verify country was set — retry via click-based selection if still empty
	time.Sleep(500 * time.Millisecond)

	// Shopify's new checkout uses a custom combobox for country/state that requires
	// physically clicking the dropdown and selecting from the visible list.
	// The native setter approach doesn't update the React UI.
	countryFixJS := fmt.Sprintf(`(()=>{
		const selectors = [
			'select[autocomplete="country"]',
			'select[name="countryCode"]',
			'select[name="country"]',
		];
		function selectOption(el, opt) {
			el.selectedIndex = opt.index;
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		for(const s of selectors){
			const el = document.querySelector(s);
			if(!el) continue;
			// Already set to US?
			if(el.value === '%s') return 'country already set: '+el.value;
			// Try US first
			const usOpt = el.querySelector('option[value="%s"]');
			if(usOpt){
				selectOption(el, usOpt);
				return 'selected via option index: '+usOpt.value;
			}
			// US not available — pick first non-empty option
			const opts = el.querySelectorAll('option');
			for(const o of opts){
				if(o.value && o.value.trim() !== '' && !o.disabled){
					selectOption(el, o);
					return 'selected fallback country: '+o.value+' ('+o.textContent.trim()+')';
				}
			}
		}
		return 'select not found or no matching option';
	})()`, buyerCountry, buyerCountry)
	var countryStatus string
	if err := chromedp.Run(ctx, chromedp.Evaluate(countryFixJS, &countryStatus)); err == nil {
		fmt.Printf("Country check: %s\n", countryStatus)
	}

	// If country still not set, use physical click approach
	if strings.Contains(countryStatus, "not found") || strings.Contains(countryStatus, "not set") {
		fmt.Println("Country not set via JS — trying click approach...")
		selectCountryViaClick(ctx, buyerCountry, buyerCountryName)
	}

	// Give Shopify time to load state dropdown after country change
	time.Sleep(500 * time.Millisecond)

	// Also verify state was set
	stateFixJS := fmt.Sprintf(`(()=>{
		const sel = document.querySelector('select[autocomplete="address-level1"], select[name="zone"], select[name="province"]');
		if(!sel) return 'no state select';
		if(sel.value && sel.value !== '') return 'state ok: '+sel.value;
		const opt = sel.querySelector('option[value="%s"]');
		if(opt){
			sel.selectedIndex = opt.index;
			sel.dispatchEvent(new Event('input', {bubbles:true}));
			sel.dispatchEvent(new Event('change', {bubbles:true}));
			sel.dispatchEvent(new Event('blur', {bubbles:true}));
			return 'state selected via index: '+opt.value;
		}
		return 'state not set';
	})()`, buyerState)
	var stateStatus string
	if err := chromedp.Run(ctx, chromedp.Evaluate(stateFixJS, &stateStatus)); err == nil {
		fmt.Printf("State check: %s\n", stateStatus)
	}

	if strings.Contains(stateStatus, "not set") {
		fmt.Println("State not set via JS — trying click approach...")
		selectCountryViaClick(ctx, buyerState, buyerStateName)
	}

	// Fill empty address2 fields with "Apt" if they exist (some sites require it)
	addr2FillJS := `(()=>{
		function setNativeValue(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		const sels = ['input[autocomplete="address-line2"]','input[name="address2"]'];
		for(const sel of sels){
			const el = document.querySelector(sel);
			if(el && !el.value){ el.focus(); setNativeValue(el, 'Apt'); return 'filled address2'; }
		}
		return 'no empty address2';
	})()`
	var addr2Result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(addr2FillJS, &addr2Result)); err == nil {
		fmt.Printf("Address2 auto-fill: %s\n", addr2Result)
	}

	fmt.Println("Waiting for shipping rates...")
	time.Sleep(1 * time.Second)

	// ───────────────────────────────────────────────────────
	// Step 5: Handle shipping — works for one-page AND multi-step checkouts
	// ───────────────────────────────────────────────────────

	// Try clicking continue/submit buttons up to 3 times to advance through
	// multi-step checkouts (info → shipping → payment)
	for step := 0; step < 3; step++ {
		clicked := clickContinueIfPresent(ctx)
		if !clicked {
			break
		}
		time.Sleep(1500 * time.Millisecond)
	}

	// Check if there are form validation errors (missing required fields)
	// and try to fill them before proceeding
	fixValidationErrors(ctx)
	time.Sleep(1 * time.Second)

	// ───────────────────────────────────────────────────────
	// Step 6: Fill payment details in PCI iframe
	// ───────────────────────────────────────────────────────
	fmt.Println("Filling payment details...")

	if err := fillPaymentIframe(ctx, allocCtx); err != nil {
		log.Fatal("Failed to fill payment:", err)
	}

	// ── Fill billing address if it has separate fields ──
	fmt.Println("Checking for separate billing address fields...")
	billingJS := fmt.Sprintf(`(()=>{
		function setNativeValue(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		function fill(sel, val) {
			if (!val) return false;
			const el = document.querySelector(sel);
			if (el && !el.value && el.offsetParent !== null) { el.focus(); setNativeValue(el, val); return true; }
			return false;
		}
		function setSelect(sel, val) {
			const el = document.querySelector(sel);
			if (el && el.offsetParent !== null) {
				el.value = val;
				el.dispatchEvent(new Event('change', {bubbles:true}));
				return true;
			}
			return false;
		}
		const filled = [];
		// Billing-specific selectors (Shopify uses Section--billingAddress or billing_address prefix)
		const billingCtx = document.querySelector('[data-billing-address], [id*="billing"], .section--billing-address, fieldset[data-address-fields]');
		function qb(sel) {
			if (billingCtx) { const e = billingCtx.querySelector(sel); if (e) return e; }
			return null;
		}
		// Only proceed if there's a visible billing section with empty fields
		const bFirst = qb('input[autocomplete="given-name"], input[name="firstName"]') ||
			qb('input[name*="first_name"], input[name*="firstName"]');
		if (!bFirst || bFirst.value) return 'no separate billing or already filled';
		// Fill billing fields using the billing context
		const fields = billingCtx ? billingCtx.querySelectorAll('input, select') : [];
		fields.forEach(el => {
			const ac = (el.autocomplete||'').toLowerCase();
			const nm = (el.name||'').toLowerCase();
			const tag = el.tagName.toLowerCase();
			if (!el.value || el.value === '') {
				if (ac.includes('given-name') || nm.includes('firstname') || nm.includes('first_name')) {
					setNativeValue(el, %q); filled.push('billing_first');
				} else if (ac.includes('family-name') || nm.includes('lastname') || nm.includes('last_name')) {
					setNativeValue(el, %q); filled.push('billing_last');
				} else if (ac.includes('address-line1') || nm.includes('address1') || nm === 'address') {
					setNativeValue(el, %q); filled.push('billing_addr1');
				} else if (ac.includes('address-line2') || nm.includes('address2')) {
					setNativeValue(el, 'Apt'); filled.push('billing_addr2');
				} else if (ac.includes('address-level2') || nm.includes('city')) {
					setNativeValue(el, %q); filled.push('billing_city');
				} else if (ac.includes('postal-code') || nm.includes('zip') || nm.includes('postalcode')) {
					setNativeValue(el, %q); filled.push('billing_zip');
				} else if (ac.includes('tel') || nm.includes('phone') || el.type === 'tel') {
					setNativeValue(el, %q); filled.push('billing_phone');
				}
			}
			if (tag === 'select') {
				if (ac.includes('country') || nm.includes('country')) {
					el.value = %q; el.dispatchEvent(new Event('change',{bubbles:true})); filled.push('billing_country');
				} else if (ac.includes('address-level1') || nm.includes('zone') || nm.includes('province')) {
					el.value = %q; el.dispatchEvent(new Event('change',{bubbles:true})); filled.push('billing_state');
				}
			}
		});
		return filled.length > 0 ? 'filled: '+filled.join(', ') : 'no billing fields to fill';
	})()`, buyerFirstName, buyerLastName, buyerAddress1, buyerCity, buyerZip, buyerPhone, buyerCountry, buyerState)
	var billingResult string
	if err := chromedp.Run(ctx, chromedp.Evaluate(billingJS, &billingResult)); err == nil {
		fmt.Printf("Billing address: %s\n", billingResult)
	}

	// ───────────────────────────────────────────────────────
	// Step 7: Inject fetch interceptor to capture payment API responses
	// ───────────────────────────────────────────────────────
	injectFetchInterceptor(ctx)

	// ───────────────────────────────────────────────────────
	// Step 8: Click "Pay now" button
	// ───────────────────────────────────────────────────────
	fmt.Println("Clicking Pay now...")
	if err := clickPayButton(ctx); err != nil {
		log.Fatal("Failed to click pay:", err)
	}

	// Quick check for card validation errors before waiting
	time.Sleep(1 * time.Second)
	if cardErr := checkCardValidationError(ctx); cardErr != "" {
		fmt.Println("\n╔══════════════════════════════════════════════════════╗")
		fmt.Println("║              ❌ PAYMENT FAILED                       ║")
		fmt.Println("╚══════════════════════════════════════════════════════╝")
		fmt.Printf("  Error Code    : INCORRECT_NUMBER\n")
		fmt.Printf("  Error Message : %s\n", cardErr)
		fmt.Println("══════════════════════════════════════════════════════")
		fmt.Printf("\nTotal elapsed time: %.1f seconds\n", time.Since(startTime).Seconds())
		return
	}

	// ───────────────────────────────────────────────────────
	// Step 9: Wait for result — success redirect or error
	// ───────────────────────────────────────────────────────
	fmt.Println("Waiting for checkout result...")
	waitForResult(ctx)

	fmt.Printf("\nTotal elapsed time: %.1f seconds\n", time.Since(startTime).Seconds())
}

// fillField finds an input by selector and types the value with realistic timing.
func fillField(ctx context.Context, sel string, value string) error {
	// Click the field to focus
	if err := chromedp.Run(ctx,
		chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible),
		chromedp.Sleep(100*time.Millisecond),
	); err != nil {
		return fmt.Errorf("click %s: %w", sel, err)
	}

	// Select all existing text and delete
	if err := chromedp.Run(ctx,
		chromedp.KeyEvent("a", chromedp.KeyModifiers(input.ModifierCtrl)),
		chromedp.KeyEvent("\b"),
		chromedp.Sleep(50*time.Millisecond),
	); err != nil {
		return fmt.Errorf("clear %s: %w", sel, err)
	}

	// Type value with slight pause between characters (feels human)
	for _, ch := range value {
		if err := chromedp.Run(ctx,
			chromedp.KeyEvent(string(ch)),
		); err != nil {
			return fmt.Errorf("type char in %s: %w", sel, err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	return nil
}

// setCountry tries to select "US" in the country dropdown.
func setCountry(ctx context.Context, code string) {
	// Try common Shopify country selectors
	selectors := []string{
		`select[autocomplete="country"], select[name="countryCode"]`,
	}
	for _, sel := range selectors {
		err := chromedp.Run(ctx,
			chromedp.SetValue(sel, code, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
			// Trigger change event
			chromedp.Evaluate(fmt.Sprintf(
				`(()=>{const e=document.querySelector(%q);if(e){e.dispatchEvent(new Event('change',{bubbles:true}))}})()`,
				sel,
			), nil),
		)
		if err == nil {
			fmt.Println("Set country to", code)
			return
		}
	}
	fmt.Println("Country field not found (may already be US)")
}

// setState tries to select the state/province.
func setState(ctx context.Context, code string) {
	selectors := []string{
		`select[autocomplete="address-level1"], select[name="zone"]`,
	}
	for _, sel := range selectors {
		err := chromedp.Run(ctx,
			chromedp.SetValue(sel, code, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
			chromedp.Evaluate(fmt.Sprintf(
				`(()=>{const e=document.querySelector(%q);if(e){e.dispatchEvent(new Event('change',{bubbles:true}))}})()`,
				sel,
			), nil),
		)
		if err == nil {
			fmt.Println("Set state to", code)
			return
		}
	}
	fmt.Println("State field not found")
}

// clickContinueIfPresent tries to click "Continue" / "Continue to shipping" / "Continue to payment" etc.
// Returns true if a button was clicked.
func clickContinueIfPresent(ctx context.Context) bool {
	js := `(()=>{
		const btns = document.querySelectorAll('button[type="submit"], button, a.step__footer__continue-btn');
		// Only match real navigation buttons, not toggles/accordions
		const patterns = ['continue', 'ship to', 'next step', 'proceed', 'continue to shipping', 'continue to payment'];
		// Skip pay buttons, toggles, info expanders, etc.
		const stopWords = ['pay now', 'complete order', 'place order', 'submit order', 'buy now', 'confirm',
			'additional', 'more', 'expand', 'show', 'toggle', 'select', 'add', 'apply', 'remove', 'change'];
		for(const b of btns){
			const txt = b.textContent.toLowerCase().trim().replace(/\\s+/g, ' ');
			if(txt.length > 60) continue; // skip elements with long text (not real buttons)
			if(stopWords.some(sw => txt.includes(sw))) continue;
			if(patterns.some(kw => txt.includes(kw))){
				b.click();
				return 'clicked: '+txt;
			}
		}
		return 'none';
	})()`
	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err == nil && result != "none" {
		fmt.Printf("Continue button: %s\n", result)
		return true
	}
	return false
}

// selectCountryViaClick physically clicks a <select> dropdown and picks an option from the list.
// This handles Shopify's React-controlled selects where programmatic value setting doesn't work.
func selectCountryViaClick(ctx context.Context, value string, displayName string) {
	// Strategy 1: Find select with desired option, or fall back to first available option
	clickJS := fmt.Sprintf(`(()=>{
		const sels = document.querySelectorAll('select');
		for(const sel of sels){
			if(sel.offsetParent === null) continue;
			const ac = (sel.autocomplete||'').toLowerCase();
			const nm = (sel.name||'').toLowerCase();
			if(ac.includes('country') || nm.includes('country') || nm.includes('countrycode') ||
			   ac.includes('address-level1') || nm.includes('zone') || nm.includes('province')){
				// Try the desired option first
				let opt = sel.querySelector('option[value="%s"]');
				// If not found, pick first non-empty option
				if(!opt){
					const allOpts = sel.querySelectorAll('option');
					for(const o of allOpts){
						if(o.value && o.value.trim() !== '' && !o.disabled){ opt = o; break; }
					}
				}
				if(!opt) continue;
				sel.focus();
				sel.size = 5;
				opt.selected = true;
				sel.size = 1;
				sel.dispatchEvent(new Event('input', {bubbles:true}));
				sel.dispatchEvent(new Event('change', {bubbles:true}));
				sel.dispatchEvent(new Event('blur', {bubbles:true}));
				return 'click-selected: '+opt.value;
			}
		}
		return 'no matching select';
	})()`, value)
	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(clickJS, &result)); err == nil {
		fmt.Printf("  Click select: %s\n", result)
		if !strings.Contains(result, "no matching") {
			return
		}
	}

	// Strategy 2: Use chromedp to click the select element directly and send key events
	// This works for native <select> — clicking focuses it, then typing the display name
	// causes the browser's native select search to jump to the matching option
	selectors := []string{
		`select[autocomplete="country"]`,
		`select[name="countryCode"]`,
		`select[name="country"]`,
		`select[autocomplete="address-level1"]`,
		`select[name="zone"]`,
		`select[name="province"]`,
	}
	for _, sel := range selectors {
		err := chromedp.Run(ctx,
			chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible),
			chromedp.Sleep(200*time.Millisecond),
		)
		if err != nil {
			continue
		}
		// Type the first few chars of the display name to trigger browser's native search
		prefix := displayName
		if len(prefix) > 6 {
			prefix = prefix[:6]
		}
		for _, ch := range prefix {
			chromedp.Run(ctx, chromedp.KeyEvent(string(ch)))
			time.Sleep(50 * time.Millisecond)
		}
		// Press Enter to confirm selection
		time.Sleep(200 * time.Millisecond)
		chromedp.Run(ctx, chromedp.KeyEvent("\r"))
		time.Sleep(200 * time.Millisecond)
		fmt.Printf("  Typed '%s' in select dropdown\n", prefix)
		return
	}
	fmt.Println("  Could not find select to click")
}

// fixValidationErrors checks for validation error highlights and tries to fill missing required fields.
func fixValidationErrors(ctx context.Context) {
	js := fmt.Sprintf(`(()=>{
		function setNativeValue(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		const fixed = [];
		// Check all inputs/selects that have error styling or aria-invalid
		document.querySelectorAll('input[aria-invalid="true"], input:invalid, .field--error input, .field--error select').forEach(el => {
			const name = (el.name||'').toLowerCase();
			const ac = (el.autocomplete||'').toLowerCase();
			const type = (el.type||'').toLowerCase();
			if ((name.includes('phone') || ac.includes('tel') || type === 'tel') && !el.value) {
				setNativeValue(el, %q); fixed.push('phone');
			}
			if ((name.includes('address2') || ac.includes('address-line2')) && !el.value) {
				setNativeValue(el, ' '); fixed.push('address2');
			}
		});
		return fixed.length > 0 ? 'fixed: '+fixed.join(', ') : 'none';
	})()`, buyerPhone)
	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err == nil && result != "none" {
		fmt.Printf("Validation fix: %s\n", result)
	}
}

// fillPaymentIframe fills payment card details by finding the PCI iframe's frame context
// and focusing the input directly, then using keyboard events.
func fillPaymentIframe(ctx context.Context, allocCtx context.Context) error {
	// Wait for the payment iframe to appear in the DOM
	fmt.Println("  Waiting for payment iframe...")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`iframe[id*="number"], iframe[src*="number-ltr"]`, chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("payment iframe not found: %w", err)
	}

	// Scroll the payment iframe into view — it may be below the fold
	chromedp.Run(ctx, chromedp.Evaluate(`
		(()=>{
			const iframe = document.querySelector('iframe[id*="number"], iframe[src*="number-ltr"]');
			if(iframe) iframe.scrollIntoView({behavior:'instant', block:'center'});
		})()
	`, nil))
	time.Sleep(500 * time.Millisecond)

	// Strategy: Find the card-number iframe's frame ID via CDP, then focus the input
	// inside it directly. This is more reliable than clicking the iframe from the parent.
	focused := false
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		// Get the frame tree to find card-number iframe
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		var cardFrameID cdp.FrameID
		var allFrameURLs []string
		var findFrame func(ft *page.FrameTree)
		findFrame = func(ft *page.FrameTree) {
			if ft.Frame != nil {
				frameURL := ft.Frame.URL
				allFrameURLs = append(allFrameURLs, frameURL)
				// Match various Shopify PCI iframe URL patterns for card number
				lower := strings.ToLower(frameURL)
				isCardNumber := (strings.Contains(lower, "card-fields") && strings.Contains(lower, "number")) ||
					(strings.Contains(lower, "number") && strings.Contains(lower, "ltr")) ||
					(strings.Contains(lower, "card") && strings.Contains(lower, "number") && strings.Contains(lower, "iframe")) ||
					(strings.Contains(lower, "payment") && strings.Contains(lower, "number")) ||
					(strings.Contains(lower, "credit-card") && strings.Contains(lower, "number")) ||
					(strings.Contains(lower, "shopifycs") && strings.Contains(lower, "number"))
				if isCardNumber && cardFrameID == "" {
					cardFrameID = ft.Frame.ID
					return
				}
			}
			for _, child := range ft.ChildFrames {
				if cardFrameID != "" {
					return
				}
				findFrame(child)
			}
		}
		findFrame(tree)
		if cardFrameID == "" {
			// Log all frame URLs to help debug
			fmt.Printf("  Frame URLs found: %v\n", allFrameURLs)
			return fmt.Errorf("card number frame not found in frame tree")
		}
		fmt.Printf("  Found card number frame: %s\n", cardFrameID)

		// Create isolated world in the card-number frame to focus the input
		worldID, err := page.CreateIsolatedWorld(cardFrameID).Do(ctx)
		if err != nil {
			return fmt.Errorf("creating isolated world: %w", err)
		}

		// Focus the input inside the card-number iframe
		_, exDetail, err := runtime.Evaluate(`
			(()=>{
				const inp = document.querySelector('input[name="number"], input[type="text"], input[autocomplete="cc-number"], input');
				if(inp){ inp.focus(); inp.click(); return 'focused: '+inp.name; }
				return 'no input found';
			})()
		`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
		if err != nil {
			return fmt.Errorf("focusing card input: %w", err)
		}
		if exDetail != nil {
			return fmt.Errorf("focus exception: %v", exDetail)
		}
		focused = true
		fmt.Println("  Focused card input via frame context")
		return nil
	})); err != nil {
		fmt.Printf("  Frame context approach failed: %v — falling back to mouse click\n", err)
	}

	// Fallback: use real mouse coordinates to click INTO the iframe (not just on the container)
	// chromedp.Click only focuses the iframe element in the parent DOM — keystrokes don't reach inside.
	// We need input.DispatchMouseEvent at the iframe's pixel coordinates so the browser routes the
	// click into the iframe and focuses the actual input.
	if !focused {
		fmt.Println("  Focusing card number field via mouse coordinates...")
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			getBounds := func() (float64, float64, error) {
				var boundsJSON string
				boundsJS := `
					(()=>{
						const iframe = document.querySelector('iframe[id*="number"], iframe[src*="number-ltr"]');
						if(!iframe) return JSON.stringify([]);
						const r = iframe.getBoundingClientRect();
						return JSON.stringify([r.x, r.y, r.width, r.height]);
					})()`
				if err := chromedp.Evaluate(boundsJS, &boundsJSON).Do(ctx); err != nil {
					return 0, 0, err
				}
				var bounds []float64
				if err := json.Unmarshal([]byte(boundsJSON), &bounds); err != nil || len(bounds) < 4 {
					return 0, 0, fmt.Errorf("parsing iframe bounds: %v", boundsJSON)
				}
				return bounds[0] + bounds[2]/2, bounds[1] + bounds[3]/2, nil
			}

			// Scroll iframe into view and wait for layout
			chromedp.Evaluate(`
				(()=>{
					const iframe = document.querySelector('iframe[id*="number"], iframe[src*="number-ltr"]');
					if(iframe) iframe.scrollIntoView({behavior:'instant', block:'center'});
				})()
			`, nil).Do(ctx)
			time.Sleep(300 * time.Millisecond)

			cx, cy, err := getBounds()
			if err != nil {
				return fmt.Errorf("getting iframe bounds: %w", err)
			}
			fmt.Printf("  Iframe center after scroll: (%.0f, %.0f)\n", cx, cy)

			// If iframe is still above the viewport, force-scroll the window
			if cy < 50 {
				scrollAmount := 50 - cy + 200 // push it well into the viewport
				chromedp.Evaluate(fmt.Sprintf(`window.scrollBy(0, %f)`, scrollAmount), nil).Do(ctx)
				time.Sleep(300 * time.Millisecond)
				cx, cy, err = getBounds()
				if err != nil {
					return fmt.Errorf("getting iframe bounds after scroll: %w", err)
				}
				fmt.Printf("  Iframe center after forced scroll: (%.0f, %.0f)\n", cx, cy)
			}

			// Dispatch a real mouse click at the center of the iframe
			if err := input.DispatchMouseEvent(input.MousePressed, cx, cy).
				WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
				return err
			}
			if err := input.DispatchMouseEvent(input.MouseReleased, cx, cy).
				WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
				return err
			}
			return nil
		})); err != nil {
			return fmt.Errorf("mouse click on card iframe: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	} else {
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for the card number input to become interactive (it can be locked initially)
	fmt.Println("  Waiting for card field to unlock...")
	for attempt := 0; attempt < 20; attempt++ { // up to ~5 seconds
		var ready string
		chromedp.Run(ctx, chromedp.Evaluate(`
			(()=>{
				// Check via the iframe element's aria/class attributes
				const iframe = document.querySelector('iframe[id*="number"], iframe[src*="number-ltr"]');
				if(!iframe) return 'no-iframe';
				const parent = iframe.closest('.card-fields-container, [data-card-fields], [class*="card"]');
				// Check if there's a loading overlay or locked state
				if(parent){
					const loading = parent.querySelector('.loading, [aria-busy="true"], .spinner');
					if(loading && loading.offsetParent !== null) return 'loading';
				}
				// Check if iframe itself is interactive (has reasonable dimensions and not hidden)
				const r = iframe.getBoundingClientRect();
				if(r.height < 10) return 'collapsed';
				return 'ready';
			})()`, &ready))
		if ready == "ready" || ready == "no-iframe" {
			break
		}
		fmt.Printf("  Card field state: %s (attempt %d)...\n", ready, attempt+1)
		time.Sleep(250 * time.Millisecond)
	}
	// Additional settle time after unlock
	time.Sleep(300 * time.Millisecond)

	// Click/focus again after unlock to make sure input is focused
	if focused {
		// Re-focus via frame context
		chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			var cardFrameID cdp.FrameID
			var findFrame func(ft *page.FrameTree)
			findFrame = func(ft *page.FrameTree) {
				if ft.Frame != nil {
					lower := strings.ToLower(ft.Frame.URL)
					isCard := (strings.Contains(lower, "card-fields") && strings.Contains(lower, "number")) ||
						(strings.Contains(lower, "number") && strings.Contains(lower, "ltr")) ||
						(strings.Contains(lower, "payment") && strings.Contains(lower, "number"))
					if isCard && cardFrameID == "" {
						cardFrameID = ft.Frame.ID
					}
				}
				for _, child := range ft.ChildFrames {
					if cardFrameID == "" {
						findFrame(child)
					}
				}
			}
			findFrame(tree)
			if cardFrameID == "" {
				return nil
			}
			worldID, err := page.CreateIsolatedWorld(cardFrameID).Do(ctx)
			if err != nil {
				return nil
			}
			runtime.Evaluate(`
				(()=>{
					const inp = document.querySelector('input[name="number"], input[type="text"], input[autocomplete="cc-number"], input');
					if(inp){ inp.focus(); inp.click(); }
				})()
			`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
			return nil
		}))
	}
	time.Sleep(200 * time.Millisecond)

	// Type card number
	typeCardNumber := func() {
		fmt.Println("  Typing card number...")
		for _, ch := range cardNumber {
			chromedp.Run(ctx, chromedp.KeyEvent(string(ch)))
			time.Sleep(15 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
	}
	typeCardNumber()

	// Verify card number was actually typed by checking the iframe input value
	// If it's empty, the focus didn't work — try alternative: use chromedp.SendKeys on the iframe node
	var cardVerify string
	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return nil
		}
		// Try all frames to find one with a card input that has a value
		var checkFrame func(ft *page.FrameTree)
		checkFrame = func(ft *page.FrameTree) {
			if ft.Frame != nil && cardVerify == "" {
				worldID, err := page.CreateIsolatedWorld(ft.Frame.ID).Do(ctx)
				if err == nil {
					res, _, err := runtime.Evaluate(`
						(()=>{
							const inp = document.querySelector('input[name="number"], input[autocomplete="cc-number"], input[id*="number"]');
							if(inp) return inp.value;
							return '';
						})()
					`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
					if err == nil && res != nil && res.Value != nil {
						val := strings.Trim(string(res.Value), `"`)
						if len(val) > 0 {
							cardVerify = val
						}
					}
				}
			}
			for _, child := range ft.ChildFrames {
				if cardVerify == "" {
					checkFrame(child)
				}
			}
		}
		checkFrame(tree)
		return nil
	}))

	if len(cardVerify) == 0 {
		// Check again after a short delay — value may not have propagated yet
		fmt.Println("  Card number check 1: empty — retrying verification...")
		time.Sleep(500 * time.Millisecond)
		cardVerify = ""
		chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return nil
			}
			var checkFrame func(ft *page.FrameTree)
			checkFrame = func(ft *page.FrameTree) {
				if ft.Frame != nil && cardVerify == "" {
					worldID, err := page.CreateIsolatedWorld(ft.Frame.ID).Do(ctx)
					if err == nil {
						res, _, err := runtime.Evaluate(`
							(()=>{
								const inp = document.querySelector('input[name="number"], input[autocomplete="cc-number"], input[id*="number"]');
								if(inp) return inp.value;
								return '';
							})()
						`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
						if err == nil && res != nil && res.Value != nil {
							val := strings.Trim(string(res.Value), `"`)
							if len(val) > 0 {
								cardVerify = val
							}
						}
					}
				}
				for _, child := range ft.ChildFrames {
					if cardVerify == "" {
						checkFrame(child)
					}
				}
			}
			checkFrame(tree)
			return nil
		}))
		if len(cardVerify) > 0 {
			fmt.Printf("  Card number check 2: OK (%s)\n", cardVerify)
		}
	}

	if len(cardVerify) == 0 {
		fmt.Println("  ⚠ Card number confirmed empty after 2 checks. Retrying with click+focus...")
		// Clear any partial content first, then retype
		chromedp.Run(ctx, chromedp.KeyEvent("a", chromedp.KeyModifiers(input.ModifierCtrl)))
		chromedp.Run(ctx, chromedp.KeyEvent("\b"))
		time.Sleep(100 * time.Millisecond)
		// Try clicking the iframe more aggressively: triple-click to ensure focus, then retype
		chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			// First, try clicking input inside iframe using chromedp.Click (focuses container)
			// Then use dispatchKeyEvent which goes to the focused element in the browser
			chromedp.Click(`iframe[id*="number"], iframe[src*="number-ltr"]`, chromedp.ByQuery).Do(ctx)
			time.Sleep(200 * time.Millisecond)

			// Try to get the iframe's content document via CDP and focus its input
			var nodes []*cdp.Node
			if err := chromedp.Nodes(`iframe[id*="number"], iframe[src*="number-ltr"]`, &nodes, chromedp.ByQuery).Do(ctx); err == nil && len(nodes) > 0 {
				iframeNode := nodes[0]
				if iframeNode.ContentDocument != nil && iframeNode.ContentDocument.Children != nil {
					// The content document is accessible — find and focus the input
					fmt.Println("  Found iframe content document, searching for input...")
					var findInput func(n *cdp.Node) *cdp.Node
					findInput = func(n *cdp.Node) *cdp.Node {
						if n.NodeName == "INPUT" {
							return n
						}
						for _, child := range n.Children {
							if found := findInput(child); found != nil {
								return found
							}
						}
						return nil
					}
					if inp := findInput(iframeNode.ContentDocument); inp != nil {
						fmt.Printf("  Found input node ID: %d\n", inp.NodeID)
						chromedp.Run(ctx, chromedp.MouseClickNode(inp))
						time.Sleep(200 * time.Millisecond)
					}
				}
			}
			return nil
		}))
		time.Sleep(200 * time.Millisecond)
		// Clear any existing content before retyping
		chromedp.Run(ctx, chromedp.KeyEvent("a", chromedp.KeyModifiers(input.ModifierCtrl)))
		chromedp.Run(ctx, chromedp.KeyEvent("\b"))
		time.Sleep(100 * time.Millisecond)
		typeCardNumber()
	}

	// Tab to expiry
	fmt.Println("  Tabbing to expiry...")
	if err := chromedp.Run(ctx, chromedp.KeyEvent("\t")); err != nil {
		return fmt.Errorf("tab to expiry: %w", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Type expiry
	fmt.Println("  Typing expiry...")
	expiry := cardExpMM + cardExpYY
	for _, ch := range expiry {
		if err := chromedp.Run(ctx, chromedp.KeyEvent(string(ch))); err != nil {
			return fmt.Errorf("typing expiry: %w", err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	// Tab to CVV
	fmt.Println("  Tabbing to CVV...")
	if err := chromedp.Run(ctx, chromedp.KeyEvent("\t")); err != nil {
		return fmt.Errorf("tab to cvv: %w", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Type CVV
	fmt.Println("  Typing CVV...")
	for _, ch := range cardCVV {
		if err := chromedp.Run(ctx, chromedp.KeyEvent(string(ch))); err != nil {
			return fmt.Errorf("typing cvv: %w", err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	// Fill "Name on card" — find and focus the input, then type via KeyEvent
	// (React-controlled inputs ignore JS .value changes, but KeyEvent always works)
	fmt.Println("  Filling name on card...")

	// Find and focus the name input on the main page, select any existing text
	var nameFocused string
	chromedp.Run(ctx, chromedp.Evaluate(`
		(()=>{
			const sels = [
				'input[autocomplete="cc-name"]',
				'input[placeholder*="name on card" i]',
				'input[placeholder*="cardholder" i]',
				'input[name*="cardName" i]',
				'input[name*="nameoncard" i]',
				'input[id*="name" i][id*="card" i]',
				'input[aria-label*="name on card" i]',
				'input[data-current-field="name"]',
				'input[autocomplete="ccname"]',
				'input[name="name"]'
			];
			function focusAndSelect(inp) {
				inp.scrollIntoView({behavior:'instant', block:'center'});
				inp.focus();
				inp.click();
				try { inp.select(); } catch(e){}
				try { inp.setSelectionRange(0, 99999); } catch(e){}
				try { document.execCommand('selectAll'); } catch(e){}
			}
			// Label-based lookup first
			for(const label of document.querySelectorAll('label')){
				try {
					const lt = label.textContent.toLowerCase();
					if(lt.includes('name on card') || lt.includes('cardholder')){
						const forId = label.getAttribute('for');
						const inp = forId ? document.getElementById(forId) : label.querySelector('input');
						if(inp){
							focusAndSelect(inp);
							return 'focused via label: '+lt.trim();
						}
					}
				} catch(e){}
			}
			for(const sel of sels){
				try {
					const el = document.querySelector(sel);
					if(el){
						focusAndSelect(el);
						return 'focused: '+sel;
					}
				} catch(e){}
			}
			return 'not found';
		})()
	`, &nameFocused))
	fmt.Printf("  Name on card: %s\n", nameFocused)

	if strings.HasPrefix(nameFocused, "focused") {
		// Triple-click to select all text (most reliable cross-browser select-all)
		time.Sleep(150 * time.Millisecond)
		chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			// Get coordinates of the focused name input
			var boundsJSON string
			chromedp.Evaluate(`
				(()=>{
					const el = document.activeElement;
					if(!el) return JSON.stringify([]);
					const r = el.getBoundingClientRect();
					return JSON.stringify([r.x + r.width/2, r.y + r.height/2]);
				})()
			`, &boundsJSON).Do(ctx)
			var coords []float64
			if err := json.Unmarshal([]byte(boundsJSON), &coords); err == nil && len(coords) == 2 {
				cx, cy := coords[0], coords[1]
				// Triple-click = clickCount:3 — selects entire field content
				input.DispatchMouseEvent(input.MousePressed, cx, cy).
					WithButton(input.Left).WithClickCount(3).Do(ctx)
				input.DispatchMouseEvent(input.MouseReleased, cx, cy).
					WithButton(input.Left).WithClickCount(3).Do(ctx)
			}
			return nil
		}))
		time.Sleep(100 * time.Millisecond)
		for _, ch := range cardName {
			chromedp.Run(ctx, chromedp.KeyEvent(string(ch)))
			time.Sleep(15 * time.Millisecond)
		}
		fmt.Println("  Name on card: typed via keyboard.")
	} else {
		// Field not on main page — try iframe fallback
		fmt.Println("  Trying iframe fallback for name field...")
		chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return nil
			}
			var nameFrameID cdp.FrameID
			var findFrame func(ft *page.FrameTree)
			findFrame = func(ft *page.FrameTree) {
				if ft.Frame != nil && nameFrameID == "" {
					lower := strings.ToLower(ft.Frame.URL)
					if (strings.Contains(lower, "card-fields") || strings.Contains(lower, "card")) &&
						strings.Contains(lower, "name") {
						nameFrameID = ft.Frame.ID
					}
				}
				for _, child := range ft.ChildFrames {
					if nameFrameID == "" {
						findFrame(child)
					}
				}
			}
			findFrame(tree)
			if nameFrameID != "" {
				fmt.Println("  Found name iframe in frame tree, focusing input...")
				worldID, err := page.CreateIsolatedWorld(nameFrameID).Do(ctx)
				if err != nil {
					return nil
				}
				runtime.Evaluate(`
					(()=>{
						const inp = document.querySelector('input[name="name"], input[autocomplete="cc-name"], input[type="text"], input');
						if(inp){ inp.focus(); inp.click(); }
					})()
				`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
				return nil
			}
			return nil
		}))
		time.Sleep(200 * time.Millisecond)
		for _, ch := range cardName {
			chromedp.Run(ctx, chromedp.KeyEvent(string(ch)))
			time.Sleep(15 * time.Millisecond)
		}
		fmt.Println("  Name on card: typed via keyboard (iframe fallback).")
	}

	fmt.Println("  Payment details filled.")
	return nil
}

// fillPCITarget is no longer used since we use click+tab approach instead.

// clickPayButton finds and clicks the pay/submit button.
// Handles many Shopify button labels: Pay now, Complete order, Place order, Submit order, Buy now, etc.
func clickPayButton(ctx context.Context) error {
	js := `(()=>{
		const btns = document.querySelectorAll('button[type="submit"], button, input[type="submit"]');
		const payKeywords = ['pay now', 'complete order', 'place order', 'submit order', 'buy now',
			'confirm order', 'place your order', 'checkout', 'finalize'];
		const stopWords = ['additional', 'method', 'more', 'expand', 'toggle', 'select', 'add tip',
			'apply', 'remove', 'change', 'shop pay', 'paypal', 'gpay', 'apple'];
		for(const b of btns){
			const txt = b.textContent.toLowerCase().trim().replace(/\s+/g, ' ');
			if(txt.length > 50) continue;
			if(stopWords.some(sw => txt.includes(sw))) continue;
			if(payKeywords.some(kw => txt.includes(kw))){
				b.click();
				return 'clicked: '+txt;
			}
		}
		// Try "pay" separately with stricter matching (exact or at start)
		for(const b of btns){
			const txt = b.textContent.toLowerCase().trim().replace(/\s+/g, ' ');
			if(txt.length > 50) continue;
			if(stopWords.some(sw => txt.includes(sw))) continue;
			if(txt === 'pay' || txt.startsWith('pay ') || txt.includes('pay $') || txt.includes('pay €')){
				b.click();
				return 'clicked: '+txt;
			}
		}
		// Fallback: click the last submit button on the page
		const submits = document.querySelectorAll('button[type="submit"]');
		if(submits.length > 0){
			submits[submits.length-1].click();
			return 'clicked last submit';
		}
		return 'no pay button found';
	})()`

	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result)); err != nil {
		return err
	}
	fmt.Printf("Pay button: %s\n", result)
	return nil
}

// injectFetchInterceptor is now a no-op; we use Performance API + re-fetch instead.
func injectFetchInterceptor(ctx context.Context) {
	fmt.Println("Payment response capture: using Performance API re-fetch approach.")
}

// readCapturedResponses reads payment API responses - re-fetches PollForReceipt from Performance API.
func readCapturedResponses(ctx context.Context) {
	// Get PollForReceipt URLs from Performance API and re-fetch them
	js := `
		(async () => {
			const entries = performance.getEntriesByType('resource')
				.filter(e => e.name.includes('PollForReceipt') || e.name.includes('SubmitForCompletion'));
			const results = [];
			for (const entry of entries) {
				try {
					const resp = await fetch(entry.name, {
						headers: {'Accept': 'application/json', 'Content-Type': 'application/json'}
					});
					const body = await resp.text();
					results.push({url: entry.name.substring(0, 200), status: resp.status, body: body});
				} catch(e) {
					results.push({url: entry.name.substring(0, 200), error: e.message});
				}
			}
			return JSON.stringify(results);
		})()
	`

	var result string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		fmt.Println("Could not fetch payment API responses:", err)
		return
	}

	if result == "" || result == "[]" {
		fmt.Println("No PollForReceipt/SubmitForCompletion entries found.")
		return
	}

	var responses []map[string]interface{}
	if err := json.Unmarshal([]byte(result), &responses); err != nil {
		fmt.Printf("Raw responses: %s\n", result)
		return
	}

	fmt.Printf("\n=== %d Payment API Response(s) ===\n", len(responses))
	for i, r := range responses {
		fmt.Printf("\n--- Response %d ---\n", i+1)
		urlStr, _ := r["url"].(string)
		// Extract operation name from URL
		if idx := strings.Index(urlStr, "operationName="); idx >= 0 {
			end := strings.Index(urlStr[idx:], "&")
			if end > 0 {
				fmt.Printf("  Operation: %s\n", urlStr[idx+14:idx+end])
			} else {
				fmt.Printf("  Operation: %s\n", urlStr[idx+14:])
			}
		}
		fmt.Printf("  Status: %v\n", r["status"])
		if errMsg, ok := r["error"]; ok {
			fmt.Printf("  Error: %v\n", errMsg)
		}
		if body, ok := r["body"]; ok {
			bodyStr := fmt.Sprintf("%v", body)
			if strings.Contains(bodyStr, "CompletePaymentChallenge") {
				fmt.Println("  ⚠️  3DS Challenge Required")
			}
			fmt.Printf("  Body: %v\n", body)
		}
	}

	// Parse PollForReceipt responses for order summary
	for _, r := range responses {
		body, ok := r["body"]
		if !ok {
			continue
		}
		bodyStr := fmt.Sprintf("%v", body)
		if !strings.Contains(bodyStr, "PollForReceipt") && !strings.Contains(bodyStr, "ProcessedReceipt") {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(bodyStr), &parsed); err != nil {
			continue
		}
		data, _ := parsed["data"].(map[string]interface{})
		if data == nil {
			continue
		}
		receipt, _ := data["receipt"].(map[string]interface{})
		if receipt == nil {
			continue
		}
		// Check order creation status
		orderStatus, _ := receipt["orderCreationStatus"].(map[string]interface{})
		statusType := ""
		if orderStatus != nil {
			statusType, _ = orderStatus["__typename"].(string)
		}

		if statusType != "OrderCreationSucceeded" {
			// Detect 3DS challenge
			is3DS := strings.Contains(bodyStr, "CompletePaymentChallenge") ||
				strings.Contains(bodyStr, "3ds") ||
				strings.Contains(bodyStr, "3DS") ||
				strings.Contains(bodyStr, "THREE_D_SECURE") ||
				strings.Contains(bodyStr, "ThreeDSecure") ||
				strings.Contains(bodyStr, "buyerActionRequired")

			if is3DS {
				fmt.Println("\n╔══════════════════════════════════════════════════════╗")
				fmt.Println("║              ⚠️  3DS CHALLENGE REQUIRED              ║")
				fmt.Println("╚══════════════════════════════════════════════════════╝")
			} else {
				fmt.Println("\n╔══════════════════════════════════════════════════════╗")
				fmt.Println("║              ❌ PAYMENT FAILED                       ║")
				fmt.Println("╚══════════════════════════════════════════════════════╝")
			}
			if statusType != "" {
				fmt.Printf("  Status        : %s\n", statusType)
			}
			extractErrorCodes(bodyStr, receipt)
			fmt.Println("══════════════════════════════════════════════════════")
			continue
		}

		fmt.Println("\n╔══════════════════════════════════════════════════════╗")
		fmt.Println("║              ✅ ORDER PLACED SUCCESSFULLY            ║")
		fmt.Println("╚══════════════════════════════════════════════════════╝")

		// Order number
		if identity, ok := receipt["orderIdentity"].(map[string]interface{}); ok {
			if buyerID, ok := identity["buyerIdentifier"].(string); ok && buyerID != "" {
				fmt.Printf("  Order Number  : %s\n", buyerID)
			}
		}

		// Product details
		if po, ok := receipt["purchaseOrder"].(map[string]interface{}); ok {
			if merch, ok := po["merchandise"].(map[string]interface{}); ok {
				if lines, ok := merch["merchandiseLines"].([]interface{}); ok {
					for _, line := range lines {
						ml, _ := line.(map[string]interface{})
						if ml == nil {
							continue
						}
						qty := 0
						if qObj, ok := ml["quantity"].(map[string]interface{}); ok {
							if items, ok := qObj["items"].(float64); ok {
								qty = int(items)
							}
						}
						if m, ok := ml["merchandise"].(map[string]interface{}); ok {
							title, _ := m["productTitle"].(string)
							variant, _ := m["title"].(string)
							price := ""
							if p, ok := m["price"].(map[string]interface{}); ok {
								amt, _ := p["amount"].(string)
								cur, _ := p["currencyCode"].(string)
								price = amt + " " + cur
							}
							fmt.Printf("  Product       : %s", title)
							if variant != "" && variant != title {
								fmt.Printf(" (%s)", variant)
							}
							fmt.Println()
							if qty > 0 {
								fmt.Printf("  Quantity      : %d\n", qty)
							}
							if price != "" {
								fmt.Printf("  Item Price    : %s\n", price)
							}
						}
					}
				}
			}

			// Shipping
			if delivery, ok := po["delivery"].(map[string]interface{}); ok {
				if dLines, ok := delivery["deliveryLines"].([]interface{}); ok && len(dLines) > 0 {
					dl, _ := dLines[0].(map[string]interface{})
					if dl != nil {
						if strat, ok := dl["deliveryStrategy"].(map[string]interface{}); ok {
							shippingTitle, _ := strat["title"].(string)
							if shippingTitle != "" {
								fmt.Printf("  Shipping      : %s\n", shippingTitle)
							}
						}
						if shipAmt, ok := dl["lineAmount"].(map[string]interface{}); ok {
							amt, _ := shipAmt["amount"].(string)
							cur, _ := shipAmt["currencyCode"].(string)
							if amt != "" {
								fmt.Printf("  Shipping Cost : %s %s\n", amt, cur)
							}
						}
						if addr, ok := dl["destinationAddress"].(map[string]interface{}); ok {
							name, _ := addr["firstName"].(string)
							last, _ := addr["lastName"].(string)
							a1, _ := addr["address1"].(string)
							a2, _ := addr["address2"].(string)
							city, _ := addr["city"].(string)
							zone, _ := addr["zoneCode"].(string)
							zip, _ := addr["postalCode"].(string)
							country, _ := addr["countryCode"].(string)
							addrLine := a1
							if a2 != "" {
								addrLine += ", " + a2
							}
							fmt.Printf("  Ship To       : %s %s, %s, %s %s %s, %s\n",
								name, last, addrLine, city, zone, zip, country)
						}
					}
				}
			}

			// Total
			if total, ok := po["checkoutTotal"].(map[string]interface{}); ok {
				amt, _ := total["amount"].(string)
				cur, _ := total["currencyCode"].(string)
				if amt != "" {
					fmt.Printf("  Total Charged : %s %s\n", amt, cur)
				}
			}
		}

		// Payment details
		if pd, ok := receipt["paymentDetails"].(map[string]interface{}); ok {
			brand, _ := pd["paymentCardBrand"].(string)
			last4, _ := pd["creditCardLastFourDigits"].(string)
			gateway, _ := pd["paymentGateway"].(string)
			if brand != "" && last4 != "" {
				fmt.Printf("  Payment       : %s ending in %s\n", brand, last4)
			}
			if gateway != "" {
				fmt.Printf("  Gateway       : %s\n", gateway)
			}
		}

		// Order status page URL
		if statusURL, ok := receipt["orderStatusPageUrl"].(string); ok && statusURL != "" {
			// Truncate for display
			display := statusURL
			if len(display) > 120 {
				display = display[:120] + "..."
			}
			fmt.Printf("  Order Status  : %s\n", display)
		}

		// Customer info
		if custID, ok := receipt["customerId"].(string); ok && custID != "" {
			fmt.Printf("  Customer ID   : %s\n", custID)
		}
		if first, ok := receipt["isFirstOrder"].(bool); ok && first {
			fmt.Printf("  First Order   : Yes\n")
		}

		fmt.Println("══════════════════════════════════════════════════════")
		return // Only show one order summary
	}
}

// extractErrorCodes finds and prints error codes from a payment response.
func extractErrorCodes(bodyStr string, receipt map[string]interface{}) {
	printed := make(map[string]bool)

	// Walk the receipt for known error fields
	// paymentDetails may contain error info
	if pd, ok := receipt["paymentDetails"].(map[string]interface{}); ok {
		if reason, ok := pd["financialPendingReason"].(string); ok && reason != "" {
			fmt.Printf("  Pending Reason: %s\n", reason)
		}
		if bai, ok := pd["buyerActionInfo"].(map[string]interface{}); ok && bai != nil {
			if typ, ok := bai["__typename"].(string); ok {
				fmt.Printf("  Action Info   : %s\n", typ)
			}
		}
	}

	// Detect 3DS challenge indicators
	if strings.Contains(bodyStr, "CompletePaymentChallenge") ||
		strings.Contains(bodyStr, "THREE_D_SECURE") ||
		strings.Contains(bodyStr, "ThreeDSecure") ||
		strings.Contains(bodyStr, "buyerActionRequired") {
		if !printed["3DS"] {
			printed["3DS"] = true
			fmt.Printf("  Error Code    : 3DS (3D Secure Authentication Required)\n")
		}
	}

	// Scan JSON string for "code":"VALUE" patterns (catches PROCESSING_ERROR, CARD_DECLINED, etc.)
	searchStr := bodyStr
	for {
		idx := strings.Index(searchStr, `"code":"`)
		if idx < 0 {
			break
		}
		start := idx + 8
		end := strings.Index(searchStr[start:], `"`)
		if end < 0 {
			break
		}
		code := searchStr[start : start+end]
		if !printed[code] {
			printed[code] = true
			fmt.Printf("  Error Code    : %s\n", code)
		}
		searchStr = searchStr[start+end:]
	}

	// Also look for "message" fields near errors
	for {
		idx := strings.Index(searchStr, `"message":"`)
		if idx < 0 {
			break
		}
		start := idx + 11
		end := strings.Index(searchStr[start:], `"`)
		if end < 0 {
			break
		}
		msg := searchStr[start : start+end]
		if len(msg) > 0 && len(msg) < 200 && !printed[msg] {
			printed[msg] = true
			fmt.Printf("  Error Message : %s\n", msg)
		}
		searchStr = searchStr[start+end:]
	}

	if len(printed) == 0 {
		fmt.Println("  (no error code found in response)")
	}
}

// checkCardValidationError looks for card number validation errors on the page and inside payment iframes.
// Returns the error text if found, empty string otherwise.
func checkCardValidationError(ctx context.Context) string {
	// Check main page for card validation error messages (broad text search)
	js := `(()=>{
		const body = document.body ? document.body.innerText.toLowerCase() : '';
		if(body.includes('enter a valid card number') || body.includes('invalid card number')) {
			// Find the specific element with the error
			const all = document.querySelectorAll('*');
			for(const el of all){
				if(el.children.length === 0 || el.tagName === 'P' || el.tagName === 'SPAN' || el.tagName === 'DIV'){
					const t = el.textContent.trim().toLowerCase();
					if((t.includes('valid card number') || t.includes('invalid card number')) && t.length < 100){
						return el.textContent.trim();
					}
				}
			}
			return 'Enter a valid card number';
		}
		return '';
	})()`

	var errText string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &errText)); err == nil && errText != "" {
		return errText
	}

	// Check ALL frames via CDP (cross-origin iframes where the error actually lives)
	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return nil
		}
		var checkFrame func(ft *page.FrameTree)
		checkFrame = func(ft *page.FrameTree) {
			if ft.Frame != nil && errText == "" {
				worldID, err := page.CreateIsolatedWorld(ft.Frame.ID).Do(ctx)
				if err == nil {
					res, _, err := runtime.Evaluate(`
						(()=>{
							const body = document.body ? document.body.innerText.toLowerCase() : '';
							if(body.includes('valid card number') || body.includes('invalid card')) {
								return document.body.innerText.trim().substring(0, 200);
							}
							return '';
						})()
					`).WithContextID(runtime.ExecutionContextID(worldID)).Do(ctx)
					if err == nil && res != nil && res.Value != nil {
						val := strings.Trim(string(res.Value), `"`)
						if len(val) > 0 {
							errText = val
						}
					}
				}
			}
			for _, child := range ft.ChildFrames {
				if errText == "" {
					checkFrame(child)
				}
			}
		}
		checkFrame(tree)
		return nil
	}))

	return errText
}

// waitForResult monitors the page for success/failure after payment submission.
func waitForResult(ctx context.Context) {
	deadline := time.Now().Add(60 * time.Second)
	lastURL := ""
	errorSeen := false

	for time.Now().Before(deadline) {
		var currentURL string
		if err := chromedp.Run(ctx, chromedp.Location(&currentURL)); err != nil {
			fmt.Println("Error getting URL:", err)
			break
		}

		if currentURL != lastURL {
			fmt.Printf("URL: %s\n", currentURL)
			lastURL = currentURL
		}

		// Check for success indicators (covers most Shopify confirmation URL patterns)
		if strings.Contains(currentURL, "thank_you") || strings.Contains(currentURL, "thank-you") ||
			strings.Contains(currentURL, "orders/") || strings.Contains(currentURL, "order-confirmation") ||
			strings.Contains(currentURL, "confirmation") {
			fmt.Println("\n✅ CHECKOUT SUCCESSFUL!")

			var pageText string
			chromedp.Run(ctx, chromedp.Evaluate(`document.body.innerText`, &pageText))
			if len(pageText) > 500 {
				pageText = pageText[:500]
			}
			fmt.Println("\nConfirmation page:")
			fmt.Println(pageText)
			readCapturedResponses(ctx)
			return
		}

		// Check for card validation errors (e.g. "Enter a valid card number")
		if cardErr := checkCardValidationError(ctx); cardErr != "" {
			fmt.Println("\n╔══════════════════════════════════════════════════════╗")
			fmt.Println("║              ❌ PAYMENT FAILED                       ║")
			fmt.Println("╚══════════════════════════════════════════════════════╝")
			fmt.Printf("  Error Code    : INCORRECT_NUMBER\n")
			fmt.Printf("  Error Message : %s\n", cardErr)
			fmt.Println("══════════════════════════════════════════════════════")
			return
		}

		// Check for payment error — stop immediately
		var errorText string
		chromedp.Run(ctx, chromedp.Evaluate(`
			(()=>{
				const errs = document.querySelectorAll('[role="alert"], .notice--error, .field__message--error, [data-error]');
				return Array.from(errs).map(e => e.textContent.trim()).filter(t => t).join(' | ');
			})()
		`, &errorText))

		if errorText != "" {
			lower := strings.ToLower(errorText)
			// "order's being processed" is a status message, not an error
			if strings.Contains(lower, "order") && strings.Contains(lower, "being processed") {
				if !errorSeen {
					fmt.Printf("  Status: %s\n", errorText)
				}
			} else if strings.Contains(lower, "captcha") {
				fmt.Printf("\n⚠️  CAPTCHA detected: %s\n", errorText)
				readCapturedResponses(ctx)
				return
			} else if strings.Contains(lower, "issue processing") ||
				strings.Contains(lower, "problem processing") ||
				strings.Contains(lower, "unable to process") ||
				strings.Contains(lower, "payment method") ||
				strings.Contains(lower, "try again") ||
				strings.Contains(lower, "card was declined") ||
				strings.Contains(lower, "transaction failed") {
				// These are real payment errors, not 3DS status
				if !errorSeen {
					errorSeen = true
					fmt.Printf("\n❌ Payment error: %s\n", errorText)
					time.Sleep(3 * time.Second)
					readCapturedResponses(ctx)
					return
				}
			} else if strings.Contains(lower, "redirect") ||
				strings.Contains(lower, "3d secure") ||
				strings.Contains(lower, "3ds") ||
				strings.Contains(lower, "authentication") {
				if !errorSeen {
					fmt.Printf("  3DS challenge: %s — waiting...\n", errorText)
				}
				// Don't abort — wait for 3DS challenge to complete
			} else if !errorSeen {
				errorSeen = true
				fmt.Printf("\n❌ Payment error: %s\n", errorText)
				time.Sleep(3 * time.Second)
				readCapturedResponses(ctx)
				return
			}
		}

		time.Sleep(2 * time.Second)
	}

	fmt.Println("\nTimeout waiting for checkout result")
	readCapturedResponses(ctx)
	var finalURL string
	chromedp.Run(ctx, chromedp.Location(&finalURL))
	fmt.Printf("Final URL: %s\n", finalURL)
}

// createCheckoutViaHTTP uses plain HTTP to fetch products, add to cart, and get the checkout URL.
func createCheckoutViaHTTP() (string, []*http.Cookie, int64) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects automatically
		},
	}

	// Step 1: GET /products.json
	fmt.Println("[HTTP] Fetching products...")
	resp, err := client.Get(browserStoreURL + "/products.json")
	if err != nil {
		log.Fatal("Failed to fetch products:", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var productsData BrowserProductsResponse
	if err := json.Unmarshal(body, &productsData); err != nil {
		log.Fatal("Failed to parse products:", err)
	}

	// Find cheapest variant
	var cheapestVariantID int64
	var cheapestPrice float64 = math.MaxFloat64
	var cheapestProductTitle, cheapestVariantTitle, cheapestPriceStr string

	for _, p := range productsData.Products {
		for _, v := range p.Variants {
			pf, _ := strconv.ParseFloat(v.Price, 64)
			if pf < cheapestPrice && pf > 0 {
				cheapestPrice = pf
				cheapestVariantID = v.ID
				cheapestProductTitle = p.Title
				cheapestVariantTitle = v.Title
				cheapestPriceStr = v.Price
			}
		}
	}
	if cheapestVariantID == 0 {
		log.Fatal("No products found")
	}
	fmt.Printf("[HTTP] Cheapest: %s — %s ($%s) [variant %d]\n", cheapestProductTitle, cheapestVariantTitle, cheapestPriceStr, cheapestVariantID)

	// Step 2: POST /cart/add.js
	fmt.Println("[HTTP] Adding to cart...")
	cartBody := fmt.Sprintf(`{"items":[{"id":%d,"quantity":1}]}`, cheapestVariantID)
	req, _ := http.NewRequest("POST", browserStoreURL+"/cart/add.js", strings.NewReader(cartBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		log.Fatal("Failed to add to cart:", err)
	}
	addResult, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(addResult) > 200 {
		addResult = addResult[:200]
	}
	fmt.Printf("[HTTP] Cart response (%d): %s\n", resp.StatusCode, string(addResult))

	// Step 3: GET /checkout — follow redirects to get the actual checkout URL
	fmt.Println("[HTTP] Creating checkout...")
	client.CheckRedirect = nil // allow redirects now
	resp, err = client.Get(browserStoreURL + "/checkout")
	if err != nil {
		log.Fatal("Failed to create checkout:", err)
	}
	resp.Body.Close()

	checkoutURL := resp.Request.URL.String()

	// Transfer cookies to the browser by appending them via URL if needed
	// The browser will need the session cookies — we'll pass them via cookie injection
	// For now, store cookies to inject later
	parsedURL, _ := url.Parse(browserStoreURL)
	cookies := jar.Cookies(parsedURL)
	fmt.Printf("[HTTP] Got %d cookies\n", len(cookies))
	for _, c := range cookies {
		fmt.Printf("  %s = %s...\n", c.Name, truncate(c.Value, 40))
	}

	return checkoutURL, cookies, cheapestVariantID
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
