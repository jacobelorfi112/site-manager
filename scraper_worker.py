#!/usr/bin/env python3
"""
Scraper Worker — Discovers Shopify stores via multiple free subdomain/DNS
enumeration APIs that work from datacenter IPs. Stores results directly
in PostgreSQL for the checker service to process.

Sources used (all confirmed working from servers):
  - RapidDNS    (~100 random stores/page, effectively infinite)
  - HackerTarget (~50 stores)
  - urlscan.io  (~83 stores, security scan database)
  - DNSRepo     (~150 stores, DNS history)
  - SiteDossier (~24 stores/page, web crawl index)
  - CommonCrawl (supplementary, rotates through multiple indexes)

Environment variables:
  DATABASE_URL          — PostgreSQL connection string (required)
  SCRAPER_BATCH_SIZE    — URLs to buffer before inserting (default: 100)
  SCRAPER_REQUESTS      — API requests per cycle (default: 30)
  SCRAPER_CYCLE_DELAY   — Seconds between cycles (default: 10)
"""

import os
import re
import sys
import time
import random

import urllib3
import requests

urllib3.disable_warnings()

# ── Config ──────────────────────────────────────────────────────────
SCRAPER_VERSION    = "3.0.0-cfworker"

SITE_MANAGER_URL   = "https://cf-site-manager.anonchat-notlak3.workers.dev"
DATABASE_URL       = os.environ.get("DATABASE_URL", "")
BATCH_SIZE         = int(os.environ.get("SCRAPER_BATCH_SIZE", "100"))
REQUESTS_PER_CYCLE = int(os.environ.get("SCRAPER_REQUESTS", "30"))
CYCLE_DELAY        = int(os.environ.get("SCRAPER_CYCLE_DELAY", "10"))

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:122.0) Gecko/20100101 Firefox/122.0",
]

COMMONCRAWL_INDEXES = [
    "CC-MAIN-2024-51", "CC-MAIN-2024-46", "CC-MAIN-2024-42",
    "CC-MAIN-2024-38", "CC-MAIN-2024-33", "CC-MAIN-2024-26",
]

# ── URL extraction ──────────────────────────────────────────────────
MYSHOPIFY_RE = re.compile(r"\b([a-z0-9][a-z0-9\-]{1,}[a-z0-9])\.myshopify\.com\b", re.IGNORECASE)

def extract_stores(text: str) -> set[str]:
    stores = set()
    for m in MYSHOPIFY_RE.finditer(text):
        store = m.group(1).lower().strip("-")
        if len(store) >= 3:
            stores.add(f"https://{store}.myshopify.com")
    return stores

def ua() -> str:
    return random.choice(USER_AGENTS)

# ── Sources ──────────────────────────────────────────────────────────

def fetch_rapiddns(session: requests.Session, page: int) -> tuple[set[str], str]:
    """~100 random unique stores per page. Almost no overlap between pages."""
    try:
        r = session.get(f"https://rapiddns.io/subdomain/myshopify.com?full=1&page={page}#result",
                        timeout=15, headers={"User-Agent": ua()})
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        return extract_stores(r.text), ""
    except Exception as e:
        return set(), str(e)


def fetch_hackertarget(session: requests.Session) -> tuple[set[str], str]:
    """~50 stores. Call sparingly (rate limit after ~50 requests/day)."""
    try:
        r = session.get("https://api.hackertarget.com/hostsearch/?q=myshopify.com",
                        timeout=15, headers={"User-Agent": ua()})
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        if "API count exceeded" in r.text or r.text.startswith("error"):
            return set(), f"rate limited"
        return extract_stores(r.text), ""
    except Exception as e:
        return set(), str(e)


def fetch_urlscan(session: requests.Session) -> tuple[set[str], str]:
    """~83 stores from security scan database."""
    try:
        r = session.get(
            "https://urlscan.io/api/v1/search/?q=page.domain:myshopify.com&size=100",
            timeout=15, headers={"User-Agent": ua()})
        if r.status_code == 429:
            return set(), "rate limited"
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        return extract_stores(r.text), ""
    except Exception as e:
        return set(), str(e)


def fetch_dnsrepo(session: requests.Session) -> tuple[set[str], str]:
    """~150 stores from DNS history database."""
    try:
        r = session.get("https://dnsrepo.noc.org/?domain=myshopify.com",
                        timeout=15, headers={"User-Agent": ua()})
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        return extract_stores(r.text), ""
    except Exception as e:
        return set(), str(e)


def fetch_sitedossier(session: requests.Session, page: int) -> tuple[set[str], str]:
    """~24 stores/page from web crawl index."""
    try:
        r = session.get(f"http://www.sitedossier.com/parentdomain/myshopify.com/{page}",
                        timeout=15, allow_redirects=False, headers={"User-Agent": ua()})
        if r.status_code == 302 or r.status_code == 301:
            return set(), "no more pages"
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        stores = extract_stores(r.text)
        if not stores:
            return set(), "no results"
        return stores, ""
    except Exception as e:
        return set(), str(e)


def fetch_commoncrawl(session: requests.Session, index: str, page: int) -> tuple[set[str], str]:
    """Supplementary — CommonCrawl crawl index."""
    try:
        r = session.get(
            f"https://index.commoncrawl.org/{index}-index?url=*.myshopify.com&output=json&limit=100&page={page}",
            timeout=20, headers={"User-Agent": ua()})
        if r.status_code == 404:
            return set(), "index not found"
        if r.status_code != 200:
            return set(), f"HTTP {r.status_code}"
        return extract_stores(r.text), ""
    except Exception as e:
        return set(), str(e)


# ── Cycle ────────────────────────────────────────────────────────────

def scrape_cycle(num_requests: int, seen: set[str]) -> set[str]:
    found: set[str] = set()
    session = requests.Session()
    session.verify = False

    # State for rotating sources
    ht_done = False
    urlscan_done = False
    dnsrepo_done = False
    rapiddns_page = random.randint(1, 300)
    sitedossier_page = random.randint(1, 50)
    cc_idx = 0
    cc_page = 0

    # Source weights for random selection (higher = more frequent)
    SOURCES = [
        ("RapidDNS", 40),
        ("HackerTarget", 5),
        ("urlscan", 10),
        ("DNSRepo", 10),
        ("SiteDossier", 15),
        ("CommonCrawl", 20),
    ]
    names   = [s[0] for s in SOURCES]
    weights = [s[1] for s in SOURCES]

    for i in range(1, num_requests + 1):
        source_name = random.choices(names, weights=weights, k=1)[0]

        if source_name == "HackerTarget":
            if ht_done:
                source_name = "RapidDNS"
            else:
                ht_done = True

        if source_name == "urlscan":
            if urlscan_done:
                source_name = "RapidDNS"
            else:
                urlscan_done = True

        if source_name == "DNSRepo":
            if dnsrepo_done:
                source_name = "RapidDNS"
            else:
                dnsrepo_done = True

        if source_name == "RapidDNS":
            stores, err = fetch_rapiddns(session, rapiddns_page)
            label = f"RapidDNS/p{rapiddns_page}"
            rapiddns_page = random.randint(1, 500)

        elif source_name == "HackerTarget":
            stores, err = fetch_hackertarget(session)
            label = "HackerTarget"

        elif source_name == "urlscan":
            stores, err = fetch_urlscan(session)
            label = "urlscan.io"

        elif source_name == "DNSRepo":
            stores, err = fetch_dnsrepo(session)
            label = "DNSRepo"

        elif source_name == "SiteDossier":
            stores, err = fetch_sitedossier(session, sitedossier_page)
            label = f"SiteDossier/p{sitedossier_page}"
            sitedossier_page += 1
            if sitedossier_page > 100:
                sitedossier_page = 1

        else:  # CommonCrawl
            index = COMMONCRAWL_INDEXES[cc_idx % len(COMMONCRAWL_INDEXES)]
            stores, err = fetch_commoncrawl(session, index, cc_page)
            label = f"CommonCrawl/{index}/p{cc_page}"
            cc_page += 1
            if cc_page > 15:
                cc_page = 0
                cc_idx += 1

        if err:
            print(f"  [{i}/{num_requests}] {label} -> ERROR: {err}", flush=True)
        else:
            new = stores - found - seen
            found.update(stores)
            print(f"  [{i}/{num_requests}] {label} -> {len(stores)} stores, +{len(new)} new", flush=True)

        time.sleep(random.uniform(0.3, 1.0))

    return found


# ── Site Manager API (replaces direct DB access — no DATABASE_URL needed) ──

_api_session: requests.Session | None = None

def _api() -> requests.Session:
    global _api_session
    if _api_session is None:
        _api_session = requests.Session()
        _api_session.headers.update({"Content-Type": "application/json", "Accept": "application/json"})
    return _api_session


def ensure_schema():
    """No-op — the Go site-manager handles schema migrations."""
    pass


def insert_sites(urls: list[str]) -> int:
    """POST discovered store URLs to the site-manager's /sites/add endpoint."""
    if not urls:
        return 0
    r = _api().post(f"{SITE_MANAGER_URL}/sites/add", json={"urls": urls}, timeout=30)
    if r.status_code == 200:
        return r.json().get("added", 0)
    print(f"[API] /sites/add returned {r.status_code}: {r.text[:120]}", flush=True)
    return 0


def get_stats() -> dict:
    """GET /sites/stats for the dashboard counters."""
    try:
        r = _api().get(f"{SITE_MANAGER_URL}/sites/stats", timeout=15)
        if r.status_code == 200:
            stats = r.json().get("by_status", {})
            stats["total"] = r.json().get("total", sum(stats.values()))
            return stats
    except Exception:
        pass
    return {"total": 0, "pending": 0, "working": 0}


def get_existing_urls() -> set[str]:
    """Fetch all known URLs from the site-manager to avoid re-discovery."""
    seen: set[str] = set()
    offset = 0
    while True:
        try:
            r = _api().get(f"{SITE_MANAGER_URL}/sites/working?limit=1000&offset={offset}", timeout=30)
            if r.status_code != 200:
                break
            data = r.json()
            for s in data.get("sites", []):
                seen.add(s.get("url", ""))
            if offset + 1000 >= data.get("total", 0):
                break
            offset += 1000
        except Exception:
            break
    return seen


# ── Main loop ───────────────────────────────────────────────────────

def main():
    if not SITE_MANAGER_URL:
        raise RuntimeError(
            "SITE_MANAGER_URL environment variable is not set.\n"
            "Set it to your site-manager's Railway URL, e.g.:\n"
            "  https://site-manager-production-xxxx.up.railway.app"
        )
    print(f"Scraper Worker v{SCRAPER_VERSION} starting", flush=True)
    print(f"  Site Manager: {SITE_MANAGER_URL}", flush=True)
    print(f"  Sources: RapidDNS, HackerTarget, urlscan.io, DNSRepo, SiteDossier, CommonCrawl", flush=True)
    print(f"  Requests per cycle: {REQUESTS_PER_CYCLE}", flush=True)
    print(f"  Cycle delay: {CYCLE_DELAY}s", flush=True)

    # Verify site-manager is reachable
    try:
        r = _api().get(f"{SITE_MANAGER_URL}/sites/stats", timeout=15)
        if r.status_code == 200:
            print("Site Manager connected", flush=True)
        else:
            print(f"Site Manager returned {r.status_code} — continuing anyway", flush=True)
    except Exception as e:
        print(f"Site Manager unreachable: {e} — continuing anyway", flush=True)

    ensure_schema()
    seen = get_existing_urls()
    print(f"Loaded {len(seen)} existing URLs from Site Manager", flush=True)

    cycle = 0
    total_found = 0
    total_added = 0

    while True:
        cycle += 1
        stats = get_stats()
        print(f"\n{'='*55}", flush=True)
        print(f"[Cycle {cycle}] DB: {stats.get('total', 0)} total | "
              f"{stats.get('pending', 0)} pending | {stats.get('working', 0)} working", flush=True)

        found = scrape_cycle(REQUESTS_PER_CYCLE, seen)
        total_found += len(found)

        new_urls = [u for u in found if u not in seen]
        seen.update(new_urls)

        added = 0
        for i in range(0, len(new_urls), BATCH_SIZE):
            added += insert_sites(new_urls[i:i + BATCH_SIZE])
        total_added += added

        print(f"\n[Cycle {cycle}] Done — {len(found)} found, {added} new in DB", flush=True)
        print(f"All-time: {total_found} found, {total_added} added | sleeping {CYCLE_DELAY}s...", flush=True)
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
