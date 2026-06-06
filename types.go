package main

import (
	"fmt"
	"strings"
)

// CardInfo holds parsed card details for a single check.
type CardInfo struct {
	Raw      string // original "number|MM|YY|CVV" string
	Number   string // formatted with spaces (e.g. "4553 8800 8549 8769")
	ExpMM    string
	ExpYY    string
	CVV      string
	FullName string // "FIRST LAST"
	Last4    string
}

// BuyerInfo holds the buyer / shipping profile used for checkout.
type BuyerInfo struct {
	Email       string `json:"email"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Address1    string `json:"address1"`
	Address2    string `json:"address2"`
	City        string `json:"city"`
	State       string `json:"state"`
	StateName   string `json:"stateName"`
	Zip         string `json:"zip"`
	Country     string `json:"country"`
	CountryName string `json:"countryName"`
	Phone       string `json:"phone"`
}

// CheckResult is the outcome of a single card check.
type CheckResult struct {
	Index          int     `json:"index"`
	CardLast4      string  `json:"card_last4"`
	Store          string  `json:"store"`
	Status         string  `json:"status"` // "success", "declined", "3ds", "error"
	ErrorCode      string  `json:"error_code"`
	ErrorMessage   string  `json:"error_message"`
	OrderNumber    string  `json:"order_number,omitempty"`
	CheckoutPrice  float64 `json:"checkout_price"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

// CheckRequest is the JSON body for POST /check.
type CheckRequest struct {
	Buyer *BuyerInfo `json:"buyer"` // nil → use defaults
	Cards []string   `json:"cards"` // "number|MM|YY|CVV|https://store.myshopify.com"
}

// CheckResponse is the JSON response for POST /check.
type CheckResponse struct {
	Total          int           `json:"total"`
	Completed      int           `json:"completed"`
	ElapsedSeconds float64       `json:"elapsed_seconds"`
	Results        []CheckResult `json:"results"`
}

// CardEntry is a parsed card+store pair from the request.
type CardEntry struct {
	Card  CardInfo
	Store string
	Index int
}

// DefaultBuyer returns the hardcoded default buyer profile.
func DefaultBuyer() BuyerInfo {
	return BuyerInfo{
		Email:       "jamesanderson2242@gmail.com",
		FirstName:   "JAMES",
		LastName:    "ANDERSON",
		Address1:    "111-55 166th St",
		Address2:    "",
		City:        "Jamaica",
		State:       "NY",
		StateName:   "New York",
		Zip:         "11433",
		Country:     "US",
		CountryName: "United States",
		Phone:       "9175551234",
	}
}

// ParseCardEntry parses "number|MM|YY|CVV" or "number|MM|YY|CVV|storeURL" into a CardEntry.
// If storeURL is omitted, Store will be empty and must be assigned by the caller.
func ParseCardEntry(raw string, index int) (CardEntry, error) {
	parts := splitCardParts(raw)
	if len(parts) < 4 {
		return CardEntry{}, fmt.Errorf("invalid format: expected number|MM|YY|CVV[|storeURL], got %d parts", len(parts))
	}
	num := strings.TrimSpace(parts[0])
	mm := strings.TrimSpace(parts[1])
	yy := strings.TrimSpace(parts[2])
	cvv := strings.TrimSpace(parts[3])
	store := ""
	if len(parts) >= 5 {
		store = strings.TrimSpace(strings.Join(parts[4:], "|"))
	}

	// Format card number with spaces
	var spaced []string
	for i := 0; i < len(num); i += 4 {
		end := i + 4
		if end > len(num) {
			end = len(num)
		}
		spaced = append(spaced, num[i:end])
	}

	last4 := num
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}

	return CardEntry{
		Card: CardInfo{
			Raw:      raw,
			Number:   strings.Join(spaced, " "),
			ExpMM:    mm,
			ExpYY:    yy,
			CVV:      cvv,
			FullName: "", // set by caller with buyer info
			Last4:    last4,
		},
		Store: store,
		Index: index,
	}, nil
}

// splitCardParts splits on "|" but keeps the store URL intact even if it contains "|".
// Format: number|MM|YY|CVV|storeURL
func splitCardParts(s string) []string {
	// Split into exactly 5 parts: the first 4 pipe-separated fields + everything after the 4th pipe
	parts := make([]string, 0, 5)
	remaining := s
	for i := 0; i < 4; i++ {
		idx := strings.Index(remaining, "|")
		if idx < 0 {
			parts = append(parts, remaining)
			return parts
		}
		parts = append(parts, remaining[:idx])
		remaining = remaining[idx+1:]
	}
	parts = append(parts, remaining)
	return parts
}
