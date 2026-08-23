#!/usr/bin/env python3
"""
Local Scraper — Runs Brave scraper from your PC (not rate-limited) and uploads
found Shopify URLs to the site-manager API on Render.

Usage:
  python local_scraper.py

Environment variables:
  SITE_MANAGER_URL  — site-manager API URL (default: https://site-manager-vmu7.onrender.com)
  DORKS_FILE        — path to dork file (default: shopify_dorks.txt)
  MAX_PAGES          — pages per dork (default: 5)
"""

import os
import sys
import time
import urllib.parse as up

import requests
from curl_cffi import requests as curl_requests

from dork_parser import (
    parse_brave, BRAVE_PROFILES,
)
from dork_parser import SHOPIFY_DORKS_FILE, SHOPIFY_HOST_SUFFIX

SITE_MANAGER_URL = os.environ.get('SITE_MANAGER_URL', 'https://site-manager-vmu7.onrender.com')
DORKS_FILE = os.environ.get('DORKS_FILE', SHOPIFY_DORKS_FILE)
MAX_PAGES = int(os.environ.get('MAX_PAGES', '5'))
BRAVE_DELAY = float(os.environ.get('BRAVE_DELAY', '3'))  # 3s between Brave requests


def fetch_brave_local(dork, page=1):
    """Fetch Brave results directly (local IP, not rate-limited)."""
    url = 'https://search.brave.com/search?q=' + up.quote(dork) + '&source=web'
    if page > 1:
        url += '&offset=%d' % ((page - 1) * 20)
    prof = BRAVE_PROFILES[page % len(BRAVE_PROFILES)]
    try:
        r = curl_requests.get(url, impersonate=prof, timeout=15)
        if r.status_code == 200:
            return parse_brave(r.text), 'OK'
        if r.status_code == 429:
            return [], '429'
        return [], 'HTTP %d' % r.status_code
    except Exception as e:
        return [], '%s' % e.__class__.__name__


def shopify_ok(u):
    try:
        host = up.urlparse(u).hostname or ''
    except Exception:
        return False
    return host == 'myshopify.com' or host.endswith(SHOPIFY_HOST_SUFFIX)


def norm_url(u):
    u = u.strip()
    if not u.startswith('http'):
        return ''
    return u.rstrip('/')


def extract_shopify(urls):
    return list({n for u in urls if (n := norm_url(u)) and shopify_ok(n)})


def upload_sites(urls):
    """Upload found Shopify URLs to site-manager API."""
    if not urls:
        return 0
    try:
        # site-manager has POST /sites/add endpoint for bulk insert
        resp = requests.post(f'{SITE_MANAGER_URL}/sites/add', json={'urls': urls}, timeout=30)
        if resp.status_code in (200, 201):
            data = resp.json()
            return data.get('added', 0)
        print(f'  upload error: HTTP {resp.status_code} {resp.text[:100]}', flush=True)
    except Exception as e:
        print(f'  upload error: {e}', flush=True)
    return 0


def main():
    print('=== Local Scraper (Brave, direct from your PC) ===', flush=True)
    print(f'  Upload to: {SITE_MANAGER_URL}', flush=True)
    print(f'  Dork file: {DORKS_FILE}', flush=True)
    print(f'  Max pages/dork: {MAX_PAGES}', flush=True)
    print(f'  Delay between requests: {BRAVE_DELAY}s', flush=True)

    dorks_path = DORKS_FILE
    if not os.path.isfile(dorks_path):
        print(f'Dork file not found: {dorks_path}', flush=True)
        sys.exit(1)
    with open(dorks_path, encoding='utf-8-sig', errors='replace') as fh:
        dorks = [l.strip() for l in fh if l.strip() and not l.strip().startswith('#')]
    print(f'  Loaded {len(dorks)} dorks', flush=True)

    cycle = 0
    total_uploaded = 0
    while True:
        cycle += 1
        print(f"\n[Cycle {cycle}] Running {len(dorks)} dorks...", flush=True)
        found_all = set()
        t0 = time.time()

        for di, dork in enumerate(dorks, 1):
            dork_found = set()
            for page in range(1, MAX_PAGES + 1):
                urls, status = fetch_brave_local(dork, page=page)
                if status == 'OK' and urls:
                    shopify = extract_shopify(urls)
                    dork_found.update(shopify)
                    if not shopify:
                        break  # no Shopify on this page, stop
                else:
                    break
                if page < MAX_PAGES:
                    time.sleep(BRAVE_DELAY)

            if dork_found:
                new = dork_found - found_all
                found_all.update(dork_found)
                print(f'  [{di}/{len(dorks)}] {dork[:50]:<50} +{len(new)} new (total: {len(found_all)})', flush=True)
            else:
                print(f'  [{di}/{len(dorks)}] {dork[:50]:<50} +0', flush=True)

            # Upload every 20 dorks or 100 new URLs
            if len(found_all) >= 100 or (di % 20 == 0 and found_all):
                added = upload_sites(list(found_all))
                if added:
                    total_uploaded += added
                    print(f'  >> uploaded {added} new to site-manager (all-time: {total_uploaded})', flush=True)
                found_all.clear()

            time.sleep(BRAVE_DELAY)

        # Upload remaining
        if found_all:
            added = upload_sites(list(found_all))
            total_uploaded += added

        elapsed = time.time() - t0
        print(f'\n[Cycle {cycle}] Done in {elapsed:.0f}s — uploaded {total_uploaded} total', flush=True)
        time.sleep(10)


if __name__ == '__main__':
    main()
