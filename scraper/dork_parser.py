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
import json
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
    """Plain urllib GET — only used as fallback. Prefer curl_cffi when available."""
    req = ur.Request(url, headers=HEADERS)
    r = ur.urlopen(req, context=SSLC, timeout=TIMEOUT)
    return r.status, r.read().decode('utf-8', 'replace')


def curl_get(url, engine_label=''):
    """Browser-impersonated GET via curl_cffi. Returns (html, status_str)."""
    global _brave_idx
    if not CURL_OK:
        st, html = http_get(url)
        return html, 'OK' if st == 200 else 'HTTP %d' % st
    prof = BRAVE_PROFILES[_brave_idx % len(BRAVE_PROFILES)]
    _brave_idx += 1
    try:
        r = curl_requests.get(url, impersonate=prof, timeout=15)
        if r.status_code == 200:
            return r.text, 'OK'
        if r.status_code == 429:
            return '', '429'
        return '', 'HTTP %d' % r.status_code
    except Exception as e:
        return '', '%s' % e.__class__.__name__


def b64_decode_url(s):
    s = s + '=' * (-len(s) % 4)
    try:
        return base64.urlsafe_b64decode(s).decode('utf-8', 'replace')
    except Exception:
        return ''


def parse_bing(html):
    """Extract organic result URLs from a Bing results page."""
    out = []
    # Bing wraps result URLs in base64 redirect links (u=a1... / u=a2...).
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
    # Fallback: any direct http(s) href in cite tags (Bing's visible URLs).
    for href in re.findall(r'<cite[^>]*>([^<]+)</cite>', html):
        href = href.strip()
        if href.startswith('http'):
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
_brave_cooldown_until = 0  # timestamp — skip Brave until this time
BRAVE_COOLDOWN_SECS = int(os.environ.get('BRAVE_COOLDOWN_SECS', '30'))  # 30s cooldown when all proxies 429

# ── Proxy pool (tested working for Brave HTTPS) ─────────────────────
PROXY_API_URL = os.environ.get('PROXY_API_URL', 'https://proxy-manager-t69d.onrender.com/proxies?limit=10000')
_tested_proxies = []  # only proxies that passed the Brave HTTPS test
_proxy_idx = 0
_proxy_bad = set()
_proxy_last_test = 0
PROXY_RETEST_INTERVAL = 600  # re-test all proxies every 10 min

try:
    from concurrent.futures import ThreadPoolExecutor, as_completed
    _THREADS_AVAILABLE = True
except ImportError:
    _THREADS_AVAILABLE = False


def _test_single_proxy(proxy):
    """Test if a proxy can reach Brave (HTTPS). Returns proxy string if OK, None if not."""
    try:
        r = curl_requests.get('https://search.brave.com/search?q=test&source=web',
                              impersonate='chrome120', timeout=6,
                              proxies={'http': f'http://{proxy}', 'https': f'http://{proxy}'})
        if r.status_code == 200:
            return proxy
    except Exception:
        pass
    return None


def test_all_proxies():
    """Fetch ALL proxies from API, test each against Brave in parallel.
    Only ~0.2% of public proxies can tunnel HTTPS to Brave without being 429'd."""
    global _tested_proxies, _proxy_bad, _proxy_last_test
    try:
        r = curl_requests.get(PROXY_API_URL, timeout=30)
        if r.status_code != 200:
            print('[proxy] API fetch failed', flush=True)
            return False
        all_proxies = [l.strip() for l in r.text.strip().split('\n') if l.strip()]
    except Exception as e:
        print(f'[proxy] API fetch error: {e}', flush=True)
        return False

    print(f'[proxy] testing {len(all_proxies)} proxies for Brave HTTPS...', flush=True)
    good = []
    if _THREADS_AVAILABLE:
        with ThreadPoolExecutor(max_workers=20) as ex:
            futures = {ex.submit(_test_single_proxy, p): p for p in all_proxies}
            done = 0
            for fut in as_completed(futures):
                result = fut.result()
                if result:
                    good.append(result)
                    print(f'[proxy] FOUND working: {result}', flush=True)
                done += 1
                if done % 1000 == 0:
                    print(f'[proxy] ...{done}/{len(all_proxies)} tested ({len(good)} working)', flush=True)
    else:
        for i, p in enumerate(all_proxies):
            result = _test_single_proxy(p)
            if result:
                good.append(result)
                print(f'[proxy] FOUND working: {result}', flush=True)
            if (i + 1) % 1000 == 0:
                print(f'[proxy] ...{i+1}/{len(all_proxies)} tested ({len(good)} working)', flush=True)

    _tested_proxies = good
    _proxy_bad = set()
    _proxy_last_test = time.time()
    print(f'[proxy] {len(good)} working proxies for Brave out of {len(all_proxies)}', flush=True)
    return len(good) > 0


def get_next_proxy():
    """Get next good proxy from tested pool (round-robin, skip bad ones)."""
    global _proxy_idx, _tested_proxies, _proxy_last_test
    # Only re-test if it's been > PROXY_RETEST_INTERVAL since last full test
    if not _tested_proxies:
        test_all_proxies()
    elif (time.time() - _proxy_last_test > PROXY_RETEST_INTERVAL) and not _proxy_bad:
        test_all_proxies()
    if not _tested_proxies:
        return None
    good = [p for p in _tested_proxies if p not in _proxy_bad]
    if not good:
        # All tested proxies bad — DON'T re-test (too slow). Return None
        # and let fetch_brave use direct connection + cooldown.
        return None
    proxy = good[_proxy_idx % len(good)]
    _proxy_idx += 1
    return proxy


def mark_proxy_bad(proxy):
    """Mark a proxy as bad (429'd or dead)."""
    _proxy_bad.add(proxy)


def parse_brave(html):
    """Extract result URLs from a Brave results page."""
    out = []
    for m in re.finditer(r'<a[^>]+href="([^"]+)"', html):
        u = m.group(1)
        if u.startswith('http'):
            out.append(u)
    return out


# ── NeoSearch engine ────────────────────────────────────────────────
_neosearch_xsrf_token = ''
_neosearch_token_fetched = 0
NEOSEARCH_TOKEN_REFRESH = 300  # refresh token every 5 min


def _neosearch_get_token():
    """Fetch XSRF token from neosearch.org homepage."""
    global _neosearch_xsrf_token, _neosearch_token_fetched
    try:
        r = curl_requests.get('https://neosearch.org/', impersonate='chrome120', timeout=15)
        m = re.search(r'<meta\s+name="xsrf-token"\s+content="([^"]+)"', r.text)
        if m:
            _neosearch_xsrf_token = m.group(1)
            _neosearch_token_fetched = time.time()
            return True
    except Exception as e:
        print(f'[neosearch] token fetch error: {e.__class__.__name__}', flush=True)
    return False


def parse_neosearch(content):
    """Parse NeoSearch newline-delimited JSON response. Extracts URLs from
    lenses → categories → links structure."""
    out = []
    for line in content.strip().split('\n'):
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except Exception:
            continue
        for lens in obj.get('lenses', []):
            for cat in lens.get('categories', []):
                for link in cat.get('links', []):
                    url = link.get('url', '')
                    if url.startswith('http'):
                        out.append(url)
    return out


def fetch_neosearch(dork, page=1):
    """NeoSearch API — no proxies needed, no 429s, returns 16-20 Shopify URLs/dork.
    Supports site: dorks. Uses XSRF token from homepage."""
    global _neosearch_xsrf_token, _neosearch_token_fetched
    if not _neosearch_xsrf_token or (time.time() - _neosearch_token_fetched > NEOSEARCH_TOKEN_REFRESH):
        if not _neosearch_get_token():
            return [], 'no-token'
    body = json.dumps({'q': dork, 'generate': 'auto', 'loc': None})
    referer = 'https://neosearch.org/?q=' + up.quote(dork)
    headers = {
        'X-XSRF-TOKEN': _neosearch_xsrf_token,
        'Origin': 'https://neosearch.org',
        'Referer': referer,
        'Accept': '*/*',
        'Accept-Language': 'en-US,en;q=0.9',
        'Content-Type': 'application/json',
    }
    try:
        r = curl_requests.post('https://neosearch.org/search',
                              data=body, headers=headers,
                              impersonate='chrome120', timeout=20)
        if r.status_code == 200:
            return parse_neosearch(r.text), 'OK'
        if r.status_code == 403 or r.status_code == 502:
            # Cloudflare block — refresh token and retry once
            if _neosearch_get_token():
                headers['X-XSRF-TOKEN'] = _neosearch_xsrf_token
                r2 = curl_requests.post('https://neosearch.org/search',
                                        data=body, headers=headers,
                                        impersonate='chrome120', timeout=20)
                if r2.status_code == 200:
                    return parse_neosearch(r2.text), 'OK'
            return [], 'CF-%d' % r.status_code
        return [], 'HTTP %d' % r.status_code
    except Exception as e:
        return [], '%s' % e.__class__.__name__


def fetch_brave(dork, page=1):
    """Brave via curl_cffi with tested proxy rotation. Direct fallback if no proxies."""
    global _brave_idx, _brave_cooldown_until
    if time.time() < _brave_cooldown_until:
        return [], 'cooldown'
    url = 'https://search.brave.com/search?q=' + up.quote(dork) + '&source=web'
    if page > 1:
        url += '&offset=%d' % ((page - 1) * 20)
    prof = BRAVE_PROFILES[_brave_idx % len(BRAVE_PROFILES)]
    _brave_idx += 1

    # Try up to 3 tested proxies before falling back to direct
    for attempt in range(3):
        proxy = get_next_proxy()
        if not proxy:
            break  # no tested proxies — try direct below
        proxies_dict = {'http': f'http://{proxy}', 'https': f'http://{proxy}'}
        try:
            r = curl_requests.get(url, impersonate=prof, timeout=15, proxies=proxies_dict)
            if r.status_code == 200:
                return parse_brave(r.text), 'OK'
            if r.status_code == 429:
                mark_proxy_bad(proxy)
                continue  # rotate to next proxy
            return [], 'HTTP %d' % r.status_code
        except Exception:
            mark_proxy_bad(proxy)
            continue

    # Direct connection as last resort (Render's IP, will likely 429)
    try:
        r = curl_requests.get(url, impersonate=prof, timeout=15)
        if r.status_code == 200:
            return parse_brave(r.text), 'OK'
        if r.status_code == 429:
            _brave_cooldown_until = time.time() + BRAVE_COOLDOWN_SECS
            # Clear bad proxies so they get retried after cooldown
            _proxy_bad.clear()
            return [], '429'
        return [], 'HTTP %d' % r.status_code
    except Exception as e:
        return [], '%s' % e.__class__.__name__


PARSERS = {
    'bing': ('Bing', 'https://www.bing.com/search?q={q}&count=30&setlang=en-US&cc=US', parse_bing, 0.0),
    'duckduckgo': ('DuckDuckGo', 'https://html.duckduckgo.com/html/?q={q}', parse_ddg, 1.5),
}

ENGINE_KEYS = ['bing', 'duckduckgo']
ENGINE_LABELS = {'bing': 'Bing', 'duckduckgo': 'DuckDuckGo', 'brave': 'Brave'}

SHOPIFY_DORKS_FILE = 'shopify_dorks.txt'
SHOPIFY_HOST_SUFFIX = '.myshopify.com'
SHOPIFY_DELAY = float(os.environ.get('SHOPIFY_DELAY', '1.0'))  # seconds between dorks
MAX_PAGES = int(os.environ.get('MAX_PAGES', '0'))  # 0 = unlimited (keep going until no results)


def fetch_engine(engine, dork, page=1):
    name, url_tpl, parse_fn, delay = PARSERS[engine]
    query = dork if engine != 'bing' else dork
    url = url_tpl.format(q=up.quote(query))
    # Bing pagination: &first=30 for page 2, &first=60 for page 3, etc.
    if page > 1 and engine == 'bing':
        url += '&first=%d' % ((page - 1) * 30)
    if delay:
        time.sleep(delay)
    html, status = curl_get(url, name)
    if status != 'OK':
        return [], status
    urls = [u for u in parse_fn(html) if not any(j in u.lower() for j in JUNK)]
    return urls, status


def run_engine(engine, dork, page=1):
    """One dork on one engine, with rate-limit handling."""
    name = PARSERS[engine][0]
    last = ''
    for attempt in range(3):
        urls, status = fetch_engine(engine, dork, page=page)
        if status == 'OK':
            return urls, status
        last = status
        if '429' in status or '202' in status or '503' in status or (status and status[0] == '5'):
            time.sleep(2.5 * (attempt + 1))
        else:
            break
    return [], status if status else last


def run_engine_quick(engine, dork, page=1):
    """Single best-effort attempt (for DDG fallback - avoids long backoff stalls)."""
    urls, status = fetch_engine(engine, dork, page=page)
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
    """NeoSearch (primary, no proxy needed) -> Brave (proxy) -> Bing (fallback).
    Returns (kept_by_engine: dict, status_str)."""
    got = {e: [] for e in ENGINE_KEYS + ['brave', 'neosearch']}
    parts = []

    # NeoSearch PRIMARY: no proxies needed, no 429s, 16-20 Shopify URLs/dork.
    # Supports site: dorks. Works from any IP (including Render).
    neo_urls, neo_status = fetch_neosearch(dork)
    got['neosearch'] = _shopify_kept(neo_urls)
    if neo_status == 'OK':
        parts.append('Neo:OK(%d)' % len(got['neosearch']))
    else:
        parts.append('Neo:%s(%d)' % (neo_status, len(got['neosearch'])))

    # Brave SECONDARY: if NeoSearch found nothing, try Brave with proxy rotation
    if not any(got.values()):
        brave_urls = []
        page = 1
        while True:
            urls, status = fetch_brave(dork, page=page)
            if status == 'cooldown':
                wait_secs = int(_brave_cooldown_until - time.time())
                if wait_secs > 0:
                    print(f'    [Brave cooldown] waiting {wait_secs}s...', flush=True)
                    time.sleep(wait_secs)
                continue
            if status == 'OK' and urls:
                brave_urls.extend(urls)
            else:
                parts.append('Brave:%s(%d)' % (status, 0))
                break
            if MAX_PAGES and page >= MAX_PAGES:
                break
            if not MAX_PAGES and page >= 5:
                break
            page += 1
            time.sleep(1)
        got['brave'] = _shopify_kept(brave_urls)
        if brave_urls:
            parts.append('Brave:OK(%d)' % len(got['brave']))

    # Bing FALLBACK: only when both NeoSearch and Brave found nothing
    if not any(got.values()):
        bing_urls = []
        empty_shopify_pages = 0
        page = 1
        while True:
            urls, status = run_engine_quick('bing', dork, page=page)
            if status == 'OK' and urls:
                bing_urls.extend(urls)
                kept = _shopify_kept(urls)
                if kept:
                    empty_shopify_pages = 0
                else:
                    empty_shopify_pages += 1
                    if empty_shopify_pages >= 3:
                        break
            else:
                break
            if MAX_PAGES and page >= MAX_PAGES:
                break
            if not MAX_PAGES and page >= 20:
                break
            page += 1
            time.sleep(1)
        got['bing'] = _shopify_kept(bing_urls)
        parts.append('Bing:%s(%d)' % ('OK' if bing_urls else '0', len(got['bing'])))

    # DDG fallback — DISABLED. Re-enable with env var USE_DDG=1 if needed.
    if not any(got.values()) and os.environ.get('USE_DDG', '0') == '1':
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