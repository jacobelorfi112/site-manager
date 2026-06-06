package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// safeEval evaluates JS on the page, returning the string result or "" on error.
func safeEval(pg *rod.Page, js string) string {
	res, err := pg.Eval(js)
	if err != nil {
		return ""
	}
	return res.Value.String()
}

// paymentCapture stores a captured PollForReceipt/SubmitForCompletion response.
type paymentCapture struct {
	URL    string
	Status int
	Body   string
}

// runCheckout executes a full checkout flow for one card+store in the given Rod page.
func runCheckout(pg *rod.Page, entry CardEntry, buyer BuyerInfo) (result CheckResult) {
	start := time.Now()
	defer func() { result.ElapsedSeconds = time.Since(start).Seconds() }()
	card := entry.Card
	card.FullName = buyer.FirstName + " " + buyer.LastName
	storeURL := entry.Store

	result = CheckResult{
		Index:     entry.Index,
		CardLast4: card.Last4,
		Store:     storeURL,
		Status:    "error",
	}

	logPrefix := fmt.Sprintf("[%d/%s]", entry.Index, card.Last4)

	// Remove navigator.webdriver flag
	pg.EvalOnNewDocument(`Object.defineProperty(navigator, 'webdriver', {get: () => undefined})`)

	// Enable CDP Fetch domain to intercept PollForReceipt/SubmitForCompletion responses.
	proto.FetchEnable{
		Patterns: []*proto.FetchRequestPattern{
			{URLPattern: "*PollForReceipt*", RequestStage: proto.FetchRequestStageRequest},
			{URLPattern: "*SubmitForCompletion*", RequestStage: proto.FetchRequestStageRequest},
			{URLPattern: "*PollForReceipt*", RequestStage: proto.FetchRequestStageResponse},
			{URLPattern: "*SubmitForCompletion*", RequestStage: proto.FetchRequestStageResponse},
		},
	}.Call(pg)
	var netCapMu sync.Mutex
	var netCaptures []paymentCapture
	go pg.EachEvent(func(e *proto.FetchRequestPaused) {
		reqURL := e.Request.URL
		if e.ResponseStatusCode == nil {
			fmt.Printf("%s CDP Fetch request: %s\n", logPrefix, reqURL[:min(len(reqURL), 120)])
			proto.FetchContinueRequest{RequestID: e.RequestID}.Call(pg)
			return
		}
		status := *e.ResponseStatusCode
		bodyRes, err := proto.FetchGetResponseBody{RequestID: e.RequestID}.Call(pg)
		if err == nil && bodyRes != nil {
			body := bodyRes.Body
			if bodyRes.Base64Encoded {
				if decoded, err := base64.StdEncoding.DecodeString(body); err == nil {
					body = string(decoded)
				}
			}
			netCapMu.Lock()
			netCaptures = append(netCaptures, paymentCapture{
				URL:    reqURL,
				Status: status,
				Body:   body,
			})
			netCapMu.Unlock()
			fmt.Printf("%s CDP Fetch response: %s (status=%d, %d bytes)\n",
				logPrefix, reqURL[:min(len(reqURL), 120)], status, len(body))
		}
		proto.FetchContinueResponse{RequestID: e.RequestID}.Call(pg)
	})()

	// ── Create checkout via HTTP ──
	fmt.Printf("%s Creating checkout on %s...\n", logPrefix, storeURL)
	checkoutURL, cookies, variantID, productPrice, err := createCheckout(storeURL)
	if err != nil {
		result.ErrorMessage = "checkout creation failed: " + err.Error()
		result.ErrorCode = "CHECKOUT_FAILED"
		fmt.Printf("%s ERROR: %s\n", logPrefix, result.ErrorMessage)
		return result
	}
	result.CheckoutPrice = productPrice
	fmt.Printf("%s Checkout URL ready (%.1fs)\n", logPrefix, time.Since(start).Seconds())

	// ── Inject cookies and navigate to checkout ──
	parsedStore, _ := url.Parse(storeURL)
	cookieDomain := parsedStore.Hostname()
	domParts := strings.Split(cookieDomain, ".")
	if len(domParts) > 2 {
		cookieDomain = strings.Join(domParts[len(domParts)-2:], ".")
	}
	cookieDomain = "." + cookieDomain

	var rodCookies []*proto.NetworkCookieParam
	for _, c := range cookies {
		rodCookies = append(rodCookies, &proto.NetworkCookieParam{
			Name:   c.Name,
			Value:  c.Value,
			Domain: cookieDomain,
			Path:   "/",
		})
	}
	pg.SetCookies(rodCookies)

	// Navigate and wait for page to settle (resilient to redirects)
	err = pg.Navigate(checkoutURL)
	if err != nil {
		result.ErrorMessage = "failed to navigate to checkout: " + err.Error()
		fmt.Printf("%s ERROR: %s\n", logPrefix, result.ErrorMessage)
		return result
	}
	time.Sleep(2 * time.Second)

	// Wait for checkout form with generous timeout
	emailEl, err := pg.Timeout(15 * time.Second).Element(`input#email, input[autocomplete="email"], input[name="email"]`)
	checkoutReady := err == nil && emailEl != nil

	info, _ := pg.Info()
	currentURL := ""
	if info != nil {
		currentURL = info.URL
	}

	if !checkoutReady || (!strings.Contains(currentURL, "/checkouts/") && !strings.Contains(currentURL, "/checkout")) {
		fmt.Printf("%s Redirected — recreating cart in browser\n", logPrefix)

		// Use cart/add.js via fetch in the page
		addJS := fmt.Sprintf(`() => {
			return fetch('%s/cart/add.js', {
				method:'POST',
				headers:{'Content-Type':'application/json'},
				body: JSON.stringify({items:[{id:%d,quantity:1}]})
			}).then(r => r.ok ? 'ok' : 'status:'+r.status).catch(e => 'error:'+e.message)
		}`, storeURL, variantID)

		addRes, evalErr := pg.Eval(addJS)
		addResult := ""
		if evalErr == nil && addRes != nil {
			addResult = addRes.Value.String()
		}
		if !strings.Contains(addResult, "ok") {
			// Fallback: navigate to cart/add URL
			addURL := fmt.Sprintf("%s/cart/add?id=%d&quantity=1", storeURL, variantID)
			pg.Navigate(addURL)
			time.Sleep(2 * time.Second)
		}

		err = pg.Navigate(storeURL + "/checkout")
		if err != nil {
			result.ErrorMessage = "failed to navigate to checkout (retry)"
			result.ErrorCode = "CHECKOUT_FAILED"
			fmt.Printf("%s ERROR: %s\n", logPrefix, result.ErrorMessage)
			return result
		}
		time.Sleep(2 * time.Second)

		_, err := pg.Timeout(10 * time.Second).Element(`input#email, input[autocomplete="email"], input[name="email"]`)
		if err != nil {
			result.ErrorMessage = "checkout form not found on retry"
			result.ErrorCode = "CHECKOUT_FAILED"
			fmt.Printf("%s ERROR: %s\n", logPrefix, result.ErrorMessage)
			return result
		}
	}

	// ── Fill checkout form ──
	fmt.Printf("%s Filling checkout form...\n", logPrefix)
	fillJS := buildFillJS(buyer)
	fillResult := safeEval(pg, fmt.Sprintf("() => { return %s }", fillJS))
	fmt.Printf("%s Form: %s\n", logPrefix, fillResult)

	// Dismiss autocomplete
	pg.Keyboard.Press(input.Escape)
	pg.Eval(`() => document.body.click()`)
	time.Sleep(100 * time.Millisecond)

	// Fix country if needed
	countryFixJS := buildCountryFixJS(buyer.Country)
	countryStatus := safeEval(pg, fmt.Sprintf("() => { return %s }", countryFixJS))
	if strings.Contains(countryStatus, "not found") || strings.Contains(countryStatus, "not set") {
		selectDropdownViaEval(pg, buyer.Country, buyer.CountryName)
	}

	time.Sleep(150 * time.Millisecond)

	// Fix state if needed
	stateFixJS := buildStateFixJS(buyer.State)
	stateStatus := safeEval(pg, fmt.Sprintf("() => { return %s }", stateFixJS))
	if strings.Contains(stateStatus, "not set") {
		selectDropdownViaEval(pg, buyer.State, buyer.StateName)
	}

	// Auto-fill empty address2
	pg.Eval(`() => {
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
			if(el && !el.value){ el.focus(); setNativeValue(el, 'Apt'); return 'filled'; }
		}
		return 'none';
	}`)

	time.Sleep(1500 * time.Millisecond) // let Shopify recalculate payment methods after country change

	// ── Multi-step checkout advancement ──
	// Some Shopify stores use multi-step checkout (Info → Shipping → Payment).
	for step := 0; step < 3; step++ {
		advanced := safeEval(pg, `() => {
			const btns = document.querySelectorAll('button[type="submit"], button, input[type="submit"]');
			const kw = ['continue to shipping','continue to payment','continue','next step','proceed'];
			const stop = ['pay now','complete order','place order','submit order','buy now','add tip','apply','remove','shop pay','paypal'];
			for(const b of btns){
				const txt = b.textContent.toLowerCase().trim().replace(/\s+/g,' ');
				if(txt.length > 60 || txt.length < 3) continue;
				if(stop.some(s => txt.includes(s))) continue;
				if(kw.some(k => txt.includes(k)) && b.offsetParent !== null){
					b.click();
					return 'clicked: '+txt;
				}
			}
			return 'none';
		}`)
		if advanced == "none" || advanced == "" {
			break
		}
		fmt.Printf("%s Multi-step: %s\n", logPrefix, advanced)
		time.Sleep(2000 * time.Millisecond)
		fixValidationErrors(pg, buyer.Phone)
		time.Sleep(500 * time.Millisecond)
	}

	// ── Early detection of shipping/store errors ──
	earlyError := safeEval(pg, `() => {
		const body = (document.body ? document.body.innerText : '').toLowerCase();
		const issues = ['not available for delivery','shipping not available','cannot be shipped',
			'checkout is unavailable','empty cart and return','cart is empty'];
		for(const kw of issues){
			if(body.includes(kw)){
				const sels = ['[role="alert"]','.notice--error','.banner--error','.banner__content',
					'[class*="error-message"]','[class*="notice"]','[class*="error"]'];
				for(const sel of sels){
					for(const el of document.querySelectorAll(sel)){
						const t = el.textContent.trim();
						if(t && t.length > 10 && t.length < 500) return t;
					}
				}
				return body.substring(0, 300);
			}
		}
		return '';
	}`)
	if earlyError != "" {
		result.ErrorCode = "CHECKOUT_FAILED"
		result.ErrorMessage = earlyError
		fmt.Printf("%s EARLY CHECKOUT ERROR: %s\n", logPrefix, earlyError)
		return result
	}

	// Try to select "Credit card" payment method if not already selected
	ccSel := safeEval(pg, `() => {
		const kw = ['credit card','credit','debit or credit','card'];
		const stop = ['gift card','card on file','saved card'];
		function match(txt){ txt=txt.toLowerCase().trim(); return kw.some(k=>txt.includes(k)) && !stop.some(s=>txt.includes(s)); }
		const radios = document.querySelectorAll('input[type="radio"]');
		for(const r of radios){
			const lbl = r.closest('label') || document.querySelector('label[for="'+r.id+'"]');
			if(lbl && match(lbl.textContent)){ r.click(); lbl.click(); return 'radio:'+lbl.textContent.trim().substring(0,40); }
		}
		const els = document.querySelectorAll('[role="radio"],[role="tab"],[data-payment-method],button[class*="payment"],[class*="payment-method"] [role="button"]');
		for(const e of els){
			if(match(e.textContent)){ e.click(); return 'el:'+e.textContent.trim().substring(0,40); }
		}
		return 'none';
	}`)
	if ccSel != "none" && ccSel != "" {
		fmt.Printf("%s Credit card selected: %s\n", logPrefix, ccSel)
		time.Sleep(1500 * time.Millisecond)
	}

	// Debug: dump page state before payment fill
	debugInfo := safeEval(pg, `() => {
		const r = {url: location.href, buttons: [], iframes: [], sections: []};
		document.querySelectorAll('button, input[type="submit"], a[role="button"]').forEach(b => {
			const txt = b.textContent.trim().replace(/\s+/g, ' ').substring(0, 60);
			if(txt.length > 2) r.buttons.push(txt);
		});
		document.querySelectorAll('iframe').forEach(f => {
			r.iframes.push((f.id||'no-id') + ' :: ' + (f.src||'').substring(0, 80));
		});
		document.querySelectorAll('[data-step], [class*="payment"], [class*="shipping"], section, [role="main"]').forEach(s => {
			const cls = s.className ? (typeof s.className === 'string' ? s.className : '') : '';
			r.sections.push(s.tagName + '.' + cls.substring(0, 60));
		});
		return JSON.stringify(r);
	}`)
	fmt.Printf("%s PAGE STATE: %s\n", logPrefix, debugInfo)

	// ── Fill payment ──
	fmt.Printf("%s Filling payment... (%.1fs)\n", logPrefix, time.Since(start).Seconds())
	payDone := make(chan error, 1)
	go func() { payDone <- fillPaymentIframe(pg, card, logPrefix) }()
	var payErr error
	select {
	case payErr = <-payDone:
	case <-time.After(45 * time.Second):
		payErr = fmt.Errorf("payment fill timed out after 45s")
	}
	if payErr != nil {
		result.ErrorMessage = "payment fill failed: " + payErr.Error()
		result.ErrorCode = "PAYMENT_FILL_FAILED"
		fmt.Printf("%s ERROR: %s\n", logPrefix, result.ErrorMessage)
		return result
	}
	fmt.Printf("%s Payment filled (%.1fs)\n", logPrefix, time.Since(start).Seconds())

	// Fill billing address if separate
	billingJS := buildBillingJS(buyer)
	pg.Eval(fmt.Sprintf("() => { return %s }", billingJS))

	// ── Click Pay ──
	fmt.Printf("%s Clicking Pay... (%.1fs)\n", logPrefix, time.Since(start).Seconds())
	clickPayButton(pg)

	// Quick card validation check
	time.Sleep(500 * time.Millisecond)
	if cardErr := checkCardValidationError(pg); cardErr != "" {
		result.Status = "declined"
		result.ErrorCode = "INCORRECT_NUMBER"
		result.ErrorMessage = cardErr
		fmt.Printf("%s DECLINED: %s\n", logPrefix, cardErr)
		return result
	}

	// ── Wait for result ──
	fmt.Printf("%s Waiting for result...\n", logPrefix)
	waitResult := waitForCheckoutResult(pg, logPrefix, &netCapMu, &netCaptures)
	result.Status = waitResult.Status
	result.ErrorCode = waitResult.ErrorCode
	result.ErrorMessage = waitResult.ErrorMessage
	result.OrderNumber = waitResult.OrderNumber
	result.ElapsedSeconds = time.Since(start).Seconds()
	fmt.Printf("%s RESULT: %s (%s) in %.1fs\n", logPrefix, result.Status, result.ErrorCode, result.ElapsedSeconds)
	return result
}

// ──────────────────────────────────────────────────────────────────
// HTTP checkout creation (no browser needed)
// ──────────────────────────────────────────────────────────────────

type productsResponse struct {
	Products []struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Handle   string `json:"handle"`
		Variants []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Price string `json:"price"`
		} `json:"variants"`
	} `json:"products"`
}

func createCheckout(storeURL string) (checkoutURL string, cookies []*http.Cookie, variantID int64, price float64, err error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(storeURL + "/products.json")
	if err != nil {
		return "", nil, 0, 0, fmt.Errorf("fetch products: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil, 0, 0, fmt.Errorf("products endpoint returned %d", resp.StatusCode)
	}
	if len(body) > 0 && body[0] == '<' {
		return "", nil, 0, 0, fmt.Errorf("site returned HTML instead of JSON (password-protected or unavailable)")
	}

	var data productsResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return "", nil, 0, 0, fmt.Errorf("parse products: %w", err)
	}

	var cheapest int64
	cheapestPrice := math.MaxFloat64
	for _, p := range data.Products {
		for _, v := range p.Variants {
			pf, _ := strconv.ParseFloat(v.Price, 64)
			if pf < cheapestPrice && pf > 0 {
				cheapestPrice = pf
				cheapest = v.ID
			}
		}
	}
	if cheapest == 0 {
		return "", nil, 0, 0, fmt.Errorf("no products found")
	}

	cartBody := fmt.Sprintf(`{"items":[{"id":%d,"quantity":1}]}`, cheapest)
	req, _ := http.NewRequest("POST", storeURL+"/cart/add.js", strings.NewReader(cartBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return "", nil, 0, 0, fmt.Errorf("add to cart: %w", err)
	}
	resp.Body.Close()

	client.CheckRedirect = nil
	resp, err = client.Get(storeURL + "/checkout")
	if err != nil {
		return "", nil, 0, 0, fmt.Errorf("create checkout: %w", err)
	}
	resp.Body.Close()

	parsedURL, _ := url.Parse(storeURL)
	return resp.Request.URL.String(), jar.Cookies(parsedURL), cheapest, cheapestPrice, nil
}

// ──────────────────────────────────────────────────────────────────
// JS builders (same JS, unchanged)
// ──────────────────────────────────────────────────────────────────

func buildFillJS(b BuyerInfo) string {
	return fmt.Sprintf(`(()=>{
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
				const nativeSetter = Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype, 'value').set;
				nativeSetter.call(el, val);
				el.dispatchEvent(new Event('input', {bubbles: true}));
				el.dispatchEvent(new Event('change', {bubbles: true}));
				el.dispatchEvent(new Event('blur', {bubbles: true}));
				return true;
			}
			return false;
		}
		function setCustomSelect(labelText, val, displayVal) {
			const labels = document.querySelectorAll('label, legend, .field__label');
			for(const lbl of labels){
				const lt = lbl.textContent.toLowerCase().trim();
				if(!lt.includes(labelText)) continue;
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
		fill('input#email, input[autocomplete="email"], input[name="email"], input[type="email"]', %q);
		filled.push('email');
		if(!setSelect('select[autocomplete="country"], select[name="countryCode"], select[name="country"]', %q)){
			setCustomSelect('country', %q, %q);
		}
		filled.push('country');
		fill('input[autocomplete="given-name"], input[name="firstName"], input[name="first_name"]', %q);
		fill('input[autocomplete="family-name"], input[name="lastName"], input[name="last_name"]', %q);
		filled.push('name');
		fill('input[autocomplete="address-line1"], input[name="address1"], input[name="address"]', %q);
		fill('input[autocomplete="address-line2"], input[name="address2"]', %q);
		filled.push('address');
		fill('input[autocomplete="address-level2"], input[name="city"]', %q);
		filled.push('city');
		if(!setSelect('select[autocomplete="address-level1"], select[name="zone"], select[name="province"]', %q)){
			setCustomSelect('state', %q, %q);
		}
		fill('input[autocomplete="address-level1"], input[name="zone"], input[name="province"]', %q);
		filled.push('state');
		fill('input[autocomplete="postal-code"], input[name="postalCode"], input[name="zip"]', %q);
		filled.push('zip');
		fill('input[autocomplete="tel"], input[name="phone"], input[type="tel"]', %q);
		filled.push('phone');
		return 'filled: ' + filled.join(', ');
	})()`,
		b.Email, b.Country, b.Country, b.CountryName,
		b.FirstName, b.LastName,
		b.Address1, b.Address2,
		b.City,
		b.State, b.State, b.StateName, b.State,
		b.Zip,
		b.Phone)
}

func buildCountryFixJS(country string) string {
	return fmt.Sprintf(`(()=>{
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
			if(el.value === '%s') return 'country already set: '+el.value;
			const usOpt = el.querySelector('option[value="%s"]');
			if(usOpt){
				selectOption(el, usOpt);
				return 'selected via option index: '+usOpt.value;
			}
			const opts = el.querySelectorAll('option');
			for(const o of opts){
				if(o.value && o.value.trim() !== '' && !o.disabled){
					selectOption(el, o);
					return 'selected fallback country: '+o.value+' ('+o.textContent.trim()+')';
				}
			}
		}
		return 'select not found or no matching option';
	})()`, country, country)
}

func buildStateFixJS(state string) string {
	return fmt.Sprintf(`(()=>{
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
	})()`, state)
}

func buildBillingJS(b BuyerInfo) string {
	return fmt.Sprintf(`(()=>{
		function setNativeValue(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		const billingCtx = document.querySelector('[data-billing-address], [id*="billing"], .section--billing-address, fieldset[data-address-fields]');
		function qb(sel) {
			if (billingCtx) { const e = billingCtx.querySelector(sel); if (e) return e; }
			return null;
		}
		const bFirst = qb('input[autocomplete="given-name"], input[name="firstName"]') ||
			qb('input[name*="first_name"], input[name*="firstName"]');
		if (!bFirst || bFirst.value) return 'no separate billing or already filled';
		const filled = [];
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
	})()`, b.FirstName, b.LastName, b.Address1, b.City, b.Zip, b.Phone, b.Country, b.State)
}

// ──────────────────────────────────────────────────────────────────
// Helpers rewritten for Rod
// ──────────────────────────────────────────────────────────────────

func selectDropdownViaEval(pg *rod.Page, value string, displayName string) {
	js := fmt.Sprintf(`() => {
		const sels = document.querySelectorAll('select');
		for(const sel of sels){
			if(sel.offsetParent === null) continue;
			const ac = (sel.autocomplete||'').toLowerCase();
			const nm = (sel.name||'').toLowerCase();
			if(ac.includes('country') || nm.includes('country') || nm.includes('countrycode') ||
			   ac.includes('address-level1') || nm.includes('zone') || nm.includes('province')){
				let opt = sel.querySelector('option[value="%s"]');
				if(!opt){
					const allOpts = sel.querySelectorAll('option');
					for(const o of allOpts){
						if(o.value && o.value.trim() !== '' && !o.disabled){ opt = o; break; }
					}
				}
				if(!opt) continue;
				sel.focus();
				opt.selected = true;
				sel.dispatchEvent(new Event('input', {bubbles:true}));
				sel.dispatchEvent(new Event('change', {bubbles:true}));
				sel.dispatchEvent(new Event('blur', {bubbles:true}));
				return 'selected: '+opt.value;
			}
		}
		return 'no matching select';
	}`, value)
	pg.Eval(js)
}

func clickContinueIfPresent(pg *rod.Page) bool {
	js := `() => {
		const btns = document.querySelectorAll('button[type="submit"], button, a.step__footer__continue-btn');
		const patterns = ['continue', 'ship to', 'next step', 'proceed', 'continue to shipping', 'continue to payment'];
		const stopWords = ['pay now', 'complete order', 'place order', 'submit order', 'buy now', 'confirm',
			'additional', 'more', 'expand', 'show', 'toggle', 'select', 'add', 'apply', 'remove', 'change'];
		for(const b of btns){
			const txt = b.textContent.toLowerCase().trim().replace(/\s+/g, ' ');
			if(txt.length > 60) continue;
			if(stopWords.some(sw => txt.includes(sw))) continue;
			if(patterns.some(kw => txt.includes(kw))){
				b.click();
				return 'clicked: '+txt;
			}
		}
		return 'none';
	}`
	result := safeEval(pg, js)
	return result != "none" && result != ""
}

func fixValidationErrors(pg *rod.Page, phone string) {
	js := fmt.Sprintf(`() => {
		function setNativeValue(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		const fixed = [];
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
	}`, phone)
	pg.Eval(js)
}

// ──────────────────────────────────────────────────────────────────
// Payment iframe filling — Rod makes this MUCH simpler
// ──────────────────────────────────────────────────────────────────

func fillPaymentIframe(pg *rod.Page, card CardInfo, logPrefix string) error {
	// Try direct (non-iframe) card inputs first — faster to fill
	directFound := safeEval(pg, `() => {
		const sels = [
			'input[autocomplete="cc-number"]',
			'input[placeholder*="card number" i]',
			'input[name="number"][type="text"]',
			'input[name="number"][type="tel"]',
			'input[id*="card-number"]',
			'input[data-current-field="number"]',
			'input[aria-label*="card number" i]'
		];
		for(const sel of sels){
			const el = document.querySelector(sel);
			if(el && el.offsetParent !== null) return 'direct';
		}
		return '';
	}`)
	if directFound == "direct" {
		fmt.Printf("%s Payment: using direct card inputs (no iframe)\n", logPrefix)
		return fillPaymentDirect(pg, card, logPrefix)
	}

	// Wait for card number iframe
	iframeSel := `iframe[id*="card-fields-number"], iframe[id*="number"], iframe[src*="number-ltr"], iframe[src*="card-fields"]`
	_, err := pg.Timeout(12 * time.Second).Element(iframeSel)
	if err != nil {
		// Iframe not found — check for direct inputs that may have appeared late
		directFound = safeEval(pg, `() => {
			const sels = ['input[autocomplete="cc-number"]','input[placeholder*="card number" i]','input[name="number"]','input[id*="card-number"]','input[data-current-field="number"]'];
			for(const sel of sels){ const el = document.querySelector(sel); if(el && el.offsetParent !== null) return 'direct'; }
			return '';
		}`)
		if directFound == "direct" {
			fmt.Printf("%s Payment: using direct card inputs (late discovery)\n", logPrefix)
			return fillPaymentDirect(pg, card, logPrefix)
		}
		diag := safeEval(pg, `() => {
			const iframes = document.querySelectorAll('iframe');
			const iInfo = [];
			iframes.forEach((f, i) => { iInfo.push('iframe[' + i + '] id=' + (f.id||'') + ' src=' + (f.src||'').substring(0,80)); });
			return iInfo.length > 0 ? iInfo.join(' | ') : 'NONE';
		}`)
		return fmt.Errorf("no card fields found after 12s. Page: %s", diag)
	}

	// Re-get iframe from parent page context
	iframeEl, err := pg.Element(iframeSel)
	if err != nil {
		return fmt.Errorf("iframe re-get failed: %w", err)
	}

	// Scroll iframe into view
	iframeEl.ScrollIntoView()
	time.Sleep(500 * time.Millisecond)

	// Wait for iframe to be fully ready
	for attempt := 0; attempt < 5; attempt++ {
		ready := safeEval(pg, `() => {
			const iframe = document.querySelector('iframe[id*="number"], iframe[src*="number-ltr"], iframe[src*="card-fields"]');
			if(!iframe) return 'no-iframe';
			const parent = iframe.closest('.card-fields-container, [data-card-fields], [class*="card"]');
			if(parent){
				const loading = parent.querySelector('.loading, [aria-busy="true"], .spinner');
				if(loading && loading.offsetParent !== null) return 'loading';
			}
			const r = iframe.getBoundingClientRect();
			if(r.height < 10) return 'collapsed';
			return 'ready';
		}`)
		if ready == "ready" || ready == "no-iframe" || ready == "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("%s Payment: using iframe (%s)\n", logPrefix, iframeSel)
	return fillPaymentViaIframe(pg, iframeEl, card, logPrefix)
}

// fillPaymentDirect fills card details using direct page inputs (non-iframe Shopify checkout).
func fillPaymentDirect(pg *rod.Page, card CardInfo, logPrefix string) error {
	result := safeEval(pg, fmt.Sprintf(`() => {
		function setVal(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		function fill(sels, val) {
			for(const sel of sels){
				const el = document.querySelector(sel);
				if(el && el.offsetParent !== null){
					el.focus();
					setVal(el, val);
					return true;
				}
			}
			return false;
		}
		const filled = [];
		if(fill([
			'input[autocomplete="cc-number"]',
			'input[placeholder*="card number" i]',
			'input[name="number"]',
			'input[id*="card-number"]',
			'input[data-current-field="number"]',
			'input[aria-label*="card number" i]'
		], %q)) filled.push('number');
		const expCombined = %q;
		const expMM = %q;
		const expYY = %q;
		if(fill([
			'input[autocomplete="cc-exp"]',
			'input[placeholder*="MM / YY" i]',
			'input[placeholder*="MM/YY" i]',
			'input[name="expiry"]',
			'input[id*="expiry"]',
			'input[data-current-field="expiry"]',
			'input[aria-label*="expiration" i]'
		], expCombined)) {
			filled.push('expiry');
		} else {
			fill(['input[autocomplete="cc-exp-month"]','input[name="month"]','input[name="exp_month"]'], expMM);
			fill(['input[autocomplete="cc-exp-year"]','input[name="year"]','input[name="exp_year"]'], expYY);
			filled.push('exp_parts');
		}
		if(fill([
			'input[autocomplete="cc-csc"]',
			'input[placeholder*="security code" i]',
			'input[placeholder*="CVV" i]',
			'input[placeholder*="CVC" i]',
			'input[name="verification_value"]',
			'input[name="cvv"]','input[name="cvc"]',
			'input[id*="cvv"]',
			'input[data-current-field="verification_value"]',
			'input[aria-label*="security" i]',
			'input[aria-label*="CVV" i]'
		], %q)) filled.push('cvv');
		fill([
			'input[autocomplete="cc-name"]',
			'input[placeholder*="name on card" i]',
			'input[placeholder*="cardholder" i]',
			'input[name*="cardName" i]',
			'input[name*="nameoncard" i]',
			'input[data-current-field="name"]',
			'input[aria-label*="name on card" i]',
			'input[id*="name" i][id*="card" i]'
		], %q);
		filled.push('name');
		return filled.join(',');
	}`, card.Number, card.ExpMM+card.ExpYY, card.ExpMM, card.ExpYY, card.CVV, card.FullName))

	if result == "" {
		return fmt.Errorf("direct card fill returned empty result")
	}
	fmt.Printf("%s Payment direct filled: %s\n", logPrefix, result)
	return nil
}

func fillPaymentViaIframe(pg *rod.Page, iframeEl *rod.Element, card CardInfo, logPrefix string) error {
	iframeSel := `iframe[id*="card-fields-number"], iframe[id*="number"], iframe[src*="number-ltr"], iframe[src*="card-fields"]`
	var cardFrame *rod.Page
	const maxFrameAttempts = 3
	for attempt := 0; attempt < maxFrameAttempts; attempt++ {
		tmpEl, err := pg.Timeout(3 * time.Second).Element(iframeSel)
		if err != nil {
			fmt.Printf("%s Frame() attempt %d: iframe element not found\n", logPrefix, attempt+1)
			time.Sleep(1 * time.Second)
			continue
		}
		type frameResult struct {
			frame *rod.Page
			err   error
		}
		frameCh := make(chan frameResult, 1)
		go func() {
			f, e := tmpEl.Frame()
			frameCh <- frameResult{f, e}
		}()
		select {
		case fr := <-frameCh:
			if fr.err != nil {
				fmt.Printf("%s Frame() attempt %d failed: %v\n", logPrefix, attempt+1, fr.err)
				time.Sleep(1 * time.Second)
				continue
			}
			cardFrame = fr.frame
		case <-time.After(5 * time.Second):
			fmt.Printf("%s Frame() attempt %d timed out after 5s\n", logPrefix, attempt+1)
			continue
		}
		if cardFrame != nil {
			break
		}
	}
	if cardFrame == nil {
		return fmt.Errorf("failed to access card frame after %d attempts", maxFrameAttempts)
	}

	// Find card number input inside the iframe
	cardInput, err := cardFrame.Timeout(8 * time.Second).Element(`input[name="number"], input[autocomplete="cc-number"], input[type="text"], input`)
	if err != nil {
		return fmt.Errorf("card number input not found in iframe: %w", err)
	}
	cardInput, _ = cardFrame.Element(`input[name="number"], input[autocomplete="cc-number"], input[type="text"], input`)

	// Click to focus, then type character by character via real keyboard events
	cardInput.Click(proto.InputMouseButtonLeft, 1)
	time.Sleep(100 * time.Millisecond)
	cardInput.SelectAllText()
	time.Sleep(50 * time.Millisecond)
	pg.Keyboard.Press(input.Backspace)
	time.Sleep(50 * time.Millisecond)

	// Type card number
	fmt.Printf("%s Typing card number...\n", logPrefix)
	for _, ch := range card.Number {
		pg.Keyboard.Type(input.Key(ch))
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// Tab to expiry field — browser natively handles cross-iframe tabbing
	fmt.Printf("%s Tabbing to expiry...\n", logPrefix)
	pg.Keyboard.Press(input.Tab)
	time.Sleep(300 * time.Millisecond)

	// Type expiry
	expiry := card.ExpMM + card.ExpYY
	for _, ch := range expiry {
		pg.Keyboard.Type(input.Key(ch))
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	// Tab to CVV field
	fmt.Printf("%s Tabbing to CVV...\n", logPrefix)
	pg.Keyboard.Press(input.Tab)
	time.Sleep(300 * time.Millisecond)

	// Type CVV
	for _, ch := range card.CVV {
		pg.Keyboard.Type(input.Key(ch))
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	// Fill name on card on main page only
	fillNameOnCard(pg, card.FullName, logPrefix)

	return nil
}

func fillNameOnCard(pg *rod.Page, name string, logPrefix string) {
	safeEval(pg, fmt.Sprintf(`() => {
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
		function setVal(el, val) {
			const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
			s.call(el, val);
			el.dispatchEvent(new Event('input', {bubbles:true}));
			el.dispatchEvent(new Event('change', {bubbles:true}));
			el.dispatchEvent(new Event('blur', {bubbles:true}));
		}
		for(const sel of sels){
			const el = document.querySelector(sel);
			if(el){ el.focus(); setVal(el, %q); return 'filled'; }
		}
		return 'not found';
	}`, name))
}

func clickPayButton(pg *rod.Page) {
	pg.Eval(`() => {
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
		for(const b of btns){
			const txt = b.textContent.toLowerCase().trim().replace(/\s+/g, ' ');
			if(txt.length > 50) continue;
			if(stopWords.some(sw => txt.includes(sw))) continue;
			if(txt === 'pay' || txt.startsWith('pay ') || txt.includes('pay $') || txt.includes('pay €')){
				b.click();
				return 'clicked: '+txt;
			}
		}
		const submits = document.querySelectorAll('button[type="submit"]');
		if(submits.length > 0){
			submits[submits.length-1].click();
			return 'clicked last submit';
		}
		return 'no pay button found';
	}`)
}

func checkCardValidationError(pg *rod.Page) string {
	result := safeEval(pg, `() => {
		const body = document.body ? document.body.innerText.toLowerCase() : '';
		if(body.includes('enter a valid card number') || body.includes('invalid card number')) {
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
	}`)
	return result
}

// ──────────────────────────────────────────────────────────────────
// Wait for checkout result
// ──────────────────────────────────────────────────────────────────

func waitForCheckoutResult(pg *rod.Page, logPrefix string, capMu *sync.Mutex, captures *[]paymentCapture) CheckResult {
	result := CheckResult{Status: "error"}
	resultStart := time.Now()
	deadline := time.Now().Add(35 * time.Second)
	maxDeadline := time.Now().Add(60 * time.Second)
	var totalExtended time.Duration
	lastURL := ""
	var threedsDetectedAt time.Time
	const threedsTimeout = 15 * time.Second

	const pollJS = `() => {
		const r = {url: location.href, card_err: '', errors: '', proc: 'idle'};
		const body = document.body ? document.body.innerText.toLowerCase() : '';
		if(body.includes('enter a valid card number') || body.includes('invalid card number')) {
			const all = document.querySelectorAll('p, span, div');
			for(const el of all){
				const t = el.textContent.trim().toLowerCase();
				if((t.includes('valid card number') || t.includes('invalid card number')) && t.length < 100){
					r.card_err = el.textContent.trim(); break;
				}
			}
			if(!r.card_err) r.card_err = 'Enter a valid card number';
		}
		const sels = ['[role="alert"]','.notice--error','.field__message--error','[data-error]',
			'.banner--error','.banner__content','[class*="error-message"]',
			'[class*="payment-error"]','.section__content [class*="error"]',
			'[data-step="payment_method"] [role="status"]',
			'[class*="notice"]','[class*="flash"]','.checkout-error',
			'p[class*="error"]','span[class*="error"]','div[class*="error"]'];
		const texts = new Set();
		for(const sel of sels){
			document.querySelectorAll(sel).forEach(el => {
				const t = el.textContent.trim();
				if(t && t.length > 3 && t.length < 500) texts.add(t);
			});
		}
		r.errors = Array.from(texts).join(' | ');
		const overlay = document.querySelector('[class*="processing"],[class*="loading-overlay"],[class*="spinner"]');
		if(overlay && overlay.offsetParent !== null){ r.proc = 'processing'; }
		else {
			const btn = document.querySelector('[data-pay-button],button[type="submit"],#checkout-pay-button');
			if(btn && (btn.disabled || btn.getAttribute('aria-busy')==='true')) r.proc = 'processing';
			if(document.body && document.body.classList.contains('order-summary--is-loading')) r.proc = 'processing';
		}
		return JSON.stringify(r);
	}`

	type pollState struct {
		URL     string `json:"url"`
		CardErr string `json:"card_err"`
		Errors  string `json:"errors"`
		Proc    string `json:"proc"`
	}

	for time.Now().Before(deadline) {
		raw := safeEval(pg, pollJS)
		var ps pollState
		json.Unmarshal([]byte(raw), &ps)

		if ps.URL != lastURL && ps.URL != "" {
			fmt.Printf("%s URL: %s\n", logPrefix, ps.URL)
			lastURL = ps.URL
		}

		// Success indicators in URL
		if strings.Contains(ps.URL, "thank_you") || strings.Contains(ps.URL, "thank-you") ||
			strings.Contains(ps.URL, "orders/") || strings.Contains(ps.URL, "order-confirmation") ||
			strings.Contains(ps.URL, "confirmation") {
			result.Status = "success"
			return result
		}

		// Card validation errors
		if ps.CardErr != "" {
			result.Status = "declined"
			result.ErrorCode = "INCORRECT_NUMBER"
			result.ErrorMessage = ps.CardErr
			return result
		}

		// Check broad page body text for decline/error messages
		if ps.Errors != "" {
			lower := strings.ToLower(ps.Errors)
			if strings.Contains(lower, "order") && strings.Contains(lower, "being processed") {
				// Still processing — keep waiting
			} else if strings.Contains(lower, "captcha") {
				result.Status = "declined"
				result.ErrorCode = "CARD_DECLINED"
				result.ErrorMessage = ps.Errors
				return result
			} else if strings.Contains(lower, "3d secure") || strings.Contains(lower, "3ds") {
				if threedsDetectedAt.IsZero() {
					threedsDetectedAt = time.Now()
					fmt.Printf("%s 3DS challenge detected, starting %ds timeout\n", logPrefix, int(threedsTimeout.Seconds()))
				} else if time.Since(threedsDetectedAt) > threedsTimeout {
					result.Status = "3ds"
					result.ErrorCode = "THREE_DS"
					result.ErrorMessage = "3DS challenge not resolved within timeout"
					return result
				}
			} else if strings.Contains(lower, "problem with our checkout") ||
				strings.Contains(lower, "refresh this page") ||
				strings.Contains(lower, "try again in a few minutes") ||
				strings.Contains(lower, "something went wrong") {
				// Transient Shopify message — keep waiting
			} else if strings.Contains(lower, "checkout is unavailable") {
				result.Status = "error"
				result.ErrorCode = "CHECKOUT_FAILED"
				result.ErrorMessage = ps.Errors
				return result
			} else if strings.Contains(lower, "issue processing") ||
				strings.Contains(lower, "problem processing") ||
				strings.Contains(lower, "unable to process") ||
				strings.Contains(lower, "payment method") ||
				strings.Contains(lower, "card was declined") ||
				strings.Contains(lower, "transaction failed") ||
				strings.Contains(lower, "declined") ||
				strings.Contains(lower, "not accepted") ||
				strings.Contains(lower, "insufficient funds") ||
				strings.Contains(lower, "do not honor") ||
				strings.Contains(lower, "lost card") ||
				strings.Contains(lower, "stolen card") ||
				strings.Contains(lower, "expired card") ||
				strings.Contains(lower, "invalid card") ||
				strings.Contains(lower, "card number is invalid") ||
				strings.Contains(lower, "security code is incorrect") ||
				strings.Contains(lower, "couldn't process") ||
				strings.Contains(lower, "could not process") ||
				strings.Contains(lower, "payment can't be processed") ||
				strings.Contains(lower, "session has expired") {
				result.Status = "declined"
				result.ErrorCode = "CARD_DECLINED"
				result.ErrorMessage = ps.Errors
				return result
			}
		}

		// Check CDP captures for a definitive API result
		if capMu != nil && captures != nil {
			capMu.Lock()
			capCopy := make([]paymentCapture, len(*captures))
			copy(capCopy, *captures)
			capMu.Unlock()
			if len(capCopy) > 0 {
				apiResult := parseCapturedResponses(capCopy)
				if apiResult.Status == "success" {
					result.Status = "success"
					result.OrderNumber = apiResult.OrderNumber
					fmt.Printf("%s CDP capture: success (order=%s)\n", logPrefix, apiResult.OrderNumber)
					return result
				} else if apiResult.ErrorCode != "" {
					result.Status = apiResult.Status
					result.ErrorCode = apiResult.ErrorCode
					result.ErrorMessage = apiResult.ErrorMessage
					fmt.Printf("%s CDP capture: %s (%s)\n", logPrefix, apiResult.Status, apiResult.ErrorCode)
					return result
				}
			}
		}

		if ps.Proc == "processing" {
			if !threedsDetectedAt.IsZero() && time.Since(threedsDetectedAt) > threedsTimeout {
				result.Status = "3ds"
				result.ErrorCode = "THREE_DS"
				result.ErrorMessage = "3DS challenge not resolved within timeout"
				return result
			}
			if totalExtended < 60*time.Second {
				ext := 15 * time.Second
				newDeadline := deadline.Add(ext)
				if newDeadline.Before(maxDeadline) {
					deadline = newDeadline
					totalExtended += ext
					fmt.Printf("%s Processing detected, extended deadline (+%ds total)\n", logPrefix, int(totalExtended.Seconds()))
				}
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}

		time.Sleep(500 * time.Millisecond)
	}

	// Final attempt — one last combined poll
	raw := safeEval(pg, pollJS)
	var finalPS pollState
	json.Unmarshal([]byte(raw), &finalPS)

	lowerFinal := strings.ToLower(finalPS.Errors)
	if strings.Contains(lowerFinal, "thank you") || strings.Contains(lowerFinal, "order confirmed") {
		result.Status = "success"
		return result
	}
	if strings.Contains(lowerFinal, "declined") || strings.Contains(lowerFinal, "card was declined") {
		result.Status = "declined"
		result.ErrorCode = "CARD_DECLINED"
		result.ErrorMessage = "detected decline in page text"
		return result
	}

	result.ErrorCode = "TIMEOUT"
	result.ErrorMessage = fmt.Sprintf("no result within %ds", int(time.Since(resultStart).Seconds()))
	return result
}

type apiParseResult struct {
	Status       string
	ErrorCode    string
	ErrorMessage string
	OrderNumber  string
}

func parseCapturedResponses(captures []paymentCapture) apiParseResult {
	var result apiParseResult
	for _, c := range captures {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(c.Body), &parsed); err != nil {
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

		if typeName, ok := receipt["__typename"].(string); ok {
			if typeName == "ProcessingReceipt" {
				continue
			}
		}

		if procErr, ok := receipt["processingError"].(map[string]interface{}); ok {
			code, _ := procErr["code"].(string)
			msg, _ := procErr["messageUntranslated"].(string)
			if code != "" {
				result.ErrorCode = code
				result.ErrorMessage = msg
				if code == "CAPTCHA_REQUIRED" {
					result.Status = "declined"
					result.ErrorCode = "CARD_DECLINED"
				} else {
					result.Status = "declined"
				}
				return result
			}
		}

		orderStatus, _ := receipt["orderCreationStatus"].(map[string]interface{})
		statusType := ""
		if orderStatus != nil {
			statusType, _ = orderStatus["__typename"].(string)
		}

		if statusType == "OrderCreationSucceeded" {
			result.Status = "success"
			if identity, ok := receipt["orderIdentity"].(map[string]interface{}); ok {
				if buyerID, ok := identity["buyerIdentifier"].(string); ok {
					result.OrderNumber = buyerID
				}
			}
			return result
		}

		if strings.Contains(c.Body, "CompletePaymentChallenge") ||
			strings.Contains(c.Body, "THREE_D_SECURE") ||
			strings.Contains(c.Body, "ThreeDSecure") {
			result.Status = "3ds"
			result.ErrorCode = "3DS_CHALLENGE"
			result.ErrorMessage = "3D Secure authentication required"
			return result
		}

		result.Status = "declined"
		for searchStr := c.Body; ; {
			idx := strings.Index(searchStr, `"code":"`)
			if idx < 0 {
				break
			}
			start := idx + 8
			end := strings.Index(searchStr[start:], `"`)
			if end < 0 {
				break
			}
			result.ErrorCode = searchStr[start : start+end]
			searchStr = searchStr[start+end:]
		}
		if result.ErrorCode != "" {
			return result
		}
	}
	return result
}
