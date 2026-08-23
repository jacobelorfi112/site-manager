#!/usr/bin/env python3
"""
Scraper Worker — placeholder. The previous subdomain-enumeration scrapers
(RapidDNS / HackerTarget / urlscan / DNSRepo / SiteDossier / CommonCrawl)
have been removed. Replace this file with the new Shopify-site scraper.
"""

import time

def main():
    print("Scraper Worker idle — awaiting new scraper implementation", flush=True)
    while True:
        time.sleep(60)

if __name__ == "__main__":
    main()
