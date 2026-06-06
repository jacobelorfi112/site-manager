#!/usr/bin/env python3
"""
Scraper Worker — Finds Shopify sites via Yahoo & Brave and stores them
directly in PostgreSQL for the checker service to process later.

Environment variables:
  DATABASE_URL          — PostgreSQL connection string (required)
  SCRAPER_BATCH_SIZE    — URLs to buffer before inserting (default: 50)
  SCRAPER_SEARCHES      — Search iterations per cycle (default: 100)
  SCRAPER_CYCLE_DELAY   — Seconds to sleep between cycles (default: 30)
  SCRAPER_MIN_DELAY     — Min seconds between requests (default: 1.5)
  SCRAPER_MAX_DELAY     — Max seconds between requests (default: 3.0)
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
DATABASE_URL        = os.environ.get("DATABASE_URL", "")
BATCH_SIZE          = int(os.environ.get("SCRAPER_BATCH_SIZE", "50"))
SEARCHES_PER_CYCLE  = int(os.environ.get("SCRAPER_SEARCHES", "100"))
CYCLE_DELAY         = int(os.environ.get("SCRAPER_CYCLE_DELAY", "30"))
MIN_DELAY           = float(os.environ.get("SCRAPER_MIN_DELAY", "1.5"))
MAX_DELAY           = float(os.environ.get("SCRAPER_MAX_DELAY", "3.0"))

# ── Search engines ───────────────────────────────────────────────────
ENGINES = [
    {
        "name": "Yahoo",
        "url": "https://search.yahoo.com/search",
        "param": "p",
        "weight": 0.55,
        "extra_params": {},
    },
    {
        "name": "Brave",
        "url": "https://search.brave.com/search",
        "param": "q",
        "weight": 0.45,
        "extra_params": {},
    },
]

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
]

DORKS = [
    'site:myshopify.com store',
    'site:myshopify.com shop',
    'site:myshopify.com buy now',
    'site:myshopify.com products',
    'site:myshopify.com collection',
    'site:myshopify.com checkout',
    'site:myshopify.com sale',
    'site:myshopify.com clothing',
    'site:myshopify.com shoes',
    'site:myshopify.com jewelry',
    'site:myshopify.com accessories',
    'site:myshopify.com beauty',
    'site:myshopify.com skincare',
    'site:myshopify.com makeup',
    'site:myshopify.com electronics',
    'site:myshopify.com fitness',
    'site:myshopify.com supplements',
    'site:myshopify.com pet supplies',
    'site:myshopify.com home decor',
    'site:myshopify.com candles',
    'site:myshopify.com handmade',
    'site:myshopify.com vintage',
    'site:myshopify.com fashion',
    'site:myshopify.com kids',
    'site:myshopify.com outdoor',
    'site:myshopify.com art',
    'site:myshopify.com coffee',
    'site:myshopify.com food',
    'site:myshopify.com gift',
    'site:myshopify.com cheap',
    'site:myshopify.com affordable',
    'site:myshopify.com discount',
    'site:myshopify.com free shipping',
    'site:myshopify.com bundle',
    'site:myshopify.com new arrivals',
    'site:myshopify.com best seller',
    'site:myshopify.com trending',
    'site:myshopify.com luxury',
    'site:myshopify.com organic',
    'site:myshopify.com',
]

# ── URL extraction ──────────────────────────────────────────────────
MYSHOPIFY_RE = re.compile(r"https?://([a-z0-9\-]+)\.myshopify\.com", re.IGNORECASE)


def normalize_url(raw: str) -> str | None:
    m = MYSHOPIFY_RE.search(raw)
    if not m:
        return None
    store = m.group(1).lower().strip("-")
    if len(store) < 3:
        return None
    return f"https://{store}.myshopify.com"


def extract_shopify_urls(text: str) -> set[str]:
    urls = set()
    for m in MYSHOPIFY_RE.finditer(text):
        n = normalize_url(m.group(0))
        if n:
            urls.add(n)
    return urls


# ── Search ──────────────────────────────────────────────────────────
def search_engine(session: requests.Session, engine: dict, query: str) -> tuple[set[str], int, str]:
    """Returns (urls_found, http_status, error_message)."""
    params = {engine["param"]: query, **engine["extra_params"]}
    headers = {
        "User-Agent": random.choice(USER_AGENTS),
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        "Accept-Language": "en-US,en;q=0.5",
        "Accept-Encoding": "gzip, deflate, br",
        "DNT": "1",
        "Connection": "keep-alive",
        "Upgrade-Insecure-Requests": "1",
    }
    try:
        resp = session.get(
            engine["url"],
            params=params,
            headers=headers,
            timeout=15,
            allow_redirects=True,
        )
        status = resp.status_code
        if status == 429:
            return set(), status, "rate limited"
        if status == 403:
            return set(), status, "blocked"
        if status != 200:
            return set(), status, f"HTTP {status}"
        urls = extract_shopify_urls(resp.text)
        return urls, status, ""
    except requests.exceptions.Timeout:
        return set(), 0, "timeout"
    except requests.exceptions.ConnectionError as e:
        return set(), 0, f"connection error: {e}"
    except Exception as e:
        return set(), 0, f"error: {e}"


# Rate-limit backoff state per engine
_backoff: dict[str, float] = {}


def scrape_cycle(num_searches: int) -> set[str]:
    found: set[str] = set()
    session = requests.Session()
    session.verify = False
    weights = [e["weight"] for e in ENGINES]
    consecutive_failures = {e["name"]: 0 for e in ENGINES}

    for i in range(1, num_searches + 1):
        # Pick engine, skip if in backoff
        available = [e for e in ENGINES if time.time() >= _backoff.get(e["name"], 0)]
        if not available:
            wait = min(_backoff.get(e["name"], 0) for e in ENGINES) - time.time()
            print(f"  [{i}/{num_searches}] All engines in backoff, waiting {wait:.0f}s...", flush=True)
            time.sleep(max(wait, 1))
            available = ENGINES

        avail_weights = [e["weight"] for e in available]
        engine = random.choices(available, weights=avail_weights, k=1)[0]
        query = random.choice(DORKS)

        urls, status, err = search_engine(session, engine, query)

        if err:
            consecutive_failures[engine["name"]] += 1
            fails = consecutive_failures[engine["name"]]
            if status == 429 or fails >= 3:
                backoff_secs = min(60 * fails, 300)
                _backoff[engine["name"]] = time.time() + backoff_secs
                print(f"  [{i}/{num_searches}] {engine['name']} | {err} — backing off {backoff_secs}s", flush=True)
            else:
                print(f"  [{i}/{num_searches}] {engine['name']} | query='{query}' -> {err}", flush=True)
        else:
            consecutive_failures[engine["name"]] = 0
            new = urls - found
            found.update(new)
            if new:
                print(f"  [{i}/{num_searches}] {engine['name']} | '{query}' -> +{len(new)} new URLs (cycle total: {len(found)})", flush=True)
            else:
                print(f"  [{i}/{num_searches}] {engine['name']} | '{query}' -> {len(urls)} URLs (all seen)", flush=True)

        delay = random.uniform(MIN_DELAY, MAX_DELAY)
        time.sleep(delay)

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
    print(f"  Engines: {', '.join(e['name'] for e in ENGINES)}", flush=True)
    print(f"  Searches per cycle: {SEARCHES_PER_CYCLE}", flush=True)
    print(f"  Request delay: {MIN_DELAY}–{MAX_DELAY}s", flush=True)
    print(f"  Cycle delay: {CYCLE_DELAY}s", flush=True)
    sys.stdout.flush()

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
        print(f"[Cycle {cycle}] DB: {stats['total']} total | {stats.get('pending',0)} pending | {stats.get('working',0)} working", flush=True)
        print(f"Starting {SEARCHES_PER_CYCLE} searches...", flush=True)

        found = scrape_cycle(SEARCHES_PER_CYCLE)
        total_found += len(found)

        new_urls = [u for u in found if u not in seen]
        seen.update(new_urls)

        added = 0
        if new_urls:
            for i in range(0, len(new_urls), BATCH_SIZE):
                n = insert_sites(conn, new_urls[i:i + BATCH_SIZE])
                added += n
            total_added += added

        print(f"\n[Cycle {cycle}] Done — found {len(found)}, {added} new in DB", flush=True)
        print(f"All-time: {total_found} found, {total_added} added to DB", flush=True)
        print(f"Sleeping {CYCLE_DELAY}s before next cycle...", flush=True)
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
