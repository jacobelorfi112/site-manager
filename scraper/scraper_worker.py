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
import psycopg2
import psycopg2.extras
import requests

urllib3.disable_warnings()

# ── Config ──────────────────────────────────────────────────────────
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


# ── Database ─────────────────────────────────────────────────────────

def connect_db():
    if not DATABASE_URL:
        raise RuntimeError("DATABASE_URL environment variable is not set")
    conn = psycopg2.connect(DATABASE_URL)
    conn.autocommit = False
    return conn


def ensure_schema(conn):
    with conn.cursor() as cur:
        cur.execute("""
            CREATE TABLE IF NOT EXISTS sites (
                id             BIGSERIAL PRIMARY KEY,
                url            TEXT NOT NULL UNIQUE,
                status         TEXT NOT NULL DEFAULT 'pending',
                error_code     TEXT NOT NULL DEFAULT '',
                error_msg      TEXT NOT NULL DEFAULT '',
                checkout_price NUMERIC(10,2) NOT NULL DEFAULT 0,
                check_count    INTEGER NOT NULL DEFAULT 0,
                last_checked   TIMESTAMPTZ,
                created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
            );
            CREATE INDEX IF NOT EXISTS idx_sites_status ON sites(status);
            CREATE INDEX IF NOT EXISTS idx_sites_url    ON sites(url);
        """)
    conn.commit()


def insert_sites(conn, urls: list[str]) -> int:
    if not urls:
        return 0
    with conn.cursor() as cur:
        psycopg2.extras.execute_values(
            cur,
            "INSERT INTO sites (url) VALUES %s ON CONFLICT (url) DO NOTHING",
            [(u,) for u in urls],
            page_size=500,
        )
        added = cur.rowcount
    conn.commit()
    return added


def get_stats(conn) -> dict:
    with conn.cursor() as cur:
        cur.execute("SELECT status, COUNT(*) FROM sites GROUP BY status")
        rows = cur.fetchall()
    stats = {row[0]: row[1] for row in rows}
    stats["total"] = sum(stats.values())
    return stats


# ── Main loop ───────────────────────────────────────────────────────

def main():
    print("Scraper Worker starting", flush=True)
    print(f"  Sources: RapidDNS, HackerTarget, urlscan.io, DNSRepo, SiteDossier, CommonCrawl", flush=True)
    print(f"  Requests per cycle: {REQUESTS_PER_CYCLE}", flush=True)
    print(f"  Cycle delay: {CYCLE_DELAY}s", flush=True)

    conn = connect_db()
    print("Database connected", flush=True)
    ensure_schema(conn)

    with conn.cursor() as cur:
        cur.execute("SELECT url FROM sites")
        seen: set[str] = {row[0] for row in cur.fetchall()}
    print(f"Loaded {len(seen)} existing URLs from DB", flush=True)

    cycle = 0
    total_found = 0
    total_added = 0

    while True:
        cycle += 1
        stats = get_stats(conn)
        print(f"\n{'='*55}", flush=True)
        print(f"[Cycle {cycle}] DB: {stats['total']} total | "
              f"{stats.get('pending', 0)} pending | {stats.get('working', 0)} working", flush=True)

        found = scrape_cycle(REQUESTS_PER_CYCLE, seen)
        total_found += len(found)

        new_urls = [u for u in found if u not in seen]
        seen.update(new_urls)

        added = 0
        for i in range(0, len(new_urls), BATCH_SIZE):
            added += insert_sites(conn, new_urls[i:i + BATCH_SIZE])
        total_added += added

        print(f"\n[Cycle {cycle}] Done — {len(found)} found, {added} new in DB", flush=True)
        print(f"All-time: {total_found} found, {total_added} added | sleeping {CYCLE_DELAY}s...", flush=True)
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
