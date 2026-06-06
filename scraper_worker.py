#!/usr/bin/env python3
"""
Scraper Worker — Discovers Shopify stores via RapidDNS + HackerTarget
(subdomain enumeration APIs that work from datacenter IPs) and stores
them directly in PostgreSQL for the checker service to process later.

Environment variables:
  DATABASE_URL          — PostgreSQL connection string (required)
  SCRAPER_BATCH_SIZE    — URLs to buffer before inserting (default: 100)
  SCRAPER_REQUESTS      — API requests per cycle (default: 20)
  SCRAPER_CYCLE_DELAY   — Seconds between cycles (default: 15)
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
REQUESTS_PER_CYCLE = int(os.environ.get("SCRAPER_REQUESTS", "20"))
CYCLE_DELAY        = int(os.environ.get("SCRAPER_CYCLE_DELAY", "15"))

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
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


# ── Sources ──────────────────────────────────────────────────────────

def fetch_rapiddns(session: requests.Session, page: int) -> tuple[set[str], str]:
    """
    RapidDNS returns ~100 different myshopify subdomains per page,
    with almost no overlap between pages — behaves like random sampling.
    """
    url = f"https://rapiddns.io/subdomain/myshopify.com?full=1&page={page}#result"
    try:
        resp = session.get(url, timeout=15, allow_redirects=True,
                           headers={"User-Agent": random.choice(USER_AGENTS)})
        if resp.status_code != 200:
            return set(), f"HTTP {resp.status_code}"
        stores = extract_stores(resp.text)
        return stores, ""
    except Exception as e:
        return set(), str(e)


def fetch_hackertarget(session: requests.Session) -> tuple[set[str], str]:
    """HackerTarget hostsearch returns ~50 myshopify subdomains."""
    url = "https://api.hackertarget.com/hostsearch/?q=myshopify.com"
    try:
        resp = session.get(url, timeout=15, allow_redirects=True,
                           headers={"User-Agent": random.choice(USER_AGENTS)})
        if resp.status_code != 200:
            return set(), f"HTTP {resp.status_code}"
        if "API count exceeded" in resp.text or "error" in resp.text.lower()[:50]:
            return set(), f"rate limited: {resp.text[:80]}"
        stores = extract_stores(resp.text)
        return stores, ""
    except Exception as e:
        return set(), str(e)


def fetch_commoncrawl(session: requests.Session, index: str, page: int) -> tuple[set[str], str]:
    """CommonCrawl index API — supplementary source."""
    url = f"https://index.commoncrawl.org/{index}-index?url=*.myshopify.com&output=json&limit=100&page={page}"
    try:
        resp = session.get(url, timeout=20, allow_redirects=True,
                           headers={"User-Agent": random.choice(USER_AGENTS)})
        if resp.status_code == 404:
            return set(), f"index not found"
        if resp.status_code != 200:
            return set(), f"HTTP {resp.status_code}"
        stores = extract_stores(resp.text)
        return stores, ""
    except Exception as e:
        return set(), str(e)


COMMONCRAWL_INDEXES = [
    "CC-MAIN-2024-51",
    "CC-MAIN-2024-46",
    "CC-MAIN-2024-42",
    "CC-MAIN-2024-38",
    "CC-MAIN-2024-33",
    "CC-MAIN-2024-26",
]


def scrape_cycle(num_requests: int, seen: set[str]) -> set[str]:
    found: set[str] = set()
    session = requests.Session()
    session.verify = False

    ht_done = False
    rapiddns_page = random.randint(1, 200)
    cc_index_idx = 0
    cc_page = 0

    for i in range(1, num_requests + 1):
        # Rotate sources: HackerTarget once, then alternate RapidDNS / CommonCrawl
        if not ht_done:
            stores, err = fetch_hackertarget(session)
            source = "HackerTarget"
            ht_done = True
        elif i % 4 == 0 and cc_index_idx < len(COMMONCRAWL_INDEXES):
            index = COMMONCRAWL_INDEXES[cc_index_idx % len(COMMONCRAWL_INDEXES)]
            stores, err = fetch_commoncrawl(session, index, cc_page)
            source = f"CommonCrawl/{index}/p{cc_page}"
            cc_page += 1
            if cc_page > 10:
                cc_page = 0
                cc_index_idx += 1
        else:
            stores, err = fetch_rapiddns(session, rapiddns_page)
            source = f"RapidDNS/p{rapiddns_page}"
            rapiddns_page = random.randint(1, 500)

        if err:
            print(f"  [{i}/{num_requests}] {source} -> ERROR: {err}", flush=True)
        else:
            new = stores - found - seen
            found.update(stores)
            print(f"  [{i}/{num_requests}] {source} -> {len(stores)} stores, +{len(new)} new", flush=True)

        time.sleep(random.uniform(0.5, 1.5))

    return found


# ── Database ─────────────────────────────────────────────────────────

def connect_db() -> psycopg2.extensions.connection:
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
    print(f"  Sources: RapidDNS, HackerTarget, CommonCrawl", flush=True)
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
