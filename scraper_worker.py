#!/usr/bin/env python3
"""
Scraper Worker — Finds Shopify sites via Yahoo & Brave search and stores them
directly in PostgreSQL for the checker service to process later.

Environment variables:
  DATABASE_URL          — PostgreSQL connection string (required)
  SCRAPER_BATCH_SIZE    — URLs to buffer before inserting (default: 50)
  SCRAPER_SEARCHES      — Search iterations per cycle (default: 200)
  SCRAPER_CYCLE_DELAY   — Seconds to sleep between cycles (default: 30)
"""

import os
import re
import time
import random

import urllib3
import psycopg2
import psycopg2.extras
import requests
from bs4 import BeautifulSoup

urllib3.disable_warnings()

# ── Config ──────────────────────────────────────────────────────────
DATABASE_URL        = os.environ.get("DATABASE_URL", "")
BATCH_SIZE          = int(os.environ.get("SCRAPER_BATCH_SIZE", "50"))
SEARCHES_PER_CYCLE  = int(os.environ.get("SCRAPER_SEARCHES", "200"))
CYCLE_DELAY         = int(os.environ.get("SCRAPER_CYCLE_DELAY", "30"))

# ── Search engines (Yahoo + Brave — confirmed working) ───────────────
ENGINES = [
    {
        "name": "Yahoo",
        "url": "https://search.yahoo.com/search",
        "param": "p",
        "weight": 0.5,
    },
    {
        "name": "Brave",
        "url": "https://search.brave.com/search",
        "param": "q",
        "weight": 0.5,
    },
]

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:122.0) Gecko/20100101 Firefox/122.0",
]

DORKS = [
    'site:myshopify.com',
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
        normalized = normalize_url(m.group(0))
        if normalized:
            urls.add(normalized)
    return urls


# ── Search ──────────────────────────────────────────────────────────
def search_engine(session: requests.Session, engine: dict, query: str) -> set[str]:
    params = {engine["param"]: query}
    headers = {
        "User-Agent": random.choice(USER_AGENTS),
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        "Accept-Language": "en-US,en;q=0.5",
        "Accept-Encoding": "gzip, deflate, br",
        "DNT": "1",
        "Connection": "keep-alive",
    }
    try:
        resp = session.get(
            engine["url"],
            params=params,
            headers=headers,
            timeout=15,
            allow_redirects=True,
        )
        if resp.status_code != 200:
            return set()
        return extract_shopify_urls(resp.text)
    except Exception:
        return set()


def scrape_cycle(num_searches: int) -> set[str]:
    found: set[str] = set()
    session = requests.Session()
    session.verify = False

    weights = [e["weight"] for e in ENGINES]

    for i in range(num_searches):
        engine = random.choices(ENGINES, weights=weights, k=1)[0]
        query = random.choice(DORKS)

        urls = search_engine(session, engine, query)
        if urls:
            new = urls - found
            if new:
                found.update(new)
                print(f"  [{i+1}/{num_searches}] {engine['name']} +{len(new)} (total: {len(found)})")

        time.sleep(random.uniform(1.0, 2.5))

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
    print("Scraper Worker starting")
    print(f"  Engines: {', '.join(e['name'] for e in ENGINES)}")
    print(f"  Searches per cycle: {SEARCHES_PER_CYCLE}")
    print(f"  Batch size: {BATCH_SIZE}")
    print(f"  Cycle delay: {CYCLE_DELAY}s")

    conn = connect_db()
    print("Database connected")
    ensure_schema(conn)

    with conn.cursor() as cur:
        cur.execute("SELECT url FROM sites")
        seen: set[str] = {row[0] for row in cur.fetchall()}
    print(f"Loaded {len(seen)} existing URLs from DB")

    cycle = 0
    total_found = 0
    total_added = 0

    while True:
        cycle += 1
        stats = get_stats(conn)
        print(f"\n[Cycle {cycle}] DB: {stats['total']} total | {stats.get('pending',0)} pending | {stats.get('working',0)} working")
        print(f"Starting scrape ({SEARCHES_PER_CYCLE} searches across {len(ENGINES)} engines)...")

        found = scrape_cycle(SEARCHES_PER_CYCLE)
        total_found += len(found)

        new_urls = [u for u in found if u not in seen]
        seen.update(new_urls)

        if new_urls:
            added = 0
            for i in range(0, len(new_urls), BATCH_SIZE):
                batch = new_urls[i:i + BATCH_SIZE]
                n = insert_sites(conn, batch)
                added += n
            total_added += added
            print(f"Cycle {cycle} done: {len(found)} found, {added} new in DB")
        else:
            print(f"Cycle {cycle} done: {len(found)} found, 0 new (all already in DB)")

        print(f"Totals: {total_found} found, {total_added} added | sleeping {CYCLE_DELAY}s...")
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
