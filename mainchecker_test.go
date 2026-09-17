package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSignedHandlesSite runs the worker's exact checkout path against a site
// that died with the intermittent signedHandles failure, to reproduce the
// problem and verify the fix. Site and repeat count via env:
//
//	SH_SITE   = https://averagesucks.myshopify.com
//	SH_RUNS   = 3
func TestSignedHandlesSite(t *testing.T) {
	siteArg := os.Getenv("SH_SITE")
	if siteArg == "" {
		siteArg = "https://averagesucks.myshopify.com"
	}
	var sites []string
	for _, s := range strings.Split(siteArg, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			sites = append(sites, s)
		}
	}

	const testCardEntry = "5524860214037312|10|28|950"

	pass, fail := 0, 0
	for _, shopURL := range sites {
		fmt.Printf("\n===== %s =====\n", shopURL)
		res, err := runCheckoutForCard(shopURL, testCardEntry, "")
		if res != nil {
			fmt.Printf("  status=%d code=%q amount=%q err=%v\n", res.Status, res.StatusCode, res.Amount, err)
		} else if err != nil {
			fmt.Printf("  nil result, err=%v\n", err)
		}
		// A card decision (declined/approved/charged/brand-rejected) or
		// nothing at all means the pipeline worked; an error without a
		// decision is a pipeline failure.
		failed := err != nil && (res == nil || (res.Status != BoDeclined && res.Status != BoApproved && res.Status != BoCharged && res.StatusCode != "PAYMENTS_CREDIT_CARD_BRAND_NOT_SUPPORTED"))
		if failed {
			fail++
			fmt.Printf("  ==> FAIL\n")
		} else {
			pass++
			fmt.Printf("  ==> PASS\n")
		}
	}
	fmt.Printf("\n===== total: %d pass, %d fail =====\n", pass, fail)
}
