#!/usr/bin/env python3
"""
Scraper Worker v3.0.0 — Dork-based Shopify store discovery via NeoSearch + Brave + Bing + DDG.
Posts results to the Cloudflare Worker site-manager API (no PostgreSQL needed).

Environment variables:
   SITE_MANAGER_URL      — CF Worker URL (optional, hardcoded fallback)
   DORKS_FILE            — path to dork file (default: shopify_dorks.txt)
   SCRAPER_CYCLE_DELAY   — seconds between cycles (default: 10)
   MAX_DORKS             — limit dorks per cycle (optional, for testing)
"""

import os
import sys
import time
import urllib.parse as up

import requests

from dork_parser import run_shopify_dork, SHOPIFY_DORKS_FILE, SHOPIFY_DELAY, MAX_PAGES

# ── Config ──────────────────────────────────────────────────────────
SCRAPER_VERSION    = "3.0.1-neosearch"
SITE_MANAGER_URL   = os.environ.get("SITE_MANAGER_URL", "https://cf-site-manager.anonchat-notlak3.workers.dev")
DORKS_FILE         = os.environ.get("DORKS_FILE", SHOPIFY_DORKS_FILE)
CYCLE_DELAY        = int(os.environ.get("SCRAPER_CYCLE_DELAY", "10"))
MAX_DORKS          = os.environ.get("MAX_DORKS", "")

_api_session: requests.Session | None = None

def _api() -> requests.Session:
    global _api_session
    if _api_session is None:
        _api_session = requests.Session()
        _api_session.headers.update({"Content-Type": "application/json", "Accept": "application/json"})
    return _api_session


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


# ── Site Manager API (replaces direct DB access) ────────────────────

def insert_sites(urls):
    if not urls:
        return 0
    r = _api().post(f"{SITE_MANAGER_URL}/sites/add", json={"urls": urls}, timeout=30)
    if r.status_code == 200:
        return r.json().get("added", 0)
    print(f"[API] /sites/add returned {r.status_code}: {r.text[:120]}", flush=True)
    return 0


def get_stats():
    try:
        r = _api().get(f"{SITE_MANAGER_URL}/sites/stats", timeout=15)
        if r.status_code == 200:
            stats = r.json().get("by_status", {})
            stats["total"] = r.json().get("total", sum(stats.values()))
            return stats
    except Exception:
        pass
    return {"total": 0, "pending": 0, "working": 0}


def get_existing_urls():
    seen = set()
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


def ensure_schema():
    """No-op — the CF Worker handles schema."""
    pass


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
    print(f"Scraper Worker v{SCRAPER_VERSION} starting", flush=True)
    print(f"  Site Manager: {SITE_MANAGER_URL}", flush=True)
    print(f"  Engines: NeoSearch (primary) + Brave (proxy) + Bing (fallback) + DDG (disabled)", flush=True)
    print(f"  Dork file: {DORKS_FILE}", flush=True)
    print(f"  Pages per dork: {MAX_PAGES or 'unlimited (until no results)'}", flush=True)
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

    dorks = load_dorks(DORKS_FILE)
    if MAX_DORKS:
        dorks = dorks[: int(MAX_DORKS)]
    print(f"Loaded {len(dorks)} dorks", flush=True)

    cycle = 0
    total_added = 0

    while True:
        cycle += 1
        stats = get_stats()
        print(f"\n{'=' * 55}", flush=True)
        print(f"[Cycle {cycle}] DB: {stats.get('total', 0)} total | "
              f"{stats.get('pending', 0)} pending | {stats.get('working', 0)} working", flush=True)

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
                added = insert_sites(list(batch))
                added_total += added
                print(f"  >> inserted batch: {added} new (total this cycle: {added_total})", flush=True)
                batch.clear()

            time.sleep(SHOPIFY_DELAY)

        # Insert any remaining
        if batch:
            added = insert_sites(list(batch))
            added_total += added

        total_added += added_total
        elapsed = time.time() - t0

        print(f"\n[Cycle {cycle}] Done in {elapsed:.1f}s — {len(found)} found, {added_total} new in DB", flush=True)
        print(f"All-time: {total_added} added | sleeping {CYCLE_DELAY}s...", flush=True)
        time.sleep(CYCLE_DELAY)


if __name__ == "__main__":
    main()
