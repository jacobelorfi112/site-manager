#!/usr/bin/env python3
"""
Scraper Worker — Dork-based Shopify store discovery via Bing + Brave + DDG.
Writes results directly to PostgreSQL for the checker service to process.

Environment variables:
   DATABASE_URL          — PostgreSQL connection string (required)
   DORKS_FILE            — path to dork file (default: shopify_dorks.txt)
   SCRAPER_CYCLE_DELAY   — seconds between cycles (default: 10)
   MAX_DORKS             — limit dorks per cycle (optional, for testing)
"""

import os
import sys
import time
import urllib.parse as up

import psycopg2
import psycopg2.extras

from dork_parser import run_shopify_dork, SHOPIFY_DORKS_FILE, SHOPIFY_DELAY, MAX_PAGES

# ── Config ──────────────────────────────────────────────────────────
DATABASE_URL = os.environ.get("DATABASE_URL", "")
DORKS_FILE = os.environ.get("DORKS_FILE", SHOPIFY_DORKS_FILE)
CYCLE_DELAY = int(os.environ.get("SCRAPER_CYCLE_DELAY", "10"))
MAX_DORKS = os.environ.get("MAX_DORKS", "")


def normalize_store_url(u):
    """Extract the store root URL (https://store.myshopify.com) from any URL."""
    try:
        parsed = up.urlparse(u)
        host = parsed.hostname or ""
        if host == "myshopify.com" or host.endswith(".myshopify.com"):
            return f"https://{host}"
    except Exception:
        pass
    return ""


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


def insert_sites(conn, urls):
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


def load_dorks(path):
    if not os.path.isfile(path):
        print(f"Dork file not found: {path}", flush=True)
        sys.exit(1)
    with open(path, encoding="utf-8-sig", errors="replace") as fh:
        dorks = [l.strip() for l in fh if l.strip() and not l.strip().startswith("#")]
    if not dorks:
        print(f"No dorks found in {path}", flush=True)
        sys.exit(1)
    return dorks


def main():
    print("Scraper Worker starting (dork-based Shopify discovery)", flush=True)
    print(f"  Engines: Brave (primary, proxy-tested) + Bing (fallback) + DDG (disabled)", flush=True)
    print(f"  Dork file: {DORKS_FILE}", flush=True)
    print(f"  Pages per dork: {MAX_PAGES or 'unlimited (until no results)'}", flush=True)
    print(f"  Cycle delay: {CYCLE_DELAY}s", flush=True)

    conn = connect_db()
    print("Database connected", flush=True)
    ensure_schema(conn)

    dorks = load_dorks(DORKS_FILE)
    if MAX_DORKS:
        dorks = dorks[: int(MAX_DORKS)]
    print(f"Loaded {len(dorks)} dorks", flush=True)

    cycle = 0
    total_added = 0

    while True:
        cycle += 1
        print(f"\n{'=' * 55}", flush=True)
        print(f"[Cycle {cycle}] Running {len(dorks)} dorks...", flush=True)

        found = set()
        batch = set()
        added_total = 0
        t0 = time.time()

        for di, dork in enumerate(dorks, 1):
            try:
                kept_by_engine, status_str = run_shopify_dork(dork)
                new = 0
                for eng, urls in kept_by_engine.items():
                    for u in urls:
                        store_url = normalize_store_url(u)
                        if store_url and store_url not in found:
                            found.add(store_url)
                            batch.add(store_url)
                            new += 1
                print(f"  [{di}/{len(dorks)}] {dork[:55]:<55} {status_str}  +{new}", flush=True)
            except Exception as e:
                print(f"  [{di}/{len(dorks)}] {dork[:55]:<55} ERROR: {e}", flush=True)

            # Insert in batches during the cycle (every 50 dorks or 100 new URLs)
            if len(batch) >= 100 or (di % 50 == 0 and batch):
                added = insert_sites(conn, list(batch))
                added_total += added
                print(f"  >> inserted batch: {added} new (total this cycle: {added_total})", flush=True)
                batch.clear()

            time.sleep(SHOPIFY_DELAY)

        # Insert any remaining
        if batch:
            added = insert_sites(conn, list(batch))
            added_total += added

        total_added += added_total
        elapsed = time.time() - t0

        print(f"\n[Cycle {cycle}] Done in {elapsed:.1f}s — {len(found)} found, {added_total} new in DB", flush=True)
        print(f"All-time: {total_added} added | sleeping {CYCLE_DELAY}s...", flush=True)
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
