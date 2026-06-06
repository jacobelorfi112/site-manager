#!/usr/bin/env python3
"""
Scraper Worker — Finds Shopify sites via Tempest (Bing) and stores them
directly in PostgreSQL for the checker service to process later.

Environment variables:
  DATABASE_URL          — PostgreSQL connection string (required)
  TEMPEST_COUNTRY       — 2-letter country code, e.g. "US" (default: US)
  SCRAPER_BATCH_SIZE    — URLs to buffer before inserting (default: 50)
  SCRAPER_SEARCHES      — Search iterations per cycle (default: 500)
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

urllib3.disable_warnings()

# ── Config ──────────────────────────────────────────────────────────
DATABASE_URL = os.environ.get("DATABASE_URL", "")

TEMPEST_BASE = "https://search-api.global.tempest.com"
TEMPEST_V1_SEARCH = f"{TEMPEST_BASE}/v1/search/"

TEMPEST_COUNTRY = os.environ.get("TEMPEST_COUNTRY", "US") or None

IPAD_UA = (
    "Mozilla/5.0 (iPad; U; CPU OS 3_2_1 like Mac OS X; en-us) "
    "AppleWebKit/531.21.10 (KHTML, like Gecko) Mobile/7B405"
)

BATCH_SIZE          = int(os.environ.get("SCRAPER_BATCH_SIZE", "50"))
SEARCHES_PER_CYCLE  = int(os.environ.get("SCRAPER_SEARCHES", "500"))
CYCLE_DELAY         = int(os.environ.get("SCRAPER_CYCLE_DELAY", "30"))

# ── Dorks ───────────────────────────────────────────────────────────
DORKS = [
    'site:myshopify.com',
    'site:myshopify.com store',
    'site:myshopify.com shop',
    'site:myshopify.com buy',
    'site:myshopify.com products',
    'site:myshopify.com collection',
    'site:myshopify.com cart',
    'site:myshopify.com checkout',
    'site:myshopify.com new',
    'site:myshopify.com sale',
    'site:myshopify.com deals',
    'site:myshopify.com best seller',
    'site:myshopify.com trending',
    'site:myshopify.com popular',
    'site:myshopify.com gift',
    'site:myshopify.com bundle',
    'site:myshopify.com makeup',
    'site:myshopify.com cosmetics',
    'site:myshopify.com beauty',
    'site:myshopify.com skincare',
    'site:myshopify.com clothing',
    'site:myshopify.com shoes',
    'site:myshopify.com jewelry',
    'site:myshopify.com accessories',
    'site:myshopify.com electronics',
    'site:myshopify.com fitness',
    'site:myshopify.com pet',
    'site:myshopify.com home decor',
    'site:myshopify.com furniture',
    'site:myshopify.com vitamins',
    'site:myshopify.com supplements',
    'site:myshopify.com organic',
    'site:myshopify.com handmade',
    'site:myshopify.com vintage',
    'site:myshopify.com luxury',
    'site:myshopify.com fashion',
    'site:myshopify.com kids',
    'site:myshopify.com baby',
    'site:myshopify.com outdoor',
    'site:myshopify.com sports',
    'site:myshopify.com tech',
    'site:myshopify.com gadgets',
    'site:myshopify.com art',
    'site:myshopify.com candles',
    'site:myshopify.com coffee',
    'site:myshopify.com tea',
    'site:myshopify.com food',
    'site:myshopify.com snacks',
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


# ── Database ─────────────────────────────────────────────────────────
def connect_db():
    if not DATABASE_URL:
        raise RuntimeError("DATABASE_URL environment variable is not set")
    conn = psycopg2.connect(DATABASE_URL)
    conn.autocommit = False
    return conn


def ensure_schema(conn):
    """Create the sites table if it doesn't exist (matches the checker's schema)."""
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
    """Insert new URLs, ignoring duplicates. Returns count of newly added rows."""
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
    stats.setdefault("pending", 0)
    stats.setdefault("checking", 0)
    stats.setdefault("working", 0)
    stats.setdefault("dead", 0)
    stats.setdefault("error", 0)
    stats["total"] = sum(stats.values())
    return stats


# ── Tempest scraper ─────────────────────────────────────────────────
def scrape_tempest(num_searches: int = 500) -> set[str]:
    found = set()
    consecutive_failures = 0

    session = requests.Session()
    session.verify = False

    for i in range(num_searches):
        if consecutive_failures >= 30:
            print(f"  Too many failures ({consecutive_failures}), pausing 5s...")
            time.sleep(5)
            consecutive_failures = 0

        query = random.choice(DORKS)
        offset = random.choice([0, 50, 100])

        params = {"q": query, "count": "50", "offset": str(offset)}
        if TEMPEST_COUNTRY:
            params["cc"] = TEMPEST_COUNTRY
            params["mkt"] = f"en-{TEMPEST_COUNTRY}"

        headers = {
            "User-Agent": IPAD_UA,
            "Accept": "application/json",
            "Accept-Language": "en-US,en;q=0.5",
            "Accept-Encoding": "gzip, deflate, br",
        }

        try:
            r = session.get(
                TEMPEST_V1_SEARCH,
                params=params,
                headers=headers,
                timeout=30,
                allow_redirects=True,
            )

            if r.status_code != 200 or not r.content:
                consecutive_failures += 1
                time.sleep(0.2)
                continue

            data = r.json()
            web_pages = data.get("webPages", {})
            results = web_pages.get("value", [])

            page_urls: set[str] = set()
            for result in results:
                url     = result.get("url", "")
                snippet = result.get("snippet", "") + " " + result.get("name", "")
                for text in [url, snippet]:
                    page_urls.update(extract_shopify_urls(text))
            page_urls.update(extract_shopify_urls(r.text))

            if page_urls:
                consecutive_failures = 0
                new_urls = page_urls - found
                if new_urls:
                    found.update(new_urls)
                    print(f"  [{i+1}/{num_searches}] +{len(new_urls)} new (total this cycle: {len(found)})")
            else:
                consecutive_failures += 1

            time.sleep(random.uniform(0.1, 0.4))

        except Exception:
            consecutive_failures += 1
            time.sleep(0.2)

    return found


# ── Main loop ───────────────────────────────────────────────────────
def main():
    print("Scraper Worker starting")
    print(f"  Country: {TEMPEST_COUNTRY or 'Worldwide'}")
    print(f"  Searches per cycle: {SEARCHES_PER_CYCLE}")
    print(f"  Batch size: {BATCH_SIZE}")
    print(f"  Cycle delay: {CYCLE_DELAY}s")

    conn = connect_db()
    print("Database connected")
    ensure_schema(conn)
    print("Schema ready")

    # Load already-known URLs into memory to avoid redundant inserts
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
        print(
            f"\n[Cycle {cycle}] DB: {stats['total']} total | "
            f"{stats['pending']} pending | {stats['working']} working"
        )
        print(f"Starting Tempest scrape ({SEARCHES_PER_CYCLE} searches)...")

        found = scrape_tempest(SEARCHES_PER_CYCLE)
        total_found += len(found)

        # Filter out already-seen URLs before inserting
        new_urls = [u for u in found if u not in seen]
        seen.update(new_urls)

        if new_urls:
            added = 0
            for i in range(0, len(new_urls), BATCH_SIZE):
                batch = new_urls[i:i + BATCH_SIZE]
                n = insert_sites(conn, batch)
                added += n
                print(f"  Inserted batch {i//BATCH_SIZE + 1}: {n}/{len(batch)} new rows")
            total_added += added
            print(f"Cycle {cycle} done: {len(found)} found, {added} new in DB")
        else:
            print(f"Cycle {cycle} done: {len(found)} found, 0 new (all already in DB)")

        print(f"Total across all cycles: {total_found} found, {total_added} added to DB")
        print(f"Sleeping {CYCLE_DELAY}s...")
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
