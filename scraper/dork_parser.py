#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Standalone Dork Parser (no bot protection / no rate limits required).

Parsers:
  [1] Bing        - no rate limit, no bot protection, ~10-30 URLs per request
  [2] DuckDuckGo  - works via plain HTTP but rate-limits on bursts; auto retry/backoff/skip
  [3] All         - run both, merge deduplicated results

Usage:
  python dork_parser.py
  -> choose parser
  -> choose dork file (or type a path)
  -> results written to results/<timestamp>_<engines>.txt
"""

import base64
import os
import re
import ssl
import sys
import time
import urllib.parse as up
import urllib.request as ur
from datetime import datetime

try:
    from curl_cffi import requests as curl_requests
    CURL_OK = True
except ImportError:
    curl_requests = None
    CURL_OK = False

try:
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')
    sys.stderr.reconfigure(encoding='utf-8', errors='replace')
except Exception:
    pass

UA = ('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 '
      '(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36')
HEADERS = {
    'User-Agent': UA,
    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
    'Accept-Language': 'en-US,en;q=0.9',
    'Accept-Encoding': 'identity',
}
TIMEOUT = 12
JUNK = (
    'bing.com', 'duckduckgo.com', 'microsoft.com', 'go.microsoft.com',
    'msn.com', 'live.com', 'windows.com', 'microsoftstore.com',
    'r.search.yahoo.com', 'search.yahoo.com', 'r.bing.com', 'bing.net',
    'creativecommons.org', 'facebook.com', 'twitter.com', 'instagram.com',
    'reddit.com', 'youtube.com', 'wikipedia.org',
)
SSLC = ssl.create_default_context()
SSLC.check_hostname = False
SSLC.verify_mode = ssl.CERT_NONE


def http_get(url):
    req = ur.Request(url, headers=HEADERS)
    r = ur.urlopen(req, context=SSLC, timeout=TIMEOUT)
    return r.status, r.read().decode('utf-8', 'replace')


def b64_decode_url(s):
    s = s + '=' * (-len(s) % 4)
    try:
        return base64.urlsafe_b64decode(s).decode('utf-8', 'replace')
    except Exception:
        return ''


def parse_bing(html):
    """Extract organic result URLs from a Bing results page."""
    out = []
    for href in re.findall(r'class="tilk"[^>]*href="([^"]+)"', html):
        m = re.search(r'u=a[12]([A-Za-z0-9+/=]+)', href)
        if m:
            u = b64_decode_url(m.group(1))
            if u.startswith('http'):
                out.append(u)
        elif href.startswith('http'):
            out.append(href)
    for href in re.findall(r'<h2[^>]*>\s*<a[^>]*href="([^"]+)"', html):
        m = re.search(r'u=a[12]([A-Za-z0-9+/=]+)', href)
        if m:
            u = b64_decode_url(m.group(1))
            if u.startswith('http'):
                out.append(u)
        elif href.startswith('http'):
            out.append(href)
    return out


def parse_ddg(html):
    """Extract result URLs from DDG html; unddg redirect links."""
    out = []
    for href in re.findall(r'class="result__a"[^>]*href="([^"]+)"', html):
        if 'duckduckgo.com/l/' in href:
            q = up.urlparse(href).query
            uddg = up.parse_qs(q).get('uddg')
            if uddg:
                u = up.unquote(uddg[0])
                if u.startswith('http'):
                    out.append(u)
        elif href.startswith('http'):
            out.append(href)
    return out


BRAVE_PROFILES = ['chrome110', 'chrome116', 'chrome120', 'edge101', 'safari15_5', 'firefox133']
_brave_idx = 0


def parse_brave(html):
    """Extract result URLs from a Brave results page."""
    out = []
    for m in re.finditer(r'<a[^>]+href="([^"]+)"', html):
        u = m.group(1)
        if u.startswith('http'):
            out.append(u)
    return out


def fetch_brave(dork):
    """Brave via curl_cffi browser impersonation; rotate profiles + 429 backoff."""
    global _brave_idx
    url = 'https://search.brave.com/search?q=' + up.quote(dork) + '&source=web'
    for attempt in range(4):
        prof = BRAVE_PROFILES[_brave_idx % len(BRAVE_PROFILES)]
        _brave_idx += 1
        try:
            r = curl_requests.get(url, impersonate=prof, timeout=15)
            if r.status_code == 200:
                return parse_brave(r.text), 'OK'
            if r.status_code == 429:
                time.sleep(5 + attempt * 7)
                continue
            return [], 'HTTP %d' % r.status_code
        except Exception as e:
            return [], '%s' % e.__class__.__name__
    return [], '429'


PARSERS = {
    'bing': ('Bing', 'https://www.bing.com/search?q={q}&count=30&setlang=en-US&cc=US', parse_bing, 0.0),
    'duckduckgo': ('DuckDuckGo', 'https://html.duckduckgo.com/html/?q={q}', parse_ddg, 1.5),
}

ENGINE_KEYS = ['bing', 'duckduckgo']
ENGINE_LABELS = {'bing': 'Bing', 'duckduckgo': 'DuckDuckGo', 'brave': 'Brave'}

SHOPIFY_DORKS_FILE = 'shopify_dorks.txt'
SHOPIFY_HOST_SUFFIX = '.myshopify.com'
SHOPIFY_DELAY = 0.25


def fetch_engine(engine, dork):
    name, url_tpl, parse_fn, delay = PARSERS[engine]
    query = dork if engine != 'bing' else dork
    url = url_tpl.format(q=up.quote(query))
    if delay:
        time.sleep(delay)
    st, html = http_get(url)
    if st != 200:
        return [], 'HTTP %d' % st
    urls = [u for u in parse_fn(html) if not any(j in u.lower() for j in JUNK)]
    return urls, 'OK'


def run_engine(engine, dork):
    """One dork on one engine, with DDG rate-limit handling."""
    name = PARSERS[engine][0]
    last = ''
    for attempt in range(3):
        try:
            urls, status = fetch_engine(engine, dork)
        except ur.HTTPError as e:
            last = 'HTTP %d' % e.code
            status = last
            urls = []
        except Exception as e:
            last = '%s' % e.__class__.__name__
            status = last
            urls = []
        if status == 'OK':
            return urls, status
        if '429' in status or '202' in status or '503' in status or '5' == status[0]:
            time.sleep(2.5 * (attempt + 1))  # rate-limited: back off and retry
        else:
            break
    return [], status if status else last


def run_engine_quick(engine, dork):
    """Single best-effort attempt (for DDG fallback - avoids long backoff stalls)."""
    try:
        urls, status = fetch_engine(engine, dork)
    except ur.HTTPError as e:
        status = 'HTTP %d' % e.code
        urls = []
    except Exception as e:
        status = '%s' % e.__class__.__name__
        urls = []
    return urls, status


def norm_url(u):
    u = u.strip()
    if not u.startswith('http'):
        return ''
    return u.rstrip('/')


def shopify_ok(u):
    try:
        host = up.urlparse(u).hostname or ''
    except Exception:
        return False
    return host == 'myshopify.com' or host.endswith(SHOPIFY_HOST_SUFFIX)


def _shopify_kept(urls):
    return [n for u in urls if (n := norm_url(u)) and shopify_ok(n)]


def run_shopify_dork(dork):
    """Bing (cheap) -> Brave (curl_cffi, site:-reliable) -> DDG fallback.
    Returns (kept_by_engine: dict, status_str)."""
    got = {e: [] for e in ENGINE_KEYS}
    parts = []
    urls, status = run_engine_quick('bing', dork)
    got['bing'] = _shopify_kept(urls)
    parts.append('Bing:%s(%d)' % (status, len(got['bing'])))
    if not any(got.values()) and CURL_OK:
        urls, status = fetch_brave(dork)
        got['brave'] = _shopify_kept(urls)
        parts.append('Brave:%s(%d)' % (status, len(got['brave'])))
    if not any(got.values()):
        urls, status = run_engine_quick('duckduckgo', dork)
        got['duckduckgo'] = _shopify_kept(urls)
        parts.append('DDG:%s(%d)' % (status, len(got['duckduckgo'])))
    return got, '  '.join(parts)


def main():
    print('=== Dork Parser ===')
    print('Parsers (no bot protection, no rate limits needed):')
    print('  [1] Bing        - no rate limit, ~10-30 URLs per request')
    print('  [2] DuckDuckGo  - works but rate-limits on bursts (auto retry/backoff)')
    print('  [3] All         - run both parsers, merge results')
    print('  [4] Shopify     - Bing + Brave (curl_cffi) + DDG fallback over shopify_dorks.txt (~3100 dorks), filtered to .myshopify.com')
    while True:
        choice = input('Choose parser (1/2/3/4 or name or "all"): ').strip().lower()
        if choice in ('1', 'bing'):
            engines = ['bing']
            break
        if choice in ('2', 'duckduckgo', 'ddg'):
            engines = ['duckduckgo']
            break
        if choice in ('3', 'all', 'a'):
            engines = ENGINE_KEYS
            break
        if choice in ('4', 'shopify'):
            engines = ['bing']
            break
        print('Invalid choice.')

    shopify_mode = 'shopify' in choice or choice == '4'
    if shopify_mode:
        if not os.path.isfile(SHOPIFY_DORKS_FILE):
            print('%s not found next to the script.' % SHOPIFY_DORKS_FILE)
            sys.exit(1)
        dork_file = SHOPIFY_DORKS_FILE
        print()
    else:
        while True:
            fsel = input('Enter dork file path: ').strip().strip('"')
            if os.path.isfile(fsel):
                dork_file = fsel
                break
            print('File not found, try again.')

    with open(dork_file, encoding='utf-8-sig', errors='replace') as fh:
        dorks = [l.strip() for l in fh
                 if l.strip() and not l.strip().startswith('#')]
    if not dorks:
        print('No dorks found in %s' % dork_file)
        sys.exit(1)
    max_d = os.environ.get('MAX_DORKS')
    if max_d:
        dorks = dorks[:int(max_d)]
    print()
    print('Parsers : %s' % ', '.join(PARSERS[e][0] for e in engines))
    print('Dork file: %s (%d dorks)' % (dork_file, len(dorks)))
    print('Running...')

    os.makedirs('results', exist_ok=True)
    stamp = datetime.now().strftime('%Y%m%d_%H%M%S')
    out_name = 'results/%s_%s.txt' % (stamp, 'shopify' if shopify_mode else '_'.join(engines))
    out_fh = open(out_name, 'w', encoding='utf-8')
    seen = set()
    results = {}
    per_engine = {e: [] for e in ENGINE_KEYS + ['brave']}
    t0 = time.time()
    try:
        for di, dork in enumerate(dorks, 1):
            line_parts = []
            kept_all = []
            if shopify_mode:
                kept_by_engine, status_str = run_shopify_dork(dork)
                line_parts.append(status_str)
                for eng, kept in kept_by_engine.items():
                    per_engine[eng].extend((n, dork) for n in kept)
                    kept_all.extend(kept)
            else:
                for engine in engines:
                    urls, status = run_engine(engine, dork)
                    kept = []
                    for u in urls:
                        n = norm_url(u)
                        if n:
                            kept.append(n)
                    kept_all.extend(kept)
                    per_engine[engine].extend((n, dork) for n in kept)
                    line_parts.append('%s:%s(%d)' % (PARSERS[engine][0], status, len(kept)))
            for n in kept_all:
                if n not in seen:
                    seen.add(n)
                    results[n] = dork
                    out_fh.write(n + '\n')
                    out_fh.flush()
            print('  [%d/%d] %-55s %s' % (di, len(dorks), dork[:55], '  '.join(line_parts)))
            if shopify_mode and di < len(dorks):
                time.sleep(SHOPIFY_DELAY)
    finally:
        out_fh.close()

    elapsed = time.time() - t0

    print()
    print('Done in %.1fs - %d unique URLs -> %s' % (elapsed, len(results), out_name))
    for engine in per_engine:
        if per_engine[engine]:
            print('  %-12s %d URLs%s' % (ENGINE_LABELS[engine], len(per_engine[engine]),
                                         ' (fallback)' if shopify_mode and engine != 'bing' else ''))


if __name__ == '__main__':
    main()