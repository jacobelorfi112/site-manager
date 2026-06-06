package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	tele "gopkg.in/telebot.v4"
	"golang.org/x/net/proxy"
)

const proxyBotToken = "8356294996:AAFUVPvENTnhzQf8ylLSUovm9EpzR8IkQ1A"

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
			"🔌 *Proxy Manager*\n\n"+
				"Send proxies as text (one per line) or upload a `.txt` file.\n\n"+
				"*Supported formats:*\n"+
				"`host:port`\n"+
				"`host:port:user:pass`\n"+
				"`user:pass@host:port`\n"+
				"`(Http)host:port:user:pass`\n"+
				"`(Socks5)host:port:user:pass`\n"+
				"`http://host:port`\n"+
				"`socks5://user:pass@host:port`\n"+
				"`1.2.3.4:1234 socks5 Elite` (space format)\n\n"+
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

	bot.Handle(tele.OnText, func(c tele.Context) error {
		return handleProxies(c, bot, db, pool, strings.Split(c.Text(), "\n"))
	})

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
		return handleProxies(c, bot, db, pool, strings.Split(string(data), "\n"))
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
		return c.Send(fmt.Sprintf("❌ No valid proxies found. %d failed to parse.", invalid))
	}

	progress, _ := bot.Send(c.Chat(), fmt.Sprintf("🔄 Testing %d proxies...", len(parsed)))

	type testResult struct {
		proxy string
		ok    bool
	}
	results := make([]testResult, len(parsed))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 30)
	for i, p := range parsed {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, px string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = testResult{proxy: px, ok: testProxyConnectivity(px) == nil}
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

	added := 0
	if len(working) > 0 {
		added = db.AddProxies(working)
		pool.Reload(db)
	}

	reply := fmt.Sprintf("✅ Working: %d\n❌ Dead: %d\n💾 New in pool: %d", len(working), dead, added)
	if invalid > 0 {
		reply += fmt.Sprintf("\n⚠️ Unparseable: %d", invalid)
	}
	reply += fmt.Sprintf("\n\n🔌 Pool total: %d", len(db.GetWorkingProxies()))

	if progress != nil {
		bot.Edit(progress, reply)
		return nil
	}
	return c.Send(reply)
}

// parseProxy normalizes any common proxy format to a URL string.
func parseProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty")
	}

	// Strip leading emoji and non-ASCII flag characters (e.g. "AR 🌐 ")
	raw = stripLeadingGarbage(raw)
	if raw == "" {
		return "", fmt.Errorf("empty after strip")
	}

	// Space-separated format: "1.2.3.4:1234 socks5 Elite 1.652"
	if strings.Contains(raw, " ") {
		return parseSpaceSeparated(raw)
	}

	// Detect and strip (Http) / (Https) / (Socks5) / (Socks4) prefix
	scheme := "http"
	lower := strings.ToLower(raw)
	for _, tok := range []string{"(socks5)", "(socks4)", "(https)", "(http)"} {
		if strings.HasPrefix(lower, tok) {
			switch tok {
			case "(socks5)":
				scheme = "socks5"
			case "(socks4)":
				scheme = "socks4"
			case "(https)":
				scheme = "https"
			case "(http)":
				scheme = "http"
			}
			raw = raw[len(tok):]
			lower = strings.ToLower(raw)
			break
		}
	}

	// Already has scheme
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "socks5://") || strings.HasPrefix(lower, "socks4://") {
		u, err := url.ParseRequestURI(raw)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("invalid URL: %s", raw)
		}
		return raw, nil
	}

	// user:pass@host:port
	if idx := strings.LastIndex(raw, "@"); idx > 0 {
		userpass := raw[:idx]
		hostport := raw[idx+1:]
		colonIdx := strings.Index(userpass, ":")
		if colonIdx >= 0 {
			user := userpass[:colonIdx]
			pass := userpass[colonIdx+1:]
			return buildProxyURL(scheme, hostport, user, pass)
		}
		return scheme + "://" + raw, nil
	}

	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		// host:port
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return "", fmt.Errorf("invalid port: %s", parts[1])
		}
		return scheme + "://" + raw, nil
	case 4:
		// host:port:user:pass  — most common proxy list format
		host, port, user, pass := parts[0], parts[1], parts[2], parts[3]
		if _, err := strconv.Atoi(port); err != nil {
			return "", fmt.Errorf("invalid port: %s", port)
		}
		return buildProxyURL(scheme, host+":"+port, user, pass)
	case 3:
		// host:port:pass or ambiguous — use host:port
		if _, err := strconv.Atoi(parts[1]); err == nil {
			return scheme + "://" + parts[0] + ":" + parts[1], nil
		}
		return "", fmt.Errorf("unrecognized 3-part format: %s", raw)
	default:
		return "", fmt.Errorf("unrecognized format: %s", raw)
	}
}

// buildProxyURL constructs a proxy URL with proper encoding of user/pass.
func buildProxyURL(scheme, hostport, user, pass string) (string, error) {
	u := &url.URL{Scheme: scheme, Host: hostport}
	if user != "" || pass != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.String(), nil
}

// parseSpaceSeparated handles formats like "AR 🌐 1.2.3.4:1234 socks5 Elite 1.652"
func parseSpaceSeparated(raw string) (string, error) {
	fields := strings.Fields(raw)
	scheme := "http"
	hostport := ""

	for _, f := range fields {
		switch strings.ToLower(f) {
		case "socks5":
			scheme = "socks5"
		case "socks4":
			scheme = "socks4"
		case "https":
			scheme = "https"
		case "http":
			scheme = "http"
		}
		// Detect host:port — must have exactly one colon and numeric port
		if strings.Count(f, ":") == 1 {
			parts := strings.SplitN(f, ":", 2)
			if _, err := strconv.Atoi(parts[1]); err == nil && isASCII(parts[0]) {
				hostport = f
			}
		}
	}

	if hostport == "" {
		return "", fmt.Errorf("no host:port found in space-separated: %s", raw)
	}
	return scheme + "://" + hostport, nil
}

// stripLeadingGarbage removes leading country codes, emoji, and whitespace.
func stripLeadingGarbage(s string) string {
	var b strings.Builder
	started := false
	for _, r := range s {
		if !started {
			// Skip leading emoji, flags, spaces, and short ASCII country codes until we hit a digit or hostname
			if unicode.IsLetter(r) && r < 128 && !started {
				// Might be start of a hostname — keep going but check next
				b.WriteRune(r)
				continue
			}
			if unicode.IsDigit(r) || r == '(' {
				started = true
				b.WriteRune(r)
				continue
			}
			if r == ' ' || r > 127 {
				// Reset — preceding ASCII letters were probably a country code like "AR"
				b.Reset()
				continue
			}
			// hostname character
			started = true
			b.WriteRune(r)
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// testProxyConnectivity checks if the proxy can reach Shopify.
// Handles HTTP, HTTPS, SOCKS4, and SOCKS5.
func testProxyConnectivity(proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case "socks5", "socks4":
		return testSocksProxy(u)
	default:
		return testHTTPProxy(u)
	}
}

func testHTTPProxy(u *url.URL) error {
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get("https://shopify.com")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func testSocksProxy(u *url.URL) error {
	var auth *proxy.Auth
	if u.User != nil {
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: pass}
	}
	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 12 * time.Second})
	if err != nil {
		return err
	}
	conn, err := dialer.Dial("tcp", "shopify.com:443")
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
