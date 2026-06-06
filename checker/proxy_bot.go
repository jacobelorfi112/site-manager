package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v4"
)

const proxyBotToken = "8356294996:AAFUVPvENTnhzQf8ylLSUovm9EpzR8IkQ1A"

// StartProxyBot runs the Telegram proxy manager bot. Call in a goroutine.
func StartProxyBot(db *DB, pool *ProxyPool) {
	pref := tele.Settings{
		Token:  proxyBotToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}
	bot, err := tele.NewBot(pref)
	if err != nil {
		log.Printf("[proxy-bot] Failed to start: %v", err)
		return
	}
	log.Println("[proxy-bot] Started")

	bot.Handle("/start", func(c tele.Context) error {
		count := len(db.GetWorkingProxies())
		return c.Send(
			"🔌 *Proxy Manager*\n\n" +
				"Send proxies as text (one per line) or upload a `.txt` file.\n\n" +
				"*Supported formats:*\n" +
				"`ip:port`\n" +
				"`ip:port:user:pass`\n" +
				"`user:pass@ip:port`\n" +
				"`http://ip:port`\n" +
				"`http://user:pass@ip:port`\n" +
				"`socks5://ip:port`\n\n" +
				fmt.Sprintf("✅ Working proxies in pool: *%d*", count),
			tele.ModeMarkdown,
		)
	})

	bot.Handle("/count", func(c tele.Context) error {
		count := len(db.GetWorkingProxies())
		return c.Send(fmt.Sprintf("✅ Working proxies in pool: %d", count))
	})

	bot.Handle("/clear", func(c tele.Context) error {
		db.ClearProxies()
		pool.Reload(db)
		return c.Send("🗑️ All proxies cleared from pool.")
	})

	// Text message → parse as proxy list
	bot.Handle(tele.OnText, func(c tele.Context) error {
		lines := strings.Split(c.Text(), "\n")
		return handleProxies(c, bot, db, pool, lines)
	})

	// .txt file upload → download and parse
	bot.Handle(tele.OnDocument, func(c tele.Context) error {
		doc := c.Message().Document
		if doc == nil {
			return nil
		}
		rc, err := bot.File(&doc.File)
		if err != nil {
			return c.Send("❌ Failed to download file: " + err.Error())
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return c.Send("❌ Failed to read file.")
		}
		lines := strings.Split(string(data), "\n")
		return handleProxies(c, bot, db, pool, lines)
	})

	bot.Start()
}

func handleProxies(c tele.Context, bot *tele.Bot, db *DB, pool *ProxyPool, lines []string) error {
	var raw []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			raw = append(raw, line)
		}
	}
	if len(raw) == 0 {
		return c.Send("❌ No proxies found in input.")
	}

	// Parse and normalize
	var parsed []string
	invalid := 0
	seen := make(map[string]bool)
	for _, r := range raw {
		norm, err := parseProxy(r)
		if err != nil {
			invalid++
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			parsed = append(parsed, norm)
		}
	}

	if len(parsed) == 0 {
		return c.Send(fmt.Sprintf("❌ No valid proxies. %d failed to parse.", invalid))
	}

	progress, _ := bot.Send(c.Chat(), fmt.Sprintf("🔄 Testing %d proxies...", len(parsed)))

	// Test all concurrently
	type testResult struct {
		proxy string
		ok    bool
	}
	results := make([]testResult, len(parsed))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 30) // max 30 concurrent tests
	for i, p := range parsed {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, proxy string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = testResult{proxy: proxy, ok: testProxyConnectivity(proxy) == nil}
		}(i, p)
	}
	wg.Wait()

	var working []string
	dead := 0
	for _, r := range results {
		if r.ok {
			working = append(working, r.proxy)
		} else {
			dead++
		}
	}

	// Save working proxies to DB and refresh pool
	added := 0
	if len(working) > 0 {
		added = db.AddProxies(working)
		pool.Reload(db)
	}

	reply := fmt.Sprintf(
		"✅ Working: %d\n❌ Dead: %d\n💾 New in pool: %d",
		len(working), dead, added,
	)
	if invalid > 0 {
		reply += fmt.Sprintf("\n⚠️ Invalid format: %d", invalid)
	}
	reply += fmt.Sprintf("\n\n🔌 Pool total: %d", len(db.GetWorkingProxies()))

	if progress != nil {
		bot.Edit(progress, reply)
		return nil
	}
	return c.Send(reply)
}

// parseProxy normalizes any common proxy format into http://[user:pass@]host:port
func parseProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty")
	}

	// Already has scheme
	if strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "https://") ||
		strings.HasPrefix(raw, "socks5://") ||
		strings.HasPrefix(raw, "socks4://") {
		u, err := url.ParseRequestURI(raw)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("invalid URL: %s", raw)
		}
		return raw, nil
	}

	// user:pass@host:port
	if strings.Contains(raw, "@") {
		return "http://" + raw, nil
	}

	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		// host:port
		return "http://" + raw, nil
	case 4:
		// host:port:user:pass  (most common format from proxy lists)
		return fmt.Sprintf("http://%s:%s@%s:%s", parts[2], parts[3], parts[0], parts[1]), nil
	case 3:
		// Could be host:port:pass or weird format — try as host:port
		return "http://" + parts[0] + ":" + parts[1], nil
	default:
		return "", fmt.Errorf("unrecognized format")
	}
}

// testProxyConnectivity verifies the proxy can reach Shopify.
func testProxyConnectivity(proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}
	resp, err := client.Get("https://shopify.com")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
