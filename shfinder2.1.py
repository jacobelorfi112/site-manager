#!/usr/bin/env python3

import random
import time
import re
import threading
import requests
import urllib3
import queue
import asyncio
import os
import tempfile
import argparse
import sys
import json
import urllib.parse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime

# ⚠️ SET TO False TO DISABLE CURL_CFFI AND USE STANDARD REQUESTS
USE_CURL_CFFI = False  # DISABLED - causes connection issues

# 🧦 SOCKS PROXY SUPPORT - requires PySocks
# Install with: pip install PySocks requests[socks]
try:
    import socks
    SOCKS_AVAILABLE = True
except ImportError:
    SOCKS_AVAILABLE = False
    print("⚠️ PySocks not installed. SOCKS proxies won't work.")
    print("💡 Install with: pip install PySocks requests[socks]")

# Proxyless mode toggle (can be changed via /toggle command in bot)
USE_PROXYLESS_IN_DUAL_MODE = True  # ON by default, use /toggle to turn off if IP is blocked

# Try to import curl_cffi for browser impersonation (MUCH better for bypassing detection)
try:
    from curl_cffi import requests as curl_requests
    if USE_CURL_CFFI:
        CURL_CFFI_AVAILABLE = True
        print("✅ curl_cffi loaded - browser impersonation enabled!")
    else:
        CURL_CFFI_AVAILABLE = False
        print("⚠️ curl_cffi DISABLED - using standard requests (more stable)")
except ImportError:
    curl_requests = None
    CURL_CFFI_AVAILABLE = False
    print("⚠️ curl_cffi not found - using standard requests")
    print("💡 Install with: pip install curl_cffi")

# Browser impersonation options for curl_cffi
BROWSER_IMPERSONATE = ["chrome110", "chrome116", "chrome120", "edge101", "safari15_5"]

try:
    from telegram import Update, InlineKeyboardButton, InlineKeyboardMarkup
    from telegram.ext import Application, CommandHandler, MessageHandler, CallbackQueryHandler, filters, ContextTypes
    TELEGRAM_AVAILABLE = True
except ImportError:
    TELEGRAM_AVAILABLE = False
    print("python-telegram-bot not found")
    print("💡 Install with: pip install python-telegram-bot nigga")

urllib3.disable_warnings()

BOT_TOKEN = "8532582111:AAGwbAkfcGLQBeeeuvW8FOkFkNbpuvykN24"

USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/121.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:122.0) Gecko/20100101 Firefox/122.0",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Edge/121.0.0.0",
    "Mozilla/5.0 (X11; Ubuntu; Linux x86_64) Chrome/121.0.0.0",
]

SEARCH_ENGINES = [
    {
        'name': 'Yahoo',
        'url': 'https://search.yahoo.com/search',
        'param': 'p',
        'weight': 0.35
    },
    {
        'name': 'DuckDuckGo',
        'url': 'https://html.duckduckgo.com/html/',
        'param': 'q',
        'weight': 0.25
    },
    {
        'name': 'Brave',
        'url': 'https://search.brave.com/search',
        'param': 'q',
        'weight': 0.20
    },
    {
        'name': 'SearX-1',
        'url': 'https://searx.be/search',
        'param': 'q',
        'weight': 0.10
    },
    {
        'name': 'SearX-2',
        'url': 'https://search.sapti.me/search',
        'param': 'q',
        'weight': 0.10
    }
]

PROXYLESS_ENGINES = [
    {
        'name': 'Yahoo',
        'url': 'https://search.yahoo.com/search',
        'param': 'p',
    },
]

TEMPEST_BASE = "https://search-api.global.tempest.com"
TEMPEST_V1_SEARCH = f"{TEMPEST_BASE}/v1/search/"
TEMPEST_V2_SEARCH = f"{TEMPEST_BASE}/v2/search/"

# Country filter for Tempest (Bing) results.
# Set to a 2-letter country code (e.g. "US", "GB", "CA") to restrict results,
# or set to None to get worldwide results.
TEMPEST_COUNTRY = "US"

IPAD_UA = (
    "Mozilla/5.0 (iPad; U; CPU OS 3_2_1 like Mac OS X; en-us) "
    "AppleWebKit/531.21.10 (KHTML, like Gecko) Mobile/7B405"
)

TEMPEST_WORKERS = 500

MODE_PROXY_ONLY = 'proxy'
MODE_PROXYLESS = 'proxyless'
MODE_BOTH = 'both'

# Store user's selected mode
user_modes = {}

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
    'site:myshopify.com beauty products',
    'site:myshopify.com lipstick',
    'site:myshopify.com lip gloss',
    'site:myshopify.com lip balm',
    'site:myshopify.com lip liner',
    'site:myshopify.com lip stain',
    'site:myshopify.com lip oil',
    'site:myshopify.com liquid lipstick',
    'site:myshopify.com matte lipstick',
    'site:myshopify.com nude lipstick',
    'site:myshopify.com red lipstick',
    'site:myshopify.com pink lipstick',
    'site:myshopify.com foundation',
    'site:myshopify.com liquid foundation',
    'site:myshopify.com powder foundation',
    'site:myshopify.com bb cream',
    'site:myshopify.com cc cream',
    'site:myshopify.com tinted moisturizer',
    'site:myshopify.com concealer',
    'site:myshopify.com color corrector',
    'site:myshopify.com primer',
    'site:myshopify.com face primer',
    'site:myshopify.com eye primer',
    'site:myshopify.com lip primer',
    'site:myshopify.com mascara',
    'site:myshopify.com waterproof mascara',
    'site:myshopify.com volumizing mascara',
    'site:myshopify.com lengthening mascara',
    'site:myshopify.com eyeshadow',
    'site:myshopify.com eyeshadow palette',
    'site:myshopify.com eyeshadow single',
    'site:myshopify.com neutral eyeshadow',
    'site:myshopify.com glitter eyeshadow',
    'site:myshopify.com matte eyeshadow',
    'site:myshopify.com shimmer eyeshadow',
    'site:myshopify.com eyeliner',
    'site:myshopify.com liquid eyeliner',
    'site:myshopify.com pencil eyeliner',
    'site:myshopify.com gel eyeliner',
    'site:myshopify.com winged eyeliner',
    'site:myshopify.com blush',
    'site:myshopify.com cream blush',
    'site:myshopify.com powder blush',
    'site:myshopify.com liquid blush',
    'site:myshopify.com bronzer',
    'site:myshopify.com contour',
    'site:myshopify.com contour kit',
    'site:myshopify.com contour stick',
    'site:myshopify.com highlighter',
    'site:myshopify.com liquid highlighter',
    'site:myshopify.com powder highlighter',
    'site:myshopify.com cream highlighter',
    'site:myshopify.com strobing',
    'site:myshopify.com setting spray',
    'site:myshopify.com fixing spray',
    'site:myshopify.com setting powder',
    'site:myshopify.com translucent powder',
    'site:myshopify.com baking powder',
    'site:myshopify.com eyebrow pencil',
    'site:myshopify.com eyebrow gel',
    'site:myshopify.com eyebrow pomade',
    'site:myshopify.com eyebrow powder',
    'site:myshopify.com brow kit',
    'site:myshopify.com makeup brushes',
    'site:myshopify.com brush set',
    'site:myshopify.com beauty blender',
    'site:myshopify.com makeup sponge',
    'site:myshopify.com false lashes',
    'site:myshopify.com eyelashes',
    'site:myshopify.com lash glue',
    'site:myshopify.com makeup remover',
    'site:myshopify.com micellar water',
    'site:myshopify.com cleansing oil',
    'site:myshopify.com makeup wipes',
    'site:myshopify.com face powder',
    'site:myshopify.com compact powder',
    'site:myshopify.com pressed powder',
    'site:myshopify.com loose powder',
    'site:myshopify.com cushion foundation',
    'site:myshopify.com beauty box',
    'site:myshopify.com makeup kit',
    'site:myshopify.com makeup set',
    'site:myshopify.com vegan makeup',
    'site:myshopify.com cruelty free makeup',
    'site:myshopify.com natural makeup',
    'site:myshopify.com organic makeup',
    'site:myshopify.com mineral makeup',
    'site:myshopify.com K-beauty',
    'site:myshopify.com Korean beauty',
    'site:myshopify.com Korean makeup',
    'site:myshopify.com J-beauty',
    'site:myshopify.com Japanese makeup',
    'site:myshopify.com luxury makeup',
    'site:myshopify.com professional makeup',
    'site:myshopify.com bridal makeup',
    
    'site:myshopify.com skincare',
    'site:myshopify.com skin care',
    'site:myshopify.com facial',
    'site:myshopify.com cleanser',
    'site:myshopify.com face wash',
    'site:myshopify.com foaming cleanser',
    'site:myshopify.com gel cleanser',
    'site:myshopify.com cream cleanser',
    'site:myshopify.com oil cleanser',
    'site:myshopify.com double cleansing',
    'site:myshopify.com toner',
    'site:myshopify.com essence',
    'site:myshopify.com serum',
    'site:myshopify.com face serum',
    'site:myshopify.com vitamin c serum',
    'site:myshopify.com hyaluronic acid',
    'site:myshopify.com retinol',
    'site:myshopify.com niacinamide',
    'site:myshopify.com peptide serum',
    'site:myshopify.com moisturizer',
    'site:myshopify.com face cream',
    'site:myshopify.com day cream',
    'site:myshopify.com night cream',
    'site:myshopify.com gel moisturizer',
    'site:myshopify.com cream moisturizer',
    'site:myshopify.com lotion',
    'site:myshopify.com facial oil',
    'site:myshopify.com face oil',
    'site:myshopify.com rosehip oil',
    'site:myshopify.com argan oil',
    'site:myshopify.com jojoba oil',
    'site:myshopify.com squalane',
    'site:myshopify.com eye cream',
    'site:myshopify.com under eye cream',
    'site:myshopify.com eye gel',
    'site:myshopify.com eye patches',
    'site:myshopify.com face mask',
    'site:myshopify.com sheet mask',
    'site:myshopify.com clay mask',
    'site:myshopify.com peel off mask',
    'site:myshopify.com sleeping mask',
    'site:myshopify.com overnight mask',
    'site:myshopify.com hydrating mask',
    'site:myshopify.com exfoliator',
    'site:myshopify.com scrub',
    'site:myshopify.com face scrub',
    'site:myshopify.com chemical exfoliant',
    'site:myshopify.com AHA',
    'site:myshopify.com BHA',
    'site:myshopify.com peeling solution',
    'site:myshopify.com sunscreen',
    'site:myshopify.com SPF',
    'site:myshopify.com sun protection',
    'site:myshopify.com sunblock',
    'site:myshopify.com UV protection',
    'site:myshopify.com acne treatment',
    'site:myshopify.com acne serum',
    'site:myshopify.com spot treatment',
    'site:myshopify.com pimple patch',
    'site:myshopify.com acne patches',
    'site:myshopify.com salicylic acid',
    'site:myshopify.com benzoyl peroxide',
    'site:myshopify.com tea tree oil',
    'site:myshopify.com anti aging',
    'site:myshopify.com anti wrinkle',
    'site:myshopify.com firming cream',
    'site:myshopify.com lifting serum',
    'site:myshopify.com collagen cream',
    'site:myshopify.com brightening serum',
    'site:myshopify.com whitening cream',
    'site:myshopify.com dark spot corrector',
    'site:myshopify.com pigmentation treatment',
    'site:myshopify.com vitamin C cream',
    'site:myshopify.com glow serum',
    'site:myshopify.com radiance cream',
    'site:myshopify.com pore minimizer',
    'site:myshopify.com pore refining',
    'site:myshopify.com blackhead remover',
    'site:myshopify.com nose strips',
    'site:myshopify.com charcoal mask',
    'site:myshopify.com detox mask',
    'site:myshopify.com purifying mask',
    'site:myshopify.com calming cream',
    'site:myshopify.com soothing gel',
    'site:myshopify.com sensitive skin',
    'site:myshopify.com redness relief',
    'site:myshopify.com barrier repair',
    'site:myshopify.com ceramide cream',
    'site:myshopify.com centella',
    'site:myshopify.com cica cream',
    'site:myshopify.com facial mist',
    'site:myshopify.com face spray',
    'site:myshopify.com hydrating spray',
    'site:myshopify.com rose water',
    'site:myshopify.com thermal water',
    'site:myshopify.com lip care',
    'site:myshopify.com lip mask',
    'site:myshopify.com lip scrub',
    'site:myshopify.com lip treatment',
    'site:myshopify.com neck cream',
    'site:myshopify.com hand cream',
    'site:myshopify.com foot cream',
    'site:myshopify.com body lotion',
    'site:myshopify.com body butter',
    'site:myshopify.com body oil',
    'site:myshopify.com body scrub',
    
    'site:myshopify.com perfume',
    'site:myshopify.com fragrance',
    'site:myshopify.com cologne',
    'site:myshopify.com eau de parfum',
    'site:myshopify.com eau de toilette',
    'site:myshopify.com parfum',
    'site:myshopify.com scent',
    'site:myshopify.com luxury perfume',
    'site:myshopify.com designer perfume',
    'site:myshopify.com niche perfume',
    'site:myshopify.com artisan perfume',
    'site:myshopify.com natural perfume',
    'site:myshopify.com organic perfume',
    'site:myshopify.com vegan perfume',
    'site:myshopify.com floral perfume',
    'site:myshopify.com woody perfume',
    'site:myshopify.com oriental perfume',
    'site:myshopify.com fresh perfume',
    'site:myshopify.com citrus perfume',
    'site:myshopify.com musky perfume',
    'site:myshopify.com vanilla perfume',
    'site:myshopify.com rose perfume',
    'site:myshopify.com jasmine perfume',
    'site:myshopify.com lavender perfume',
    'site:myshopify.com oud perfume',
    'site:myshopify.com amber perfume',
    'site:myshopify.com sandalwood perfume',
    'site:myshopify.com patchouli perfume',
    'site:myshopify.com bergamot perfume',
    'site:myshopify.com perfume oil',
    'site:myshopify.com fragrance oil',
    'site:myshopify.com attar',
    'site:myshopify.com essential oil',
    'site:myshopify.com aromatherapy oil',
    'site:myshopify.com body spray',
    'site:myshopify.com body mist',
    'site:myshopify.com hair mist',
    'site:myshopify.com perfume travel set',
    'site:myshopify.com perfume sample',
    'site:myshopify.com perfume discovery set',
    'site:myshopify.com candles',
    'site:myshopify.com scented candles',
    'site:myshopify.com soy candles',
    'site:myshopify.com luxury candles',
    'site:myshopify.com aromatherapy candles',
    'site:myshopify.com incense',
    'site:myshopify.com incense sticks',
    'site:myshopify.com incense cones',
    'site:myshopify.com palo santo',
    'site:myshopify.com sage',
    'site:myshopify.com smudge stick',
    'site:myshopify.com room spray',
    'site:myshopify.com air freshener',
    'site:myshopify.com reed diffuser',
    'site:myshopify.com car diffuser',
    'site:myshopify.com home fragrance',
    'site:myshopify.com wax melts',
    'site:myshopify.com potpourri',
    'site:myshopify.com sachet',
    
    'site:myshopify.com accessories',
    'site:myshopify.com bags',
    'site:myshopify.com handbags',
    'site:myshopify.com purse',
    'site:myshopify.com clutch',
    'site:myshopify.com tote bag',
    'site:myshopify.com shoulder bag',
    'site:myshopify.com crossbody bag',
    'site:myshopify.com satchel',
    'site:myshopify.com hobo bag',
    'site:myshopify.com bucket bag',
    'site:myshopify.com backpack',
    'site:myshopify.com mini backpack',
    'site:myshopify.com leather backpack',
    'site:myshopify.com travel backpack',
    'site:myshopify.com laptop bag',
    'site:myshopify.com messenger bag',
    'site:myshopify.com wallet',
    'site:myshopify.com card holder',
    'site:myshopify.com coin purse',
    'site:myshopify.com clutch wallet',
    'site:myshopify.com leather wallet',
    'site:myshopify.com belt',
    'site:myshopify.com leather belt',
    'site:myshopify.com designer belt',
    'site:myshopify.com fashion belt',
    'site:myshopify.com waist belt',
    'site:myshopify.com sunglasses',
    'site:myshopify.com shades',
    'site:myshopify.com aviator sunglasses',
    'site:myshopify.com cat eye sunglasses',
    'site:myshopify.com round sunglasses',
    'site:myshopify.com square sunglasses',
    'site:myshopify.com oversized sunglasses',
    'site:myshopify.com polarized sunglasses',
    'site:myshopify.com UV sunglasses',
    'site:myshopify.com designer sunglasses',
    'site:myshopify.com luxury sunglasses',
    'site:myshopify.com glasses',
    'site:myshopify.com eyeglasses',
    'site:myshopify.com reading glasses',
    'site:myshopify.com blue light glasses',
    'site:myshopify.com eyewear',
    'site:myshopify.com hat',
    'site:myshopify.com cap',
    'site:myshopify.com baseball cap',
    'site:myshopify.com snapback',
    'site:myshopify.com dad hat',
    'site:myshopify.com trucker hat',
    'site:myshopify.com beanie',
    'site:myshopify.com winter hat',
    'site:myshopify.com fedora',
    'site:myshopify.com bucket hat',
    'site:myshopify.com sun hat',
    'site:myshopify.com straw hat',
    'site:myshopify.com panama hat',
    'site:myshopify.com beret',
    'site:myshopify.com headwear',
    'site:myshopify.com scarf',
    'site:myshopify.com silk scarf',
    'site:myshopify.com winter scarf',
    'site:myshopify.com infinity scarf',
    'site:myshopify.com pashmina',
    'site:myshopify.com shawl',
    'site:myshopify.com wrap',
    'site:myshopify.com bandana',
    'site:myshopify.com gloves',
    'site:myshopify.com leather gloves',
    'site:myshopify.com winter gloves',
    'site:myshopify.com driving gloves',
    'site:myshopify.com mittens',
    'site:myshopify.com phone case',
    'site:myshopify.com iPhone case',
    'site:myshopify.com Samsung case',
    'site:myshopify.com phone cover',
    'site:myshopify.com phone accessories',
    'site:myshopify.com pop socket',
    'site:myshopify.com phone grip',
    'site:myshopify.com keychain',
    'site:myshopify.com key holder',
    'site:myshopify.com key ring',
    'site:myshopify.com key fob',
    'site:myshopify.com charm keychain',
    'site:myshopify.com hair accessories',
    'site:myshopify.com hair clips',
    'site:myshopify.com hair pins',
    'site:myshopify.com barrettes',
    'site:myshopify.com hair ties',
    'site:myshopify.com scrunchies',
    'site:myshopify.com headband',
    'site:myshopify.com hair band',
    'site:myshopify.com turban',
    'site:myshopify.com hair wrap',
    'site:myshopify.com hair claw',
    'site:myshopify.com bow',
    'site:myshopify.com hair bow',
    'site:myshopify.com ribbon',
    'site:myshopify.com umbrella',
    'site:myshopify.com compact umbrella',
    'site:myshopify.com rain umbrella',
    'site:myshopify.com travel umbrella',
    
    'site:myshopify.com watches',
    'site:myshopify.com watch',
    'site:myshopify.com wristwatch',
    'site:myshopify.com timepiece',
    'site:myshopify.com smart watch',
    'site:myshopify.com smartwatch',
    'site:myshopify.com fitness watch',
    'site:myshopify.com sports watch',
    'site:myshopify.com luxury watch',
    'site:myshopify.com designer watch',
    'site:myshopify.com automatic watch',
    'site:myshopify.com mechanical watch',
    'site:myshopify.com quartz watch',
    'site:myshopify.com digital watch',
    'site:myshopify.com analog watch',
    'site:myshopify.com chronograph',
    'site:myshopify.com dive watch',
    'site:myshopify.com dress watch',
    'site:myshopify.com casual watch',
    'site:myshopify.com minimalist watch',
    'site:myshopify.com vintage watch',
    'site:myshopify.com wood watch',
    'site:myshopify.com leather watch',
    'site:myshopify.com metal watch',
    'site:myshopify.com gold watch',
    'site:myshopify.com silver watch',
    'site:myshopify.com rose gold watch',
    'site:myshopify.com watch band',
    'site:myshopify.com watch strap',
    'site:myshopify.com apple watch band',
    
    'site:myshopify.com home decor',
    'site:myshopify.com wall art',
    'site:myshopify.com candles',
    'site:myshopify.com pillows',
    'site:myshopify.com blankets',
    'site:myshopify.com rugs',
    'site:myshopify.com mirrors',
    'site:myshopify.com vases',
    'site:myshopify.com planters',
    
    'site:myshopify.com budget',
    'site:myshopify.com cheap',
    'site:myshopify.com affordable',
    'site:myshopify.com discount',
    'site:myshopify.com under 5',
    'site:myshopify.com under 10',
    'site:myshopify.com under 15',
    'site:myshopify.com under 20',
    'site:myshopify.com under 25',
    'site:myshopify.com under 30',
    'site:myshopify.com dollar store',
    'site:myshopify.com dollar items',
    'site:myshopify.com bulk',
    'site:myshopify.com wholesale',
    'site:myshopify.com clearance',
    'site:myshopify.com sale',
    'site:myshopify.com on sale',
    'site:myshopify.com low price',
    'site:myshopify.com low cost',
    'site:myshopify.com inexpensive',
    'site:myshopify.com bargain',
    'site:myshopify.com steal',
    'site:myshopify.com deal',
    'site:myshopify.com promo',
    'site:myshopify.com special offer',
    'site:myshopify.com markdown',
    'site:myshopify.com liquidation',
    'site:myshopify.com outlet',
    'site:myshopify.com discount store',
    'site:myshopify.com closeout',
    'site:myshopify.com overstock',
    'site:myshopify.com under 50',
    'site:myshopify.com under 100',
    'site:myshopify.com value for money',
    'site:myshopify.com cost effective',
    'site:myshopify.com affordable quality',
    'site:myshopify.com entry level',
    'site:myshopify.com starter',
    'site:myshopify.com basic',
    'site:myshopify.com no frills',
    'site:myshopify.com everyday',
    'site:myshopify.com practical',
    'site:myshopify.com functional',
    'site:myshopify.com utility',
    'site:myshopify.com multi purpose',
    'site:myshopify.com all purpose',
    'site:myshopify.com versatile',
    'site:myshopify.com handy',
    'site:myshopify.com convenient',
    'site:myshopify.com economical',
    'site:myshopify.com value',
    'site:myshopify.com budget friendly',
    'site:myshopify.com cheap beauty',
    'site:myshopify.com affordable makeup',
    'site:myshopify.com budget skincare',
    'site:myshopify.com drugstore makeup',
    'site:myshopify.com drugstore beauty',
    'site:myshopify.com drugstore skincare',
    'site:myshopify.com cheap cosmetics',
    'site:myshopify.com affordable cosmetics',
    'site:myshopify.com budget lipstick',
    'site:myshopify.com cheap lipstick',
    'site:myshopify.com affordable foundation',
    'site:myshopify.com cheap mascara',
    'site:myshopify.com affordable eyeshadow',
    'site:myshopify.com budget eyeshadow palette',
    'site:myshopify.com cheap brushes',
    'site:myshopify.com affordable brushes',
    'site:myshopify.com budget beauty tools',
    'site:myshopify.com cheap skincare',
    'site:myshopify.com affordable serum',
    'site:myshopify.com budget moisturizer',
    'site:myshopify.com cheap face wash',
    'site:myshopify.com affordable cleanser',
    'site:myshopify.com budget mask',
    'site:myshopify.com cheap sheet mask',
    'site:myshopify.com affordable perfume',
    'site:myshopify.com cheap perfume',
    'site:myshopify.com budget fragrance',
    'site:myshopify.com affordable cologne',
    'site:myshopify.com cheap body spray',
    'site:myshopify.com budget candles',
    'site:myshopify.com cheap candles',
    'site:myshopify.com affordable candles',
    'site:myshopify.com affordable accessories',
    'site:myshopify.com cheap bags',
    'site:myshopify.com budget handbag',
    'site:myshopify.com affordable purse',
    'site:myshopify.com cheap wallet',
    'site:myshopify.com budget wallet',
    'site:myshopify.com affordable belt',
    'site:myshopify.com cheap sunglasses',
    'site:myshopify.com budget sunglasses',
    'site:myshopify.com affordable hat',
    'site:myshopify.com cheap phone case',
    'site:myshopify.com budget phone case',
    'site:myshopify.com affordable keychain',
    'site:myshopify.com cheap watch',
    'site:myshopify.com budget watch',
    'site:myshopify.com affordable watch',
    'site:myshopify.com dollar tree',
    'site:myshopify.com dollar general',
    'site:myshopify.com 99 cent',
    'site:myshopify.com under dollar',
    'site:myshopify.com sample',
    'site:myshopify.com trial size',
    'site:myshopify.com travel size',
    'site:myshopify.com mini size',
    'site:myshopify.com small size',
    'site:myshopify.com starter kit',
    'site:myshopify.com beginner kit',
    'site:myshopify.com basic kit',
    'site:myshopify.com essentials',
    'site:myshopify.com basic',
    'site:myshopify.com simple',
    'site:myshopify.com starter',
    'site:myshopify.com entry level',
    'site:myshopify.com value pack',
    'site:myshopify.com combo pack',
    'site:myshopify.com bundle deal',
    'site:myshopify.com set',
    'site:myshopify.com pack',
    'site:myshopify.com multipack',
    'site:myshopify.com value set',
    'site:myshopify.com gift set',
    'site:myshopify.com starter set',
    'site:myshopify.com sample pack',
    'site:myshopify.com variety pack',
    'site:myshopify.com mixed pack',
    'site:myshopify.com combo',
    'site:myshopify.com bundle',
    'site:myshopify.com deal',
    'site:myshopify.com special',
    'site:myshopify.com promo',
    'site:myshopify.com promotion',
    'site:myshopify.com special offer',
    'site:myshopify.com offer',
    'site:myshopify.com discount code',
    'site:myshopify.com coupon',
    'site:myshopify.com free shipping',
    'site:myshopify.com free',
    'site:myshopify.com bonus',
    'site:myshopify.com extra',
    'site:myshopify.com bonus item',
    'site:myshopify.com free gift',
    'site:myshopify.com free sample',
    'site:myshopify.com buy one get one',
    'site:myshopify.com bogo',
    'site:myshopify.com buy 2 get 1',
    'site:myshopify.com 2 for 1',
    'site:myshopify.com 3 for 2',
    'site:myshopify.com buy more save',
    'site:myshopify.com quantity discount',
    'site:myshopify.com volume discount',
    'site:myshopify.com bulk discount',
    'site:myshopify.com wholesale price',
    'site:myshopify.com wholesale bulk',
    'site:myshopify.com bulk buy',
    'site:myshopify.com buy in bulk',
    'site:myshopify.com bulk order',
    'site:myshopify.com bulk purchase',
    'site:myshopify.com case pack',
    'site:myshopify.com case',
    'site:myshopify.com dozen',
    'site:myshopify.com 12 pack',
    'site:myshopify.com 24 pack',
    'site:myshopify.com 6 pack',
    'site:myshopify.com 3 pack',
    'site:myshopify.com 2 pack',
    'site:myshopify.com twin pack',
    'site:myshopify.com double pack',
    'site:myshopify.com triple pack',
    'site:myshopify.com economy pack',
    'site:myshopify.com economy size',
    'site:myshopify.com family size',
    'site:myshopify.com jumbo size',
    'site:myshopify.com value size',
    'site:myshopify.com refill',
    'site:myshopify.com refill pack',
    'site:myshopify.com refillable',
    'site:myshopify.com refill size',
    'site:myshopify.com replacement',
    'site:myshopify.com replacement pack',
    'site:myshopify.com generic',
    'site:myshopify.com store brand',
    'site:myshopify.com own brand',
    'site:myshopify.com private label',
    'site:myshopify.com unbranded',
    'site:myshopify.com no brand',
    'site:myshopify.com off brand',
    'site:myshopify.com alternative',
    'site:myshopify.com dupe',
    'site:myshopify.com dupe for',
    'site:myshopify.com dupe product',
    'site:myshopify.com affordable alternative',
    'site:myshopify.com budget alternative',
    'site:myshopify.com cheap alternative',
    'site:myshopify.com knockoff',
    'site:myshopify.com inspired',
    'site:myshopify.com similar',
    'site:myshopify.com comparable',
    'site:myshopify.com equivalent',
    'site:myshopify.com substitute',
    'site:myshopify.com replacement',
    'site:myshopify.com second',
    'site:myshopify.com second hand',
    'site:myshopify.com used',
    'site:myshopify.com pre owned',
    'site:myshopify.com preloved',
    'site:myshopify.com vintage',
    'site:myshopify.com retro',
    'site:myshopify.com classic',
    'site:myshopify.com basic model',
    'site:myshopify.com basic version',
    'site:myshopify.com standard',
    'site:myshopify.com standard size',
    'site:myshopify.com regular',
    'site:myshopify.com regular size',
    'site:myshopify.com normal size',
    'site:myshopify.com standard pack',
    'site:myshopify.com regular pack',
    'site:myshopify.com everyday',
    'site:myshopify.com daily',
    'site:myshopify.com daily use',
    'site:myshopify.com everyday use',
    'site:myshopify.com daily essentials',
    'site:myshopify.com everyday essentials',
    'site:myshopify.com must have',
    'site:myshopify.com essential',
    'site:myshopify.com necessity',
    'site:myshopify.com basic needs',
    'site:myshopify.com necessities',
    'site:myshopify.com staples',
    'site:myshopify.com basic items',
    'site:myshopify.com basic products',
    'site:myshopify.com simple items',
    'site:myshopify.com simple products',
    'site:myshopify.com no frills',
    'site:myshopify.com plain',
    'site:myshopify.com simple design',
    'site:myshopify.com basic design',
    'site:myshopify.com minimalist',
    'site:myshopify.com minimal',
    'site:myshopify.com simple style',
    'site:myshopify.com basic style',
    'site:myshopify.com plain style',
    'site:myshopify.com casual',
    'site:myshopify.com casual wear',
    'site:myshopify.com everyday wear',
    'site:myshopify.com daily wear',
    'site:myshopify.com practical',
    'site:myshopify.com functional',
    'site:myshopify.com utility',
    'site:myshopify.com utility item',
    'site:myshopify.com utility product',
    'site:myshopify.com practical item',
    'site:myshopify.com practical product',
    'site:myshopify.com functional item',
    'site:myshopify.com functional product',
    'site:myshopify.com useful',
    'site:myshopify.com handy',
    'site:myshopify.com convenient',
    'site:myshopify.com multipurpose',
    'site:myshopify.com multi purpose',
    'site:myshopify.com all purpose',
    'site:myshopify.com all in one',
    'site:myshopify.com 2 in 1',
    'site:myshopify.com 3 in 1',
    'site:myshopify.com 4 in 1',
    'site:myshopify.com 5 in 1',
    'site:myshopify.com combo product',
    'site:myshopify.com combination',
    'site:myshopify.com combined',
    'site:myshopify.com combined product',
    'site:myshopify.com dual purpose',
    'site:myshopify.com dual function',
    'site:myshopify.com two way',
    'site:myshopify.com reversible',
    'site:myshopify.com convertible',
    'site:myshopify.com adjustable',
    'site:myshopify.com flexible',
    'site:myshopify.com versatile',
    'site:myshopify.com multi use',
    'site:myshopify.com multi function',
    'site:myshopify.com multi tasking',
    'site:myshopify.com multi task',
    'site:myshopify.com do it all',
    'site:myshopify.com all purpose cleaner',
    'site:myshopify.com all purpose solution',
    'site:myshopify.com all purpose product',
    'site:myshopify.com universal',
    'site:myshopify.com universal fit',
    'site:myshopify.com one size',
    'site:myshopify.com one size fits all',
    'site:myshopify.com fits all',
    'site:myshopify.com universal size',
    'site:myshopify.com standard fit',
    'site:myshopify.com regular fit',
    'site:myshopify.com normal fit',
    'site:myshopify.com average size',
    'site:myshopify.com medium size',
    'site:myshopify.com medium',
    'site:myshopify.com standard medium',
    'site:myshopify.com regular medium',
    'site:myshopify.com normal medium',
    'site:myshopify.com average medium',
    'site:myshopify.com medium fit',
    'site:myshopify.com standard medium fit',
    'site:myshopify.com regular medium fit',
    'site:myshopify.com normal medium fit',
    'site:myshopify.com average medium fit',
    'site:myshopify.com small',
    'site:myshopify.com small size',
    'site:myshopify.com small pack',
    'site:myshopify.com small quantity',
    'site:myshopify.com small amount',
    'site:myshopify.com small package',
    'site:myshopify.com small box',
    'site:myshopify.com small container',
    'site:myshopify.com small bottle',
    'site:myshopify.com small tube',
    'site:myshopify.com small jar',
    'site:myshopify.com small can',
    'site:myshopify.com small bag',
    'site:myshopify.com small pouch',
    'site:myshopify.com small sachet',
    'site:myshopify.com single',
    'site:myshopify.com single item',
    'site:myshopify.com single pack',
    'site:myshopify.com single piece',
    'site:myshopify.com single unit',
    'site:myshopify.com individual',
    'site:myshopify.com individual item',
    'site:myshopify.com individual pack',
    'site:myshopify.com individual piece',
    'site:myshopify.com individual unit',
    'site:myshopify.com single serve',
    'site:myshopify.com single use',
    'site:myshopify.com one time use',
    'site:myshopify.com disposable',
    'site:myshopify.com one time',
    'site:myshopify.com one use',
    'site:myshopify.com single serving',
    'site:myshopify.com single portion',
    'site:myshopify.com individual serving',
    'site:myshopify.com individual portion',
    'site:myshopify.com portion',
    'site:myshopify.com serving',
    'site:myshopify.com serving size',
    'site:myshopify.com portion size',
    'site:myshopify.com single serving size',
    'site:myshopify.com individual serving size',
    'site:myshopify.com single portion size',
    'site:myshopify.com individual portion size',
    'site:myshopify.com pocket size',
    'site:myshopify.com pocket sized',
    'site:myshopify.com pocket friendly',
    'site:myshopify.com pocket pack',
    'site:myshopify.com pocket version',
    'site:myshopify.com portable',
    'site:myshopify.com portable size',
    'site:myshopify.com portable pack',
    'site:myshopify.com travel',
    'site:myshopify.com travel size',
    'site:myshopify.com travel sized',
    'site:myshopify.com travel pack',
    'site:myshopify.com travel friendly',
    'site:myshopify.com travel version',
    'site:myshopify.com travel ready',
    'site:myshopify.com travel ready pack',
    'site:myshopify.com travel ready size',
    'site:myshopify.com on the go',
    'site:myshopify.com on the go pack',
    'site:myshopify.com on the go size',
    'site:myshopify.com on the go version',
    'site:myshopify.com go pack',
    'site:myshopify.com go size',
    'site:myshopify.com go version',
    'site:myshopify.com go friendly',
    'site:myshopify.com go ready',
    'site:myshopify.com go ready pack',
    'site:myshopify.com go ready size',
    'site:myshopify.com go ready version',
    'site:myshopify.com compact',
    'site:myshopify.com compact size',
    'site:myshopify.com compact pack',
    'site:myshopify.com compact version',
    'site:myshopify.com compact design',
    'site:myshopify.com space saving',
    'site:myshopify.com space efficient',
    'site:myshopify.com space friendly',
    'site:myshopify.com space saving design',
    'site:myshopify.com space efficient design',
    'site:myshopify.com space friendly design',
    'site:myshopify.com space saving pack',
    'site:myshopify.com space efficient pack',
    'site:myshopify.com space friendly pack',
    'site:myshopify.com space saving size',
    'site:myshopify.com space efficient size',
    'site:myshopify.com space friendly size',
    'site:myshopify.com space saving version',
    'site:myshopify.com space efficient version',
    'site:myshopify.com space friendly version',
    'site:myshopify.com lightweight',
    'site:myshopify.com light weight',
    'site:myshopify.com light',
    'site:myshopify.com light pack',
    'site:myshopify.com light size',
    'site:myshopify.com light version',
    'site:myshopify.com light design',
    'site:myshopify.com light weight pack',
    'site:myshopify.com light weight size',
    'site:myshopify.com light weight version',
    'site:myshopify.com light weight design',
    'site:myshopify.com lightweight pack',
    'site:myshopify.com lightweight size',
    'site:myshopify.com lightweight version',
    'site:myshopify.com lightweight design',
    'site:myshopify.com featherweight',
    'site:myshopify.com feather weight',
    'site:myshopify.com ultra light',
    'site:myshopify.com ultra lightweight',
    'site:myshopify.com ultra light weight',
    'site:myshopify.com super light',
    'site:myshopify.com super lightweight',
    'site:myshopify.com super light weight',
    'site:myshopify.com extra light',
    'site:myshopify.com extra lightweight',
    'site:myshopify.com extra light weight',
    'site:myshopify.com paper',
    'site:myshopify.com paper product',
    'site:myshopify.com paper item',
    'site:myshopify.com paper pack',
    'site:myshopify.com paper box',
    'site:myshopify.com paper bag',
    'site:myshopify.com paper pouch',
    'site:myshopify.com paper container',
    'site:myshopify.com paper wrapper',
    'site:myshopify.com paper wrap',
    'site:myshopify.com paper covered',
    'site:myshopify.com paper wrapped',
    'site:myshopify.com paper package',
    'site:myshopify.com paper packaging',
    'site:myshopify.com paper packed',
    'site:myshopify.com paper packed item',
    'site:myshopify.com paper packed product',
    'site:myshopify.com paper packed pack',
    'site:myshopify.com paper packed box',
    'site:myshopify.com paper packed bag',
    'site:myshopify.com paper packed pouch',
    'site:myshopify.com paper packed container',
    'site:myshopify.com paper packed wrapper',
    'site:myshopify.com paper packed wrap',
    'site:myshopify.com paper packed covered',
    'site:myshopify.com paper packed wrapped',
    'site:myshopify.com paper packed package',
    'site:myshopify.com paper packed packaging',
    
    'site:myshopify.com stationery',
    'site:myshopify.com pens',
    'site:myshopify.com pencils',
    'site:myshopify.com notebooks',
    'site:myshopify.com paper',
    'site:myshopify.com sticky notes',
    'site:myshopify.com tape',
    'site:myshopify.com glue',
    'site:myshopify.com erasers',
    'site:myshopify.com highlighters',
    'site:myshopify.com markers',
    'site:myshopify.com cheap stationery',
    'site:myshopify.com budget stationery',
    'site:myshopify.com affordable stationery',
    'site:myshopify.com cheap pens',
    'site:myshopify.com budget notebooks',
    'site:myshopify.com cheap notebooks',
    'site:myshopify.com affordable notebooks',
    'site:myshopify.com dollar store stationery',
    'site:myshopify.com dollar store pens',
    'site:myshopify.com dollar store notebooks',
    'site:myshopify.com office supplies',
    'site:myshopify.com cheap office supplies',
    'site:myshopify.com budget office supplies',
    'site:myshopify.com affordable office supplies',
    'site:myshopify.com folders',
    'site:myshopify.com binders',
    'site:myshopify.com file organizers',
    'site:myshopify.com cheap folders',
    'site:myshopify.com budget binders',
    'site:myshopify.com affordable file organizers',
    'site:myshopify.com storage boxes',
    'site:myshopify.com cheap storage',
    'site:myshopify.com budget storage',
    'site:myshopify.com affordable storage',
    'site:myshopify.com plastic bins',
    'site:myshopify.com cheap bins',
    'site:myshopify.com budget containers',
    'site:myshopify.com affordable containers',
    'site:myshopify.com ziploc bags',
    'site:myshopify.com sandwich bags',
    'site:myshopify.com cheap bags',
    'site:myshopify.com budget bags',
    'site:myshopify.com affordable bags',
    'site:myshopify.com trash bags',
    'site:myshopify.com garbage bags',
    'site:myshopify.com cheap trash bags',
    'site:myshopify.com budget trash bags',
    'site:myshopify.com affordable trash bags',
    'site:myshopify.com cleaning supplies',
    'site:myshopify.com cheap cleaning',
    'site:myshopify.com budget cleaning',
    'site:myshopify.com affordable cleaning',
    'site:myshopify.com dish soap',
    'site:myshopify.com laundry detergent',
    'site:myshopify.com cheap detergent',
    'site:myshopify.com budget detergent',
    'site:myshopify.com affordable detergent',
    'site:myshopify.com sponges',
    'site:myshopify.com scrub brushes',
    'site:myshopify.com cheap sponges',
    'site:myshopify.com budget scrub brushes',
    'site:myshopify.com affordable scrub brushes',
    'site:myshopify.com paper towels',
    'site:myshopify.com napkins',
    'site:myshopify.com toilet paper',
    'site:myshopify.com cheap paper towels',
    'site:myshopify.com budget toilet paper',
    'site:myshopify.com affordable napkins',
    'site:myshopify.com kitchen towels',
    'site:myshopify.com dish towels',
    'site:myshopify.com cheap kitchen towels',
    'site:myshopify.com budget dish towels',
    'site:myshopify.com affordable kitchen towels',
    'site:myshopify.com dishcloths',
    'site:myshopify.com washcloths',
    'site:myshopify.com cheap washcloths',
    'site:myshopify.com budget dishcloths',
    'site:myshopify.com affordable washcloths',
    'site:myshopify.com kitchen utensils',
    'site:myshopify.com cheap utensils',
    'site:myshopify.com budget utensils',
    'site:myshopify.com affordable utensils',
    'site:myshopify.com plastic utensils',
    'site:myshopify.com disposable utensils',
    'site:myshopify.com cheap plastic utensils',
    'site:myshopify.com budget disposable utensils',
    'site:myshopify.com affordable plastic utensils',
    'site:myshopify.com cups',
    'site:myshopify.com plates',
    'site:myshopify.com bowls',
    'site:myshopify.com cheap cups',
    'site:myshopify.com budget plates',
    'site:myshopify.com affordable bowls',
    'site:myshopify.com plastic cups',
    'site:myshopify.com paper plates',
    'site:myshopify.com disposable cups',
    'site:myshopify.com disposable plates',
    'site:myshopify.com disposable bowls',
    'site:myshopify.com cheap plastic cups',
    'site:myshopify.com budget paper plates',
    'site:myshopify.com affordable disposable cups',
    'site:myshopify.com party supplies',
    'site:myshopify.com cheap party supplies',
    'site:myshopify.com budget party supplies',
    'site:myshopify.com affordable party supplies',
    'site:myshopify.com balloons',
    'site:myshopify.com streamers',
    'site:myshopify.com party decorations',
    'site:myshopify.com cheap balloons',
    'site:myshopify.com budget streamers',
    'site:myshopify.com affordable party decorations',
    'site:myshopify.com party favors',
    'site:myshopify.com cheap party favors',
    'site:myshopify.com budget party favors',
    'site:myshopify.com affordable party favors',
    'site:myshopify.com gift bags',
    'site:myshopify.com gift wrap',
    'site:myshopify.com cheap gift bags',
    'site:myshopify.com budget gift wrap',
    'site:myshopify.com affordable gift bags',
    'site:myshopify.com tissue paper',
    'site:myshopify.com wrapping paper',
    'site:myshopify.com cheap tissue paper',
    'site:myshopify.com budget wrapping paper',
    'site:myshopify.com affordable tissue paper',
    'site:myshopify.com ribbon',
    'site:myshopify.com bows',
    'site:myshopify.com cheap ribbon',
    'site:myshopify.com budget bows',
    'site:myshopify.com affordable ribbon',
    'site:myshopify.com greeting cards',
    'site:myshopify.com birthday cards',
    'site:myshopify.com cheap greeting cards',
    'site:myshopify.com budget birthday cards',
    'site:myshopify.com affordable greeting cards',
    'site:myshopify.com thank you cards',
    'site:myshopify.com cheap thank you cards',
    'site:myshopify.com budget thank you cards',
    'site:myshopify.com affordable thank you cards',
    'site:myshopify.com craft supplies',
    'site:myshopify.com cheap craft supplies',
    'site:myshopify.com budget craft supplies',
    'site:myshopify.com affordable craft supplies',
    'site:myshopify.com yarn',
    'site:myshopify.com fabric',
    'site:myshopify.com thread',
    'site:myshopify.com cheap yarn',
    'site:myshopify.com budget fabric',
    'site:myshopify.com affordable thread',
    'site:myshopify.com buttons',
    'site:myshopify.com zippers',
    'site:myshopify.com cheap buttons',
    'site:myshopify.com budget zippers',
    'site:myshopify.com affordable buttons',
    'site:myshopify.com beads',
    'site:myshopify.com charms',
    'site:myshopify.com cheap beads',
    'site:myshopify.com budget charms',
    'site:myshopify.com affordable beads',
    'site:myshopify.com stickers',
    'site:myshopify.com decals',
    'site:myshopify.com cheap stickers',
    'site:myshopify.com budget decals',
    'site:myshopify.com affordable stickers',
    'site:myshopify.com magnets',
    'site:myshopify.com pins',
    'site:myshopify.com cheap magnets',
    'site:myshopify.com budget pins',
    'site:myshopify.com affordable magnets',
    'site:myshopify.com keychains',
    'site:myshopify.com cheap keychains',
    'site:myshopify.com budget keychains',
    'site:myshopify.com affordable keychains',
    'site:myshopify.com phone accessories',
    'site:myshopify.com cheap phone accessories',
    'site:myshopify.com budget phone accessories',
    'site:myshopify.com affordable phone accessories',
    'site:myshopify.com phone stands',
    'site:myshopify.com cheap phone stands',
    'site:myshopify.com budget phone stands',
    'site:myshopify.com affordable phone stands',
    'site:myshopify.com charging cables',
    'site:myshopify.com cheap charging cables',
    'site:myshopify.com budget charging cables',
    'site:myshopify.com affordable charging cables',
    'site:myshopify.com screen protectors',
    'site:myshopify.com cheap screen protectors',
    'site:myshopify.com budget screen protectors',
    'site:myshopify.com affordable screen protectors',
    'site:myshopify.com car accessories',
    'site:myshopify.com cheap car accessories',
    'site:myshopify.com budget car accessories',
    'site:myshopify.com affordable car accessories',
    'site:myshopify.com air fresheners',
    'site:myshopify.com cheap air fresheners',
    'site:myshopify.com budget air fresheners',
    'site:myshopify.com affordable air fresheners',
    'site:myshopify.com car phone holder',
    'site:myshopify.com cheap car phone holder',
    'site:myshopify.com budget car phone holder',
    'site:myshopify.com affordable car phone holder',
    'site:myshopify.com home accessories',
    'site:myshopify.com cheap home accessories',
    'site:myshopify.com budget home accessories',
    'site:myshopify.com affordable home accessories',
    'site:myshopify.com picture frames',
    'site:myshopify.com cheap picture frames',
    'site:myshopify.com budget picture frames',
    'site:myshopify.com affordable picture frames',
    'site:myshopify.com photo frames',
    'site:myshopify.com cheap photo frames',
    'site:myshopify.com budget photo frames',
    'site:myshopify.com affordable photo frames',
    'site:myshopify.com wall hooks',
    'site:myshopify.com cheap wall hooks',
    'site:myshopify.com budget wall hooks',
    'site:myshopify.com affordable wall hooks',
    'site:myshopify.com command hooks',
    'site:myshopify.com cheap command hooks',
    'site:myshopify.com budget command hooks',
    'site:myshopify.com affordable command hooks',
    'site:myshopify.com hangers',
    'site:myshopify.com cheap hangers',
    'site:myshopify.com budget hangers',
    'site:myshopify.com affordable hangers',
    'site:myshopify.com plastic hangers',
    'site:myshopify.com cheap plastic hangers',
    'site:myshopify.com budget plastic hangers',
    'site:myshopify.com affordable plastic hangers',
    'site:myshopify.com wire hangers',
    'site:myshopify.com cheap wire hangers',
    'site:myshopify.com budget wire hangers',
    'site:myshopify.com affordable wire hangers',
    'site:myshopify.com laundry supplies',
    'site:myshopify.com cheap laundry supplies',
    'site:myshopify.com budget laundry supplies',
    'site:myshopify.com affordable laundry supplies',
    'site:myshopify.com laundry baskets',
    'site:myshopify.com cheap laundry baskets',
    'site:myshopify.com budget laundry baskets',
    'site:myshopify.com affordable laundry baskets',
    'site:myshopify.com dryer sheets',
    'site:myshopify.com fabric softener',
    'site:myshopify.com cheap dryer sheets',
    'site:myshopify.com budget fabric softener',
    'site:myshopify.com affordable dryer sheets',
    'site:myshopify.com clothespins',
    'site:myshopify.com cheap clothespins',
    'site:myshopify.com budget clothespins',
    'site:myshopify.com affordable clothespins',
    'site:myshopify.com dish drainer',
    'site:myshopify.com cheap dish drainer',
    'site:myshopify.com budget dish drainer',
    'site:myshopify.com affordable dish drainer',
    'site:myshopify.com kitchen organizers',
    'site:myshopify.com cheap kitchen organizers',
    'site:myshopify.com budget kitchen organizers',
    'site:myshopify.com affordable kitchen organizers',
    'site:myshopify.com drawer organizers',
    'site:myshopify.com cheap drawer organizers',
    'site:myshopify.com budget drawer organizers',
    'site:myshopify.com affordable drawer organizers',
    'site:myshopify.com shelf organizers',
    'site:myshopify.com cheap shelf organizers',
    'site:myshopify.com budget shelf organizers',
    'site:myshopify.com affordable shelf organizers',
    'site:myshopify.com bathroom accessories',
    'site:myshopify.com cheap bathroom accessories',
    'site:myshopify.com budget bathroom accessories',
    'site:myshopify.com affordable bathroom accessories',
    'site:myshopify.com shower curtain',
    'site:myshopify.com cheap shower curtain',
    'site:myshopify.com budget shower curtain',
    'site:myshopify.com affordable shower curtain',
    'site:myshopify.com bath mat',
    'site:myshopify.com cheap bath mat',
    'site:myshopify.com budget bath mat',
    'site:myshopify.com affordable bath mat',
    'site:myshopify.com soap dispenser',
    'site:myshopify.com cheap soap dispenser',
    'site:myshopify.com budget soap dispenser',
    'site:myshopify.com affordable soap dispenser',
    'site:myshopify.com toothbrush holder',
    'site:myshopify.com cheap toothbrush holder',
    'site:myshopify.com budget toothbrush holder',
    'site:myshopify.com affordable toothbrush holder',
    'site:myshopify.com toilet brush',
    'site:myshopify.com cheap toilet brush',
    'site:myshopify.com budget toilet brush',
    'site:myshopify.com affordable toilet brush',
    'site:myshopify.com toilet cleaner',
    'site:myshopify.com cheap toilet cleaner',
    'site:myshopify.com budget toilet cleaner',
    'site:myshopify.com affordable toilet cleaner',
    'site:myshopify.com bath towels',
    'site:myshopify.com cheap bath towels',
    'site:myshopify.com budget bath towels',
    'site:myshopify.com affordable bath towels',
    'site:myshopify.com hand towels',
    'site:myshopify.com cheap hand towels',
    'site:myshopify.com budget hand towels',
    'site:myshopify.com affordable hand towels',
    'site:myshopify.com wash towels',
    'site:myshopify.com cheap wash towels',
    'site:myshopify.com budget wash towels',
    'site:myshopify.com affordable wash towels',
    'site:myshopify.com personal care',
    'site:myshopify.com cheap personal care',
    'site:myshopify.com budget personal care',
    'site:myshopify.com affordable personal care',
    'site:myshopify.com toothbrush',
    'site:myshopify.com toothpaste',
    'site:myshopify.com cheap toothbrush',
    'site:myshopify.com budget toothpaste',
    'site:myshopify.com affordable toothbrush',
    'site:myshopify.com dental floss',
    'site:myshopify.com mouthwash',
    'site:myshopify.com cheap dental floss',
    'site:myshopify.com budget mouthwash',
    'site:myshopify.com affordable dental floss',
    'site:myshopify.com shampoo',
    'site:myshopify.com conditioner',
    'site:myshopify.com cheap shampoo',
    'site:myshopify.com budget conditioner',
    'site:myshopify.com affordable shampoo',
    'site:myshopify.com body wash',
    'site:myshopify.com soap',
    'site:myshopify.com cheap body wash',
    'site:myshopify.com budget soap',
    'site:myshopify.com affordable body wash',
    'site:myshopify.com bar soap',
    'site:myshopify.com liquid soap',
    'site:myshopify.com cheap bar soap',
    'site:myshopify.com budget liquid soap',
    'site:myshopify.com affordable bar soap',
    'site:myshopify.com deodorant',
    'site:myshopify.com antiperspirant',
    'site:myshopify.com cheap deodorant',
    'site:myshopify.com budget antiperspirant',
    'site:myshopify.com affordable deodorant',
    'site:myshopify.com razors',
    'site:myshopify.com shaving cream',
    'site:myshopify.com cheap razors',
    'site:myshopify.com budget shaving cream',
    'site:myshopify.com affordable razors',
    'site:myshopify.com disposable razors',
    'site:myshopify.com cheap disposable razors',
    'site:myshopify.com budget disposable razors',
    'site:myshopify.com affordable disposable razors',
    'site:myshopify.com q tips',
    'site:myshopify.com cotton swabs',
    'site:myshopify.com cheap q tips',
    'site:myshopify.com budget cotton swabs',
    'site:myshopify.com affordable q tips',
    'site:myshopify.com cotton balls',
    'site:myshopify.com cotton pads',
    'site:myshopify.com cheap cotton balls',
    'site:myshopify.com budget cotton pads',
    'site:myshopify.com affordable cotton balls',
    'site:myshopify.com band aids',
    'site:myshopify.com first aid',
    'site:myshopify.com cheap band aids',
    'site:myshopify.com budget first aid',
    'site:myshopify.com affordable band aids',
    'site:myshopify.com vitamins',
    'site:myshopify.com supplements',
    'site:myshopify.com cheap vitamins',
    'site:myshopify.com budget supplements',
    'site:myshopify.com affordable vitamins',
    'site:myshopify.com light bulbs',
    'site:myshopify.com cheap light bulbs',
    'site:myshopify.com budget light bulbs',
    'site:myshopify.com affordable light bulbs',
    'site:myshopify.com led bulbs',
    'site:myshopify.com cheap led bulbs',
    'site:myshopify.com budget led bulbs',
    'site:myshopify.com affordable led bulbs',
    'site:myshopify.com batteries',
    'site:myshopify.com cheap batteries',
    'site:myshopify.com budget batteries',
    'site:myshopify.com affordable batteries',
    'site:myshopify.com aa batteries',
    'site:myshopify.com aaa batteries',
    'site:myshopify.com cheap aa batteries',
    'site:myshopify.com budget aaa batteries',
    'site:myshopify.com affordable aa batteries',
    'site:myshopify.com extension cords',
    'site:myshopify.com cheap extension cords',
    'site:myshopify.com budget extension cords',
    'site:myshopify.com affordable extension cords',
    'site:myshopify.com power strips',
    'site:myshopify.com cheap power strips',
    'site:myshopify.com budget power strips',
    'site:myshopify.com affordable power strips',
    'site:myshopify.com tools',
    'site:myshopify.com cheap tools',
    'site:myshopify.com budget tools',
    'site:myshopify.com affordable tools',
    'site:myshopify.com screwdrivers',
    'site:myshopify.com hammers',
    'site:myshopify.com cheap screwdrivers',
    'site:myshopify.com budget hammers',
    'site:myshopify.com affordable screwdrivers',
    'site:myshopify.com tool kit',
    'site:myshopify.com cheap tool kit',
    'site:myshopify.com budget tool kit',
    'site:myshopify.com affordable tool kit',
    'site:myshopify.com basic tool kit',
    'site:myshopify.com starter tool kit',
    'site:myshopify.com cheap starter tool kit',
    'site:myshopify.com budget starter tool kit',
    'site:myshopify.com affordable starter tool kit',
    'site:myshopify.com gardening supplies',
    'site:myshopify.com cheap gardening supplies',
    'site:myshopify.com budget gardening supplies',
    'site:myshopify.com affordable gardening supplies',
    'site:myshopify.com plant pots',
    'site:myshopify.com planters',
    'site:myshopify.com cheap plant pots',
    'site:myshopify.com budget planters',
    'site:myshopify.com affordable plant pots',
    'site:myshopify.com seeds',
    'site:myshopify.com cheap seeds',
    'site:myshopify.com budget seeds',
    'site:myshopify.com affordable seeds',
    'site:myshopify.com garden tools',
    'site:myshopify.com cheap garden tools',
    'site:myshopify.com budget garden tools',
    'site:myshopify.com affordable garden tools',
    'site:myshopify.com watering can',
    'site:myshopify.com cheap watering can',
    'site:myshopify.com budget watering can',
    'site:myshopify.com affordable watering can',
    'site:myshopify.com pet supplies',
    'site:myshopify.com cheap pet supplies',
    'site:myshopify.com budget pet supplies',
    'site:myshopify.com affordable pet supplies',
    'site:myshopify.com pet toys',
    'site:myshopify.com cheap pet toys',
    'site:myshopify.com budget pet toys',
    'site:myshopify.com affordable pet toys',
    'site:myshopify.com pet bowls',
    'site:myshopify.com cheap pet bowls',
    'site:myshopify.com budget pet bowls',
    'site:myshopify.com affordable pet bowls',
    'site:myshopify.com pet collars',
    'site:myshopify.com cheap pet collars',
    'site:myshopify.com budget pet collars',
    'site:myshopify.com affordable pet collars',
    'site:myshopify.com pet leashes',
    'site:myshopify.com cheap pet leashes',
    'site:myshopify.com budget pet leashes',
    'site:myshopify.com affordable pet leashes',
    'site:myshopify.com pet beds',
    'site:myshopify.com cheap pet beds',
    'site:myshopify.com budget pet beds',
    'site:myshopify.com affordable pet beds',
    'site:myshopify.com pet food',
    'site:myshopify.com cheap pet food',
    'site:myshopify.com budget pet food',
    'site:myshopify.com affordable pet food',
    'site:myshopify.com baby supplies',
    'site:myshopify.com cheap baby supplies',
    'site:myshopify.com budget baby supplies',
    'site:myshopify.com affordable baby supplies',
    'site:myshopify.com baby wipes',
    'site:myshopify.com diapers',
    'site:myshopify.com cheap baby wipes',
    'site:myshopify.com budget diapers',
    'site:myshopify.com affordable baby wipes',
    'site:myshopify.com baby bottles',
    'site:myshopify.com cheap baby bottles',
    'site:myshopify.com budget baby bottles',
    'site:myshopify.com affordable baby bottles',
    'site:myshopify.com baby toys',
    'site:myshopify.com cheap baby toys',
    'site:myshopify.com budget baby toys',
    'site:myshopify.com affordable baby toys',
    'site:myshopify.com baby clothes',
    'site:myshopify.com cheap baby clothes',
    'site:myshopify.com budget baby clothes',
    'site:myshopify.com affordable baby clothes',
    'site:myshopify.com school supplies',
    'site:myshopify.com cheap school supplies',
    'site:myshopify.com budget school supplies',
    'site:myshopify.com affordable school supplies',
    'site:myshopify.com backpacks',
    'site:myshopify.com cheap backpacks',
    'site:myshopify.com budget backpacks',
    'site:myshopify.com affordable backpacks',
    'site:myshopify.com lunch boxes',
    'site:myshopify.com cheap lunch boxes',
    'site:myshopify.com budget lunch boxes',
    'site:myshopify.com affordable lunch boxes',
    'site:myshopify.com water bottles',
    'site:myshopify.com cheap water bottles',
    'site:myshopify.com budget water bottles',
    'site:myshopify.com affordable water bottles',
    'site:myshopify.com sports bottles',
    'site:myshopify.com cheap sports bottles',
    'site:myshopify.com budget sports bottles',
    'site:myshopify.com affordable sports bottles',
    'site:myshopify.com reusable bottles',
    'site:myshopify.com cheap reusable bottles',
    'site:myshopify.com budget reusable bottles',
    'site:myshopify.com affordable reusable bottles',
    'site:myshopify.com food storage',
    'site:myshopify.com cheap food storage',
    'site:myshopify.com budget food storage',
    'site:myshopify.com affordable food storage',
    'site:myshopify.com tupperware',
    'site:myshopify.com food containers',
    'site:myshopify.com cheap tupperware',
    'site:myshopify.com budget food containers',
    'site:myshopify.com affordable tupperware',
    'site:myshopify.com plastic containers',
    'site:myshopify.com cheap plastic containers',
    'site:myshopify.com budget plastic containers',
    'site:myshopify.com affordable plastic containers',
    'site:myshopify.com snack containers',
    'site:myshopify.com cheap snack containers',
    'site:myshopify.com budget snack containers',
    'site:myshopify.com affordable snack containers',
    'site:myshopify.com meal prep containers',
    'site:myshopify.com cheap meal prep containers',
    'site:myshopify.com budget meal prep containers',
    'site:myshopify.com affordable meal prep containers',
    
    'site:myshopify.com dollar section',
    'site:myshopify.com dollar aisle',
    'site:myshopify.com bargain bin',
    'site:myshopify.com clearance rack',
    'site:myshopify.com clearance section',
    'site:myshopify.com closeout',
    'site:myshopify.com closeout sale',
    'site:myshopify.com liquidation',
    'site:myshopify.com overstock',
    'site:myshopify.com overstock sale',
    'site:myshopify.com remainder',
    'site:myshopify.com remainder sale',
    'site:myshopify.com discontinued',
    'site:myshopify.com last chance',
    'site:myshopify.com final sale',
    'site:myshopify.com end of season',
    'site:myshopify.com end of line',
    'site:myshopify.com while supplies last',
    'site:myshopify.com limited stock',
    'site:myshopify.com limited quantity',
    'site:myshopify.com low stock',
    'site:myshopify.com low quantity',
    'site:myshopify.com reduced price',
    'site:myshopify.com reduced',
    'site:myshopify.com price drop',
    'site:myshopify.com price reduction',
    'site:myshopify.com markdown',
    'site:myshopify.com marked down',
    'site:myshopify.com price cut',
    'site:myshopify.com slashed price',
    'site:myshopify.com slashed',
    'site:myshopify.com discounted',
    'site:myshopify.com heavily discounted',
    'site:myshopify.com deeply discounted',
    'site:myshopify.com big discount',
    'site:myshopify.com huge discount',
    'site:myshopify.com massive discount',
    'site:myshopify.com extra discount',
    'site:myshopify.com additional discount',
    'site:myshopify.com extra savings',
    'site:myshopify.com additional savings',
    'site:myshopify.com more savings',
    'site:myshopify.com save more',
    'site:myshopify.com save big',
    'site:myshopify.com save money',
    'site:myshopify.com save',
    'site:myshopify.com savings',
    'site:myshopify.com money saving',
    'site:myshopify.com cost saving',
    'site:myshopify.com cost effective',
    'site:myshopify.com cost efficient',
    'site:myshopify.com economical choice',
    'site:myshopify.com economical option',
    'site:myshopify.com budget choice',
    'site:myshopify.com budget option',
    'site:myshopify.com affordable choice',
    'site:myshopify.com affordable option',
    'site:myshopify.com cheap choice',
    'site:myshopify.com cheap option',
    'site:myshopify.com value choice',
    'site:myshopify.com value option',
    'site:myshopify.com best value',
    'site:myshopify.com great value',
    'site:myshopify.com excellent value',
    'site:myshopify.com outstanding value',
    'site:myshopify.com superior value',
    'site:myshopify.com maximum value',
    'site:myshopify.com maximum savings',
    'site:myshopify.com maximum discount',
    'site:myshopify.com top value',
    'site:myshopify.com top savings',
    'site:myshopify.com top discount',
    'site:myshopify.com premium value',
    'site:myshopify.com premium savings',
    'site:myshopify.com premium discount',
    'site:myshopify.com exceptional value',
    'site:myshopify.com exceptional savings',
    'site:myshopify.com exceptional discount',
    'site:myshopify.com unbeatable value',
    'site:myshopify.com unbeatable price',
    'site:myshopify.com unbeatable deal',
    'site:myshopify.com unbeatable savings',
    'site:myshopify.com unbeatable discount',
    'site:myshopify.com unmatched value',
    'site:myshopify.com unmatched price',
    'site:myshopify.com unmatched deal',
    'site:myshopify.com unmatched savings',
    'site:myshopify.com unmatched discount',
    'site:myshopify.com cannot beat',
    'site:myshopify.com cannot beat price',
    'site:myshopify.com cannot beat deal',
    'site:myshopify.com cannot beat value',
    'site:myshopify.com cannot beat savings',
    'site:myshopify.com cannot beat discount',
    'site:myshopify.com best price',
    'site:myshopify.com lowest price',
    'site:myshopify.com lowest cost',
    'site:myshopify.com lowest',
    'site:myshopify.com rock bottom',
    'site:myshopify.com rock bottom price',
    'site:myshopify.com rock bottom cost',
    'site:myshopify.com rock bottom deal',
    'site:myshopify.com bottom dollar',
    'site:myshopify.com bottom dollar price',
    'site:myshopify.com bottom dollar cost',
    'site:myshopify.com bottom dollar deal',
    'site:myshopify.com dirt cheap',
    'site:myshopify.com super cheap',
    'site:myshopify.com ultra cheap',
    'site:myshopify.com mega cheap',
    'site:myshopify.com hyper cheap',
    'site:myshopify.com extremely cheap',
    'site:myshopify.com incredibly cheap',
    'site:myshopify.com amazingly cheap',
    'site:myshopify.com ridiculously cheap',
    'site:myshopify.com absurdly cheap',
    'site:myshopify.com insanely cheap',
    'site:myshopify.com unbelievably cheap',
    'site:myshopify.com outrageously cheap',
    'site:myshopify.com phenomenally cheap',
    'site:myshopify.com exceptionally cheap',
    'site:myshopify.com extraordinarily cheap',
    'site:myshopify.com incredibly affordable',
    'site:myshopify.com amazingly affordable',
    'site:myshopify.com ridiculously affordable',
    'site:myshopify.com absurdly affordable',
    'site:myshopify.com insanely affordable',
    'site:myshopify.com unbelievably affordable',
    'site:myshopify.com outrageously affordable',
    'site:myshopify.com phenomenally affordable',
    'site:myshopify.com exceptionally affordable',
    'site:myshopify.com extraordinarily affordable',
    'site:myshopify.com incredibly budget',
    'site:myshopify.com amazingly budget',
    'site:myshopify.com ridiculously budget',
    'site:myshopify.com absurdly budget',
    'site:myshopify.com insanely budget',
    'site:myshopify.com unbelievably budget',
    'site:myshopify.com outrageously budget',
    'site:myshopify.com phenomenally budget',
    'site:myshopify.com exceptionally budget',
    'site:myshopify.com extraordinarily budget',
    'site:myshopify.com penny',
    'site:myshopify.com pennies',
    'site:myshopify.com cents',
    'site:myshopify.com cents only',
    'site:myshopify.com penny items',
    'site:myshopify.com penny products',
    'site:myshopify.com penny store',
    'site:myshopify.com penny shop',
    'site:myshopify.com penny price',
    'site:myshopify.com penny cost',
    'site:myshopify.com penny deal',
    'site:myshopify.com cents items',
    'site:myshopify.com cents products',
    'site:myshopify.com cents store',
    'site:myshopify.com cents shop',
    'site:myshopify.com cents price',
    'site:myshopify.com cents cost',
    'site:myshopify.com cents deal',
    'site:myshopify.com nickel',
    'site:myshopify.com nickel items',
    'site:myshopify.com nickel products',
    'site:myshopify.com nickel price',
    'site:myshopify.com nickel cost',
    'site:myshopify.com nickel deal',
    'site:myshopify.com dime',
    'site:myshopify.com dime items',
    'site:myshopify.com dime products',
    'site:myshopify.com dime price',
    'site:myshopify.com dime cost',
    'site:myshopify.com dime deal',
    'site:myshopify.com quarter',
    'site:myshopify.com quarter items',
    'site:myshopify.com quarter products',
    'site:myshopify.com quarter price',
    'site:myshopify.com quarter cost',
    'site:myshopify.com quarter deal',
    'site:myshopify.com pocket change',
    'site:myshopify.com pocket change items',
    'site:myshopify.com pocket change products',
    'site:myshopify.com pocket change price',
    'site:myshopify.com pocket change cost',
    'site:myshopify.com pocket change deal',
    'site:myshopify.com loose change',
    'site:myshopify.com loose change items',
    'site:myshopify.com loose change products',
    'site:myshopify.com loose change price',
    'site:myshopify.com loose change cost',
    'site:myshopify.com loose change deal',
    'site:myshopify.com spare change',
    'site:myshopify.com spare change items',
    'site:myshopify.com spare change products',
    'site:myshopify.com spare change price',
    'site:myshopify.com spare change cost',
    'site:myshopify.com spare change deal',
    'site:myshopify.com small change',
    'site:myshopify.com small change items',
    'site:myshopify.com small change products',
    'site:myshopify.com small change price',
    'site:myshopify.com small change cost',
    'site:myshopify.com small change deal',
    'site:myshopify.com coins',
    'site:myshopify.com coins only',
    'site:myshopify.com coins items',
    'site:myshopify.com coins products',
    'site:myshopify.com coins price',
    'site:myshopify.com coins cost',
    'site:myshopify.com coins deal',
    'site:myshopify.com pocket money',
    'site:myshopify.com pocket money items',
    'site:myshopify.com pocket money products',
    'site:myshopify.com pocket money price',
    'site:myshopify.com pocket money cost',
    'site:myshopify.com pocket money deal',
    'site:myshopify.com allowance',
    'site:myshopify.com allowance items',
    'site:myshopify.com allowance products',
    'site:myshopify.com allowance price',
    'site:myshopify.com allowance cost',
    'site:myshopify.com allowance deal',
    'site:myshopify.com kid money',
    'site:myshopify.com kid money items',
    'site:myshopify.com kid money products',
    'site:myshopify.com kid money price',
    'site:myshopify.com kid money cost',
    'site:myshopify.com kid money deal',
    'site:myshopify.com kids budget',
    'site:myshopify.com kids budget items',
    'site:myshopify.com kids budget products',
    'site:myshopify.com kids budget price',
    'site:myshopify.com kids budget cost',
    'site:myshopify.com kids budget deal',
    'site:myshopify.com student budget',
    'site:myshopify.com student budget items',
    'site:myshopify.com student budget products',
    'site:myshopify.com student budget price',
    'site:myshopify.com student budget cost',
    'site:myshopify.com student budget deal',
    'site:myshopify.com college budget',
    'site:myshopify.com college budget items',
    'site:myshopify.com college budget products',
    'site:myshopify.com college budget price',
    'site:myshopify.com college budget cost',
    'site:myshopify.com college budget deal',
    'site:myshopify.com teen budget',
    'site:myshopify.com teen budget items',
    'site:myshopify.com teen budget products',
    'site:myshopify.com teen budget price',
    'site:myshopify.com teen budget cost',
    'site:myshopify.com teen budget deal',
    'site:myshopify.com broke',
    'site:myshopify.com broke friendly',
    'site:myshopify.com broke budget',
    'site:myshopify.com broke price',
    'site:myshopify.com broke cost',
    'site:myshopify.com broke deal',
    'site:myshopify.com poor',
    'site:myshopify.com poor friendly',
    'site:myshopify.com poor budget',
    'site:myshopify.com poor price',
    'site:myshopify.com poor cost',
    'site:myshopify.com poor deal',
    'site:myshopify.com broke student',
    'site:myshopify.com broke student items',
    'site:myshopify.com broke student products',
    'site:myshopify.com broke student price',
    'site:myshopify.com broke student cost',
    'site:myshopify.com broke student deal',
    'site:myshopify.com poor student',
    'site:myshopify.com poor student items',
    'site:myshopify.com poor student products',
    'site:myshopify.com poor student price',
    'site:myshopify.com poor student cost',
    'site:myshopify.com poor student deal',
    'site:myshopify.com college student',
    'site:myshopify.com college student items',
    'site:myshopify.com college student products',
    'site:myshopify.com college student price',
    'site:myshopify.com college student cost',
    'site:myshopify.com college student deal',
    'site:myshopify.com university student',
    'site:myshopify.com university student items',
    'site:myshopify.com university student products',
    'site:myshopify.com university student price',
    'site:myshopify.com university student cost',
    'site:myshopify.com university student deal',
    'site:myshopify.com grad student',
    'site:myshopify.com grad student items',
    'site:myshopify.com grad student products',
    'site:myshopify.com grad student price',
    'site:myshopify.com grad student cost',
    'site:myshopify.com grad student deal',
    'site:myshopify.com single mom',
    'site:myshopify.com single mom items',
    'site:myshopify.com single mom products',
    'site:myshopify.com single mom price',
    'site:myshopify.com single mom cost',
    'site:myshopify.com single mom deal',
    'site:myshopify.com single parent',
    'site:myshopify.com single parent items',
    'site:myshopify.com single parent products',
    'site:myshopify.com single parent price',
    'site:myshopify.com single parent cost',
    'site:myshopify.com single parent deal',
    'site:myshopify.com single income',
    'site:myshopify.com single income items',
    'site:myshopify.com single income products',
    'site:myshopify.com single income price',
    'site:myshopify.com single income cost',
    'site:myshopify.com single income deal',
    'site:myshopify.com fixed income',
    'site:myshopify.com fixed income items',
    'site:myshopify.com fixed income products',
    'site:myshopify.com fixed income price',
    'site:myshopify.com fixed income cost',
    'site:myshopify.com fixed income deal',
    'site:myshopify.com limited income',
    'site:myshopify.com limited income items',
    'site:myshopify.com limited income products',
    'site:myshopify.com limited income price',
    'site:myshopify.com limited income cost',
    'site:myshopify.com limited income deal',
    'site:myshopify.com low income',
    'site:myshopify.com low income items',
    'site:myshopify.com low income products',
    'site:myshopify.com low income price',
    'site:myshopify.com low income cost',
    'site:myshopify.com low income deal',
    'site:myshopify.com tight budget',
    'site:myshopify.com tight budget items',
    'site:myshopify.com tight budget products',
    'site:myshopify.com tight budget price',
    'site:myshopify.com tight budget cost',
    'site:myshopify.com tight budget deal',
    'site:myshopify.com shoestring',
    'site:myshopify.com shoestring budget',
    'site:myshopify.com shoestring items',
    'site:myshopify.com shoestring products',
    'site:myshopify.com shoestring price',
    'site:myshopify.com shoestring cost',
    'site:myshopify.com shoestring deal',
    'site:myshopify.com minimal budget',
    'site:myshopify.com minimal budget items',
    'site:myshopify.com minimal budget products',
    'site:myshopify.com minimal budget price',
    'site:myshopify.com minimal budget cost',
    'site:myshopify.com minimal budget deal',
    'site:myshopify.com no frills budget',
    'site:myshopify.com no frills budget items',
    'site:myshopify.com no frills budget products',
    'site:myshopify.com no frills budget price',
    'site:myshopify.com no frills budget cost',
    'site:myshopify.com no frills budget deal',
    'site:myshopify.com bare bones',
    'site:myshopify.com bare bones budget',
    'site:myshopify.com bare bones items',
    'site:myshopify.com bare bones products',
    'site:myshopify.com bare bones price',
    'site:myshopify.com bare bones cost',
    'site:myshopify.com bare bones deal',
    'site:myshopify.com basic budget',
    'site:myshopify.com basic budget items',
    'site:myshopify.com basic budget products',
    'site:myshopify.com basic budget price',
    'site:myshopify.com basic budget cost',
    'site:myshopify.com basic budget deal',
    'site:myshopify.com essential budget',
    'site:myshopify.com essential budget items',
    'site:myshopify.com essential budget products',
    'site:myshopify.com essential budget price',
    'site:myshopify.com essential budget cost',
    'site:myshopify.com essential budget deal',
    'site:myshopify.com necessity budget',
    'site:myshopify.com necessity budget items',
    'site:myshopify.com necessity budget products',
    'site:myshopify.com necessity budget price',
    'site:myshopify.com necessity budget cost',
    'site:myshopify.com necessity budget deal',
    'site:myshopify.com thrifty',
    'site:myshopify.com thrifty items',
    'site:myshopify.com thrifty products',
    'site:myshopify.com thrifty price',
    'site:myshopify.com thrifty cost',
    'site:myshopify.com thrifty deal',
    'site:myshopify.com thrift',
    'site:myshopify.com thrift items',
    'site:myshopify.com thrift products',
    'site:myshopify.com thrift price',
    'site:myshopify.com thrift cost',
    'site:myshopify.com thrift deal',
    'site:myshopify.com thrift store',
    'site:myshopify.com thrift store items',
    'site:myshopify.com thrift store products',
    'site:myshopify.com thrift store price',
    'site:myshopify.com thrift store cost',
    'site:myshopify.com thrift store deal',
    'site:myshopify.com thrifty shopper',
    'site:myshopify.com thrifty shopper items',
    'site:myshopify.com thrifty shopper products',
    'site:myshopify.com thrifty shopper price',
    'site:myshopify.com thrifty shopper cost',
    'site:myshopify.com thrifty shopper deal',
    'site:myshopify.com frugal',
    'site:myshopify.com frugal items',
    'site:myshopify.com frugal products',
    'site:myshopify.com frugal price',
    'site:myshopify.com frugal cost',
    'site:myshopify.com frugal deal',
    'site:myshopify.com frugal living',
    'site:myshopify.com frugal living items',
    'site:myshopify.com frugal living products',
    'site:myshopify.com frugal living price',
    'site:myshopify.com frugal living cost',
    'site:myshopify.com frugal living deal',
    'site:myshopify.com frugal shopper',
    'site:myshopify.com frugal shopper items',
    'site:myshopify.com frugal shopper products',
    'site:myshopify.com frugal shopper price',
    'site:myshopify.com frugal shopper cost',
    'site:myshopify.com frugal shopper deal',
    'site:myshopify.com penny pincher',
    'site:myshopify.com penny pincher items',
    'site:myshopify.com penny pincher products',
    'site:myshopify.com penny pincher price',
    'site:myshopify.com penny pincher cost',
    'site:myshopify.com penny pincher deal',
    'site:myshopify.com penny wise',
    'site:myshopify.com penny wise items',
    'site:myshopify.com penny wise products',
    'site:myshopify.com penny wise price',
    'site:myshopify.com penny wise cost',
    'site:myshopify.com penny wise deal',
    'site:myshopify.com money conscious',
    'site:myshopify.com money conscious items',
    'site:myshopify.com money conscious products',
    'site:myshopify.com money conscious price',
    'site:myshopify.com money conscious cost',
    'site:myshopify.com money conscious deal',
    'site:myshopify.com cost conscious',
    'site:myshopify.com cost conscious items',
    'site:myshopify.com cost conscious products',
    'site:myshopify.com cost conscious price',
    'site:myshopify.com cost conscious cost',
    'site:myshopify.com cost conscious deal',
    'site:myshopify.com price conscious',
    'site:myshopify.com price conscious items',
    'site:myshopify.com price conscious products',
    'site:myshopify.com price conscious price',
    'site:myshopify.com price conscious cost',
    'site:myshopify.com price conscious deal',
    'site:myshopify.com budget conscious',
    'site:myshopify.com budget conscious items',
    'site:myshopify.com budget conscious products',
    'site:myshopify.com budget conscious price',
    'site:myshopify.com budget conscious cost',
    'site:myshopify.com budget conscious deal',
    'site:myshopify.com smart shopper',
    'site:myshopify.com smart shopper items',
    'site:myshopify.com smart shopper products',
    'site:myshopify.com smart shopper price',
    'site:myshopify.com smart shopper cost',
    'site:myshopify.com smart shopper deal',
    'site:myshopify.com smart buy',
    'site:myshopify.com smart buy items',
    'site:myshopify.com smart buy products',
    'site:myshopify.com smart buy price',
    'site:myshopify.com smart buy cost',
    'site:myshopify.com smart buy deal',
    'site:myshopify.com smart purchase',
    'site:myshopify.com smart purchase items',
    'site:myshopify.com smart purchase products',
    'site:myshopify.com smart purchase price',
    'site:myshopify.com smart purchase cost',
    'site:myshopify.com smart purchase deal',
    'site:myshopify.com wise buy',
    'site:myshopify.com wise buy items',
    'site:myshopify.com wise buy products',
    'site:myshopify.com wise buy price',
    'site:myshopify.com wise buy cost',
    'site:myshopify.com wise buy deal',
    'site:myshopify.com wise purchase',
    'site:myshopify.com wise purchase items',
    'site:myshopify.com wise purchase products',
    'site:myshopify.com wise purchase price',
    'site:myshopify.com wise purchase cost',
    'site:myshopify.com wise purchase deal',
    'site:myshopify.com intelligent buy',
    'site:myshopify.com intelligent buy items',
    'site:myshopify.com intelligent buy products',
    'site:myshopify.com intelligent buy price',
    'site:myshopify.com intelligent buy cost',
    'site:myshopify.com intelligent buy deal',
    'site:myshopify.com intelligent purchase',
    'site:myshopify.com intelligent purchase items',
    'site:myshopify.com intelligent purchase products',
    'site:myshopify.com intelligent purchase price',
    'site:myshopify.com intelligent purchase cost',
    'site:myshopify.com intelligent purchase deal',
    'site:myshopify.com savvy shopper',
    'site:myshopify.com savvy shopper items',
    'site:myshopify.com savvy shopper products',
    'site:myshopify.com savvy shopper price',
    'site:myshopify.com savvy shopper cost',
    'site:myshopify.com savvy shopper deal',
    'site:myshopify.com savvy buy',
    'site:myshopify.com savvy buy items',
    'site:myshopify.com savvy buy products',
    'site:myshopify.com savvy buy price',
    'site:myshopify.com savvy buy cost',
    'site:myshopify.com savvy buy deal',
    'site:myshopify.com savvy purchase',
    'site:myshopify.com savvy purchase items',
    'site:myshopify.com savvy purchase products',
    'site:myshopify.com savvy purchase price',
    'site:myshopify.com savvy purchase cost',
    'site:myshopify.com savvy purchase deal',
    'site:myshopify.com budget shopper',
    'site:myshopify.com budget shopper items',
    'site:myshopify.com budget shopper products',
    'site:myshopify.com budget shopper price',
    'site:myshopify.com budget shopper cost',
    'site:myshopify.com budget shopper deal',
    'site:myshopify.com budget buyer',
    'site:myshopify.com budget buyer items',
    'site:myshopify.com budget buyer products',
    'site:myshopify.com budget buyer price',
    'site:myshopify.com budget buyer cost',
    'site:myshopify.com budget buyer deal',
    'site:myshopify.com budget purchase',
    'site:myshopify.com budget purchase items',
    'site:myshopify.com budget purchase products',
    'site:myshopify.com budget purchase price',
    'site:myshopify.com budget purchase cost',
    'site:myshopify.com budget purchase deal',
    'site:myshopify.com budget shopping',
    'site:myshopify.com budget shopping items',
    'site:myshopify.com budget shopping products',
    'site:myshopify.com budget shopping price',
    'site:myshopify.com budget shopping cost',
    'site:myshopify.com budget shopping deal',
    'site:myshopify.com economical shopper',
    'site:myshopify.com economical shopper items',
    'site:myshopify.com economical shopper products',
    'site:myshopify.com economical shopper price',
    'site:myshopify.com economical shopper cost',
    'site:myshopify.com economical shopper deal',
    'site:myshopify.com economical buyer',
    'site:myshopify.com economical buyer items',
    'site:myshopify.com economical buyer products',
    'site:myshopify.com economical buyer price',
    'site:myshopify.com economical buyer cost',
    'site:myshopify.com economical buyer deal',
    'site:myshopify.com economical purchase',
    'site:myshopify.com economical purchase items',
    'site:myshopify.com economical purchase products',
    'site:myshopify.com economical purchase price',
    'site:myshopify.com economical purchase cost',
    'site:myshopify.com economical purchase deal',
    'site:myshopify.com economical shopping',
    'site:myshopify.com economical shopping items',
    'site:myshopify.com economical shopping products',
    'site:myshopify.com economical shopping price',
    'site:myshopify.com economical shopping cost',
    'site:myshopify.com economical shopping deal',
    'site:myshopify.com value shopper',
    'site:myshopify.com value shopper items',
    'site:myshopify.com value shopper products',
    'site:myshopify.com value shopper price',
    'site:myshopify.com value shopper cost',
    'site:myshopify.com value shopper deal',
    'site:myshopify.com value buyer',
    'site:myshopify.com value buyer items',
    'site:myshopify.com value buyer products',
    'site:myshopify.com value buyer price',
    'site:myshopify.com value buyer cost',
    'site:myshopify.com value buyer deal',
    'site:myshopify.com value purchase',
    'site:myshopify.com value purchase items',
    'site:myshopify.com value purchase products',
    'site:myshopify.com value purchase price',
    'site:myshopify.com value purchase cost',
    'site:myshopify.com value purchase deal',
    'site:myshopify.com value shopping',
    'site:myshopify.com value shopping items',
    'site:myshopify.com value shopping products',
    'site:myshopify.com value shopping price',
    'site:myshopify.com value shopping cost',
    'site:myshopify.com value shopping deal',
    'site:myshopify.com deal hunter',
    'site:myshopify.com deal hunter items',
    'site:myshopify.com deal hunter products',
    'site:myshopify.com deal hunter price',
    'site:myshopify.com deal hunter cost',
    'site:myshopify.com deal hunter deal',
    'site:myshopify.com deal seeker',
    'site:myshopify.com deal seeker items',
    'site:myshopify.com deal seeker products',
    'site:myshopify.com deal seeker price',
    'site:myshopify.com deal seeker cost',
    'site:myshopify.com deal seeker deal',
    'site:myshopify.com bargain hunter',
    'site:myshopify.com bargain hunter items',
    'site:myshopify.com bargain hunter products',
    'site:myshopify.com bargain hunter price',
    'site:myshopify.com bargain hunter cost',
    'site:myshopify.com bargain hunter deal',
    'site:myshopify.com bargain seeker',
    'site:myshopify.com bargain seeker items',
    'site:myshopify.com bargain seeker products',
    'site:myshopify.com bargain seeker price',
    'site:myshopify.com bargain seeker cost',
    'site:myshopify.com bargain seeker deal',
    'site:myshopify.com bargain shopper',
    'site:myshopify.com bargain shopper items',
    'site:myshopify.com bargain shopper products',
    'site:myshopify.com bargain shopper price',
    'site:myshopify.com bargain shopper cost',
    'site:myshopify.com bargain shopper deal',
    'site:myshopify.com bargain buyer',
    'site:myshopify.com bargain buyer items',
    'site:myshopify.com bargain buyer products',
    'site:myshopify.com bargain buyer price',
    'site:myshopify.com bargain buyer cost',
    'site:myshopify.com bargain buyer deal',
    'site:myshopify.com bargain shopping',
    'site:myshopify.com bargain shopping items',
    'site:myshopify.com bargain shopping products',
    'site:myshopify.com bargain shopping price',
    'site:myshopify.com bargain shopping cost',
    'site:myshopify.com bargain shopping deal',
    'site:myshopify.com discount hunter',
    'site:myshopify.com discount hunter items',
    'site:myshopify.com discount hunter products',
    'site:myshopify.com discount hunter price',
    'site:myshopify.com discount hunter cost',
    'site:myshopify.com discount hunter deal',
    'site:myshopify.com discount seeker',
    'site:myshopify.com discount seeker items',
    'site:myshopify.com discount seeker products',
    'site:myshopify.com discount seeker price',
    'site:myshopify.com discount seeker cost',
    'site:myshopify.com discount seeker deal',
    'site:myshopify.com discount shopper',
    'site:myshopify.com discount shopper items',
    'site:myshopify.com discount shopper products',
    'site:myshopify.com discount shopper price',
    'site:myshopify.com discount shopper cost',
    'site:myshopify.com discount shopper deal',
    'site:myshopify.com discount buyer',
    'site:myshopify.com discount buyer items',
    'site:myshopify.com discount buyer products',
    'site:myshopify.com discount buyer price',
    'site:myshopify.com discount buyer cost',
    'site:myshopify.com discount buyer deal',
    'site:myshopify.com discount shopping',
    'site:myshopify.com discount shopping items',
    'site:myshopify.com discount shopping products',
    'site:myshopify.com discount shopping price',
    'site:myshopify.com discount shopping cost',
    'site:myshopify.com discount shopping deal',
    'site:myshopify.com sale hunter',
    'site:myshopify.com sale hunter items',
    'site:myshopify.com sale hunter products',
    'site:myshopify.com sale hunter price',
    'site:myshopify.com sale hunter cost',
    'site:myshopify.com sale hunter deal',
    'site:myshopify.com sale seeker',
    'site:myshopify.com sale seeker items',
    'site:myshopify.com sale seeker products',
    'site:myshopify.com sale seeker price',
    'site:myshopify.com sale seeker cost',
    'site:myshopify.com sale seeker deal',
    'site:myshopify.com sale shopper',
    'site:myshopify.com sale shopper items',
    'site:myshopify.com sale shopper products',
    'site:myshopify.com sale shopper price',
    'site:myshopify.com sale shopper cost',
    'site:myshopify.com sale shopper deal',
    'site:myshopify.com sale buyer',
    'site:myshopify.com sale buyer items',
    'site:myshopify.com sale buyer products',
    'site:myshopify.com sale buyer price',
    'site:myshopify.com sale buyer cost',
    'site:myshopify.com sale buyer deal',
    'site:myshopify.com sale shopping',
    'site:myshopify.com sale shopping items',
    'site:myshopify.com sale shopping products',
    'site:myshopify.com sale shopping price',
    'site:myshopify.com sale shopping cost',
    'site:myshopify.com sale shopping deal',
    'site:myshopify.com price hunter',
    'site:myshopify.com price hunter items',
    'site:myshopify.com price hunter products',
    'site:myshopify.com price hunter price',
    'site:myshopify.com price hunter cost',
    'site:myshopify.com price hunter deal',
    'site:myshopify.com price seeker',
    'site:myshopify.com price seeker items',
    'site:myshopify.com price seeker products',
    'site:myshopify.com price seeker price',
    'site:myshopify.com price seeker cost',
    'site:myshopify.com price seeker deal',
    'site:myshopify.com price shopper',
    'site:myshopify.com price shopper items',
    'site:myshopify.com price shopper products',
    'site:myshopify.com price shopper price',
    'site:myshopify.com price shopper cost',
    'site:myshopify.com price shopper deal',
    'site:myshopify.com price buyer',
    'site:myshopify.com price buyer items',
    'site:myshopify.com price buyer products',
    'site:myshopify.com price buyer price',
    'site:myshopify.com price buyer cost',
    'site:myshopify.com price buyer deal',
    'site:myshopify.com price shopping',
    'site:myshopify.com price shopping items',
    'site:myshopify.com price shopping products',
    'site:myshopify.com price shopping price',
    'site:myshopify.com price shopping cost',
    'site:myshopify.com price shopping deal',
    'site:myshopify.com low price hunter',
    'site:myshopify.com low price hunter items',
    'site:myshopify.com low price hunter products',
    'site:myshopify.com low price hunter price',
    'site:myshopify.com low price hunter cost',
    'site:myshopify.com low price hunter deal',
    'site:myshopify.com low price seeker',
    'site:myshopify.com low price seeker items',
    'site:myshopify.com low price seeker products',
    'site:myshopify.com low price seeker price',
    'site:myshopify.com low price seeker cost',
    'site:myshopify.com low price seeker deal',
    'site:myshopify.com low price shopper',
    'site:myshopify.com low price shopper items',
    'site:myshopify.com low price shopper products',
    'site:myshopify.com low price shopper price',
    'site:myshopify.com low price shopper cost',
    'site:myshopify.com low price shopper deal',
    'site:myshopify.com low price buyer',
    'site:myshopify.com low price buyer items',
    'site:myshopify.com low price buyer products',
    'site:myshopify.com low price buyer price',
    'site:myshopify.com low price buyer cost',
    'site:myshopify.com low price buyer deal',
    'site:myshopify.com low price shopping',
    'site:myshopify.com low price shopping items',
    'site:myshopify.com low price shopping products',
    'site:myshopify.com low price shopping price',
    'site:myshopify.com low price shopping cost',
    'site:myshopify.com low price shopping deal',
    'site:myshopify.com lowest price hunter',
    'site:myshopify.com lowest price hunter items',
    'site:myshopify.com lowest price hunter products',
    'site:myshopify.com lowest price hunter price',
    'site:myshopify.com lowest price hunter cost',
    'site:myshopify.com lowest price hunter deal',
    'site:myshopify.com lowest price seeker',
    'site:myshopify.com lowest price seeker items',
    'site:myshopify.com lowest price seeker products',
    'site:myshopify.com lowest price seeker price',
    'site:myshopify.com lowest price seeker cost',
    'site:myshopify.com lowest price seeker deal',
    'site:myshopify.com lowest price shopper',
    'site:myshopify.com lowest price shopper items',
    'site:myshopify.com lowest price shopper products',
    'site:myshopify.com lowest price shopper price',
    'site:myshopify.com lowest price shopper cost',
    'site:myshopify.com lowest price shopper deal',
    'site:myshopify.com lowest price buyer',
    'site:myshopify.com lowest price buyer items',
    'site:myshopify.com lowest price buyer products',
    'site:myshopify.com lowest price buyer price',
    'site:myshopify.com lowest price buyer cost',
    'site:myshopify.com lowest price buyer deal',
    'site:myshopify.com lowest price shopping',
    'site:myshopify.com lowest price shopping items',
    'site:myshopify.com lowest price shopping products',
    'site:myshopify.com lowest price shopping price',
    'site:myshopify.com lowest price shopping cost',
    'site:myshopify.com lowest price shopping deal',
    'site:myshopify.com best price hunter',
    'site:myshopify.com best price hunter items',
    'site:myshopify.com best price hunter products',
    'site:myshopify.com best price hunter price',
    'site:myshopify.com best price hunter cost',
    'site:myshopify.com best price hunter deal',
    'site:myshopify.com best price seeker',
    'site:myshopify.com best price seeker items',
    'site:myshopify.com best price seeker products',
    'site:myshopify.com best price seeker price',
    'site:myshopify.com best price seeker cost',
    'site:myshopify.com best price seeker deal',
    'site:myshopify.com best price shopper',
    'site:myshopify.com best price shopper items',
    'site:myshopify.com best price shopper products',
    'site:myshopify.com best price shopper price',
    'site:myshopify.com best price shopper cost',
    'site:myshopify.com best price shopper deal',
    'site:myshopify.com best price buyer',
    'site:myshopify.com best price buyer items',
    'site:myshopify.com best price buyer products',
    'site:myshopify.com best price buyer price',
    'site:myshopify.com best price buyer cost',
    'site:myshopify.com best price buyer deal',
    'site:myshopify.com best price shopping',
    'site:myshopify.com best price shopping items',
    'site:myshopify.com best price shopping products',
    'site:myshopify.com best price shopping price',
    'site:myshopify.com best price shopping cost',
    'site:myshopify.com best price shopping deal',
    'site:myshopify.com rock bottom hunter',
    'site:myshopify.com rock bottom hunter items',
    'site:myshopify.com rock bottom hunter products',
    'site:myshopify.com rock bottom hunter price',
    'site:myshopify.com rock bottom hunter cost',
    'site:myshopify.com rock bottom hunter deal',
    'site:myshopify.com rock bottom seeker',
    'site:myshopify.com rock bottom seeker items',
    'site:myshopify.com rock bottom seeker products',
    'site:myshopify.com rock bottom seeker price',
    'site:myshopify.com rock bottom seeker cost',
    'site:myshopify.com rock bottom seeker deal',
    'site:myshopify.com rock bottom shopper',
    'site:myshopify.com rock bottom shopper items',
    'site:myshopify.com rock bottom shopper products',
    'site:myshopify.com rock bottom shopper price',
    'site:myshopify.com rock bottom shopper cost',
    'site:myshopify.com rock bottom shopper deal',
    'site:myshopify.com rock bottom buyer',
    'site:myshopify.com rock bottom buyer items',
    'site:myshopify.com rock bottom buyer products',
    'site:myshopify.com rock bottom buyer price',
    'site:myshopify.com rock bottom buyer cost',
    'site:myshopify.com rock bottom buyer deal',
    'site:myshopify.com rock bottom shopping',
    'site:myshopify.com rock bottom shopping items',
    'site:myshopify.com rock bottom shopping products',
    'site:myshopify.com rock bottom shopping price',
    'site:myshopify.com rock bottom shopping cost',
    'site:myshopify.com rock bottom shopping deal',
    'site:myshopify.com dirt cheap hunter',
    'site:myshopify.com dirt cheap hunter items',
    'site:myshopify.com dirt cheap hunter products',
    'site:myshopify.com dirt cheap hunter price',
    'site:myshopify.com dirt cheap hunter cost',
    'site:myshopify.com dirt cheap hunter deal',
    'site:myshopify.com dirt cheap seeker',
    'site:myshopify.com dirt cheap seeker items',
    'site:myshopify.com dirt cheap seeker products',
    'site:myshopify.com dirt cheap seeker price',
    'site:myshopify.com dirt cheap seeker cost',
    'site:myshopify.com dirt cheap seeker deal',
    'site:myshopify.com dirt cheap shopper',
    'site:myshopify.com dirt cheap shopper items',
    'site:myshopify.com dirt cheap shopper products',
    'site:myshopify.com dirt cheap shopper price',
    'site:myshopify.com dirt cheap shopper cost',
    'site:myshopify.com dirt cheap shopper deal',
    'site:myshopify.com dirt cheap buyer',
    'site:myshopify.com dirt cheap buyer items',
    'site:myshopify.com dirt cheap buyer products',
    'site:myshopify.com dirt cheap buyer price',
    'site:myshopify.com dirt cheap buyer cost',
    'site:myshopify.com dirt cheap buyer deal',
    'site:myshopify.com dirt cheap shopping',
    'site:myshopify.com dirt cheap shopping items',
    'site:myshopify.com dirt cheap shopping products',
    'site:myshopify.com dirt cheap shopping price',
    'site:myshopify.com dirt cheap shopping cost',
    'site:myshopify.com dirt cheap shopping deal',
    'site:myshopify.com super cheap hunter',
    'site:myshopify.com super cheap hunter items',
    'site:myshopify.com super cheap hunter products',
    'site:myshopify.com super cheap hunter price',
    'site:myshopify.com super cheap hunter cost',
    'site:myshopify.com super cheap hunter deal',
    'site:myshopify.com super cheap seeker',
    'site:myshopify.com super cheap seeker items',
    'site:myshopify.com super cheap seeker products',
    'site:myshopify.com super cheap seeker price',
    'site:myshopify.com super cheap seeker cost',
    'site:myshopify.com super cheap seeker deal',
    'site:myshopify.com super cheap shopper',
    'site:myshopify.com super cheap shopper items',
    'site:myshopify.com super cheap shopper products',
    'site:myshopify.com super cheap shopper price',
    'site:myshopify.com super cheap shopper cost',
    'site:myshopify.com super cheap shopper deal',
    'site:myshopify.com super cheap buyer',
    'site:myshopify.com super cheap buyer items',
    'site:myshopify.com super cheap buyer products',
    'site:myshopify.com super cheap buyer price',
    'site:myshopify.com super cheap buyer cost',
    'site:myshopify.com super cheap buyer deal',
    'site:myshopify.com super cheap shopping',
    'site:myshopify.com super cheap shopping items',
    'site:myshopify.com super cheap shopping products',
    'site:myshopify.com super cheap shopping price',
    'site:myshopify.com super cheap shopping cost',
    'site:myshopify.com super cheap shopping deal',
    'site:myshopify.com ultra cheap hunter',
    'site:myshopify.com ultra cheap hunter items',
    'site:myshopify.com ultra cheap hunter products',
    'site:myshopify.com ultra cheap hunter price',
    'site:myshopify.com ultra cheap hunter cost',
    'site:myshopify.com ultra cheap hunter deal',
    'site:myshopify.com ultra cheap seeker',
    'site:myshopify.com ultra cheap seeker items',
    'site:myshopify.com ultra cheap seeker products',
    'site:myshopify.com ultra cheap seeker price',
    'site:myshopify.com ultra cheap seeker cost',
    'site:myshopify.com ultra cheap seeker deal',
    'site:myshopify.com ultra cheap shopper',
    'site:myshopify.com ultra cheap shopper items',
    'site:myshopify.com ultra cheap shopper products',
    'site:myshopify.com ultra cheap shopper price',
    'site:myshopify.com ultra cheap shopper cost',
    'site:myshopify.com ultra cheap shopper deal',
    'site:myshopify.com ultra cheap buyer',
    'site:myshopify.com ultra cheap buyer items',
    'site:myshopify.com ultra cheap buyer products',
    'site:myshopify.com ultra cheap buyer price',
    'site:myshopify.com ultra cheap buyer cost',
    'site:myshopify.com ultra cheap buyer deal',
    'site:myshopify.com ultra cheap shopping',
    'site:myshopify.com ultra cheap shopping items',
    'site:myshopify.com ultra cheap shopping products',
    'site:myshopify.com ultra cheap shopping price',
    'site:myshopify.com ultra cheap shopping cost',
    'site:myshopify.com ultra cheap shopping deal',
    
   
    'site:myshopify.com a',
    'site:myshopify.com b',
    'site:myshopify.com c',
    'site:myshopify.com d',
    'site:myshopify.com e',
    'site:myshopify.com f',
    'site:myshopify.com g',
    'site:myshopify.com h',
    'site:myshopify.com i',
    'site:myshopify.com j',
    'site:myshopify.com k',
    'site:myshopify.com l',
    'site:myshopify.com m',
    'site:myshopify.com n',
    'site:myshopify.com o',
    'site:myshopify.com p',
    'site:myshopify.com q',
    'site:myshopify.com r',
    'site:myshopify.com s',
    'site:myshopify.com t',
    'site:myshopify.com u',
    'site:myshopify.com v',
    'site:myshopify.com w',
    'site:myshopify.com x',
    'site:myshopify.com y',
    'site:myshopify.com z',
    

    'site:myshopify.com aa',
    'site:myshopify.com ab',
    'site:myshopify.com ac',
    'site:myshopify.com ad',
    'site:myshopify.com ae',
    'site:myshopify.com af',
    'site:myshopify.com ag',
    'site:myshopify.com ah',
    'site:myshopify.com ai',
    'site:myshopify.com aj',
    'site:myshopify.com ak',
    'site:myshopify.com al',
    'site:myshopify.com am',
    'site:myshopify.com an',
    'site:myshopify.com ao',
    'site:myshopify.com ap',
    'site:myshopify.com aq',
    'site:myshopify.com ar',
    'site:myshopify.com as',
    'site:myshopify.com at',
    'site:myshopify.com au',
    'site:myshopify.com av',
    'site:myshopify.com aw',
    'site:myshopify.com ax',
    'site:myshopify.com ay',
    'site:myshopify.com az',
    'site:myshopify.com ba',
    'site:myshopify.com bb',
    'site:myshopify.com bc',
    'site:myshopify.com bd',
    'site:myshopify.com be',
    'site:myshopify.com bf',
    'site:myshopify.com bg',
    'site:myshopify.com bh',
    'site:myshopify.com bi',
    'site:myshopify.com bj',
    'site:myshopify.com bk',
    'site:myshopify.com bl',
    'site:myshopify.com bm',
    'site:myshopify.com bn',
    'site:myshopify.com bo',
    'site:myshopify.com bp',
    'site:myshopify.com bq',
    'site:myshopify.com br',
    'site:myshopify.com bs',
    'site:myshopify.com bt',
    'site:myshopify.com bu',
    'site:myshopify.com bv',
    'site:myshopify.com bw',
    'site:myshopify.com bx',
    'site:myshopify.com by',
    'site:myshopify.com bz',
    'site:myshopify.com ca',
    'site:myshopify.com cb',
    'site:myshopify.com cc',
    'site:myshopify.com cd',
    'site:myshopify.com ce',
    'site:myshopify.com cf',
    'site:myshopify.com cg',
    'site:myshopify.com ch',
    'site:myshopify.com ci',
    'site:myshopify.com cj',
    'site:myshopify.com ck',
    'site:myshopify.com cl',
    'site:myshopify.com cm',
    'site:myshopify.com cn',
    'site:myshopify.com co',
    'site:myshopify.com cp',
    'site:myshopify.com cq',
    'site:myshopify.com cr',
    'site:myshopify.com cs',
    'site:myshopify.com ct',
    'site:myshopify.com cu',
    'site:myshopify.com cv',
    'site:myshopify.com cw',
    'site:myshopify.com cx',
    'site:myshopify.com cy',
    'site:myshopify.com cz',
    'site:myshopify.com da',
    'site:myshopify.com db',
    'site:myshopify.com dc',
    'site:myshopify.com dd',
    'site:myshopify.com de',
    'site:myshopify.com df',
    'site:myshopify.com dg',
    'site:myshopify.com dh',
    'site:myshopify.com di',
    'site:myshopify.com dj',
    'site:myshopify.com dk',
    'site:myshopify.com dl',
    'site:myshopify.com dm',
    'site:myshopify.com dn',
    'site:myshopify.com do',
    'site:myshopify.com dp',
    'site:myshopify.com dq',
    'site:myshopify.com dr',
    'site:myshopify.com ds',
    'site:myshopify.com dt',
    'site:myshopify.com du',
    'site:myshopify.com dv',
    'site:myshopify.com dw',
    'site:myshopify.com dx',
    'site:myshopify.com dy',
    'site:myshopify.com dz',
    'site:myshopify.com ea',
    'site:myshopify.com eb',
    'site:myshopify.com ec',
    'site:myshopify.com ed',
    'site:myshopify.com ee',
    'site:myshopify.com ef',
    'site:myshopify.com eg',
    'site:myshopify.com eh',
    'site:myshopify.com ei',
    'site:myshopify.com ej',
    'site:myshopify.com ek',
    'site:myshopify.com el',
    'site:myshopify.com em',
    'site:myshopify.com en',
    'site:myshopify.com eo',
    'site:myshopify.com ep',
    'site:myshopify.com eq',
    'site:myshopify.com er',
    'site:myshopify.com es',
    'site:myshopify.com et',
    'site:myshopify.com eu',
    'site:myshopify.com ev',
    'site:myshopify.com ew',
    'site:myshopify.com ex',
    'site:myshopify.com ey',
    'site:myshopify.com ez',
    'site:myshopify.com fa',
    'site:myshopify.com fb',
    'site:myshopify.com fc',
    'site:myshopify.com fd',
    'site:myshopify.com fe',
    'site:myshopify.com ff',
    'site:myshopify.com fg',
    'site:myshopify.com fh',
    'site:myshopify.com fi',
    'site:myshopify.com fj',
    'site:myshopify.com fk',
    'site:myshopify.com fl',
    'site:myshopify.com fm',
    'site:myshopify.com fn',
    'site:myshopify.com fo',
    'site:myshopify.com fp',
    'site:myshopify.com fq',
    'site:myshopify.com fr',
    'site:myshopify.com fs',
    'site:myshopify.com ft',
    'site:myshopify.com fu',
    'site:myshopify.com fv',
    'site:myshopify.com fw',
    'site:myshopify.com fx',
    'site:myshopify.com fy',
    'site:myshopify.com fz',
    'site:myshopify.com ga',
    'site:myshopify.com gb',
    'site:myshopify.com gc',
    'site:myshopify.com gd',
    'site:myshopify.com ge',
    'site:myshopify.com gf',
    'site:myshopify.com gg',
    'site:myshopify.com gh',
    'site:myshopify.com gi',
    'site:myshopify.com gj',
    'site:myshopify.com gk',
    'site:myshopify.com gl',
    'site:myshopify.com gm',
    'site:myshopify.com gn',
    'site:myshopify.com go',
    'site:myshopify.com gp',
    'site:myshopify.com gq',
    'site:myshopify.com gr',
    'site:myshopify.com gs',
    'site:myshopify.com gt',
    'site:myshopify.com gu',
    'site:myshopify.com gv',
    'site:myshopify.com gw',
    'site:myshopify.com gx',
    'site:myshopify.com gy',
    'site:myshopify.com gz',
    'site:myshopify.com ha',
    'site:myshopify.com hb',
    'site:myshopify.com hc',
    'site:myshopify.com hd',
    'site:myshopify.com he',
    'site:myshopify.com hf',
    'site:myshopify.com hg',
    'site:myshopify.com hh',
    'site:myshopify.com hi',
    'site:myshopify.com hj',
    'site:myshopify.com hk',
    'site:myshopify.com hl',
    'site:myshopify.com hm',
    'site:myshopify.com hn',
    'site:myshopify.com ho',
    'site:myshopify.com hp',
    'site:myshopify.com hq',
    'site:myshopify.com hr',
    'site:myshopify.com hs',
    'site:myshopify.com ht',
    'site:myshopify.com hu',
    'site:myshopify.com hv',
    'site:myshopify.com hw',
    'site:myshopify.com hx',
    'site:myshopify.com hy',
    'site:myshopify.com hz',
    
  
    'site:myshopify.com the',
    'site:myshopify.com my',
    'site:myshopify.com shop',
    'site:myshopify.com store',
    'site:myshopify.com official',
    'site:myshopify.com online',
    'site:myshopify.com boutique',
    'site:myshopify.com co',
    'site:myshopify.com llc',
    'site:myshopify.com inc',
    'site:myshopify.com brand',
    'site:myshopify.com company',
    'site:myshopify.com goods',
    'site:myshopify.com supply',
    'site:myshopify.com trading',
    'site:myshopify.com world',
    'site:myshopify.com global',
    'site:myshopify.com usa',
    'site:myshopify.com uk',
    'site:myshopify.com eu',
    'site:myshopify.com au',
    'site:myshopify.com ca',
    'site:myshopify.com nz',
    'site:myshopify.com de',
    'site:myshopify.com fr',
    

    'site:myshopify.com 1',
    'site:myshopify.com 2',
    'site:myshopify.com 3',
    'site:myshopify.com 4',
    'site:myshopify.com 5',
    'site:myshopify.com 6',
    'site:myshopify.com 7',
    'site:myshopify.com 8',
    'site:myshopify.com 9',
    'site:myshopify.com 0',
    'site:myshopify.com 10',
    'site:myshopify.com 20',
    'site:myshopify.com 50',
    'site:myshopify.com 100',
    'site:myshopify.com 2024',
    'site:myshopify.com 2025',
    'site:myshopify.com 2026',
    
    # More product categories
    'site:myshopify.com electronics',
    'site:myshopify.com gadgets',
    'site:myshopify.com tech',
    'site:myshopify.com fitness',
    'site:myshopify.com sports',
    'site:myshopify.com outdoor',
    'site:myshopify.com camping',
    'site:myshopify.com hiking',
    'site:myshopify.com yoga',
    'site:myshopify.com gym',
    'site:myshopify.com pet',
    'site:myshopify.com dog',
    'site:myshopify.com cat',
    'site:myshopify.com baby',
    'site:myshopify.com kids',
    'site:myshopify.com toys',
    'site:myshopify.com games',
    'site:myshopify.com art',
    'site:myshopify.com craft',
    'site:myshopify.com handmade',
    'site:myshopify.com vintage',
    'site:myshopify.com antique',
    'site:myshopify.com collectible',
    'site:myshopify.com book',
    'site:myshopify.com stationery',
    'site:myshopify.com office',
    'site:myshopify.com garden',
    'site:myshopify.com plant',
    'site:myshopify.com flower',
    'site:myshopify.com food',
    'site:myshopify.com gourmet',
    'site:myshopify.com coffee',
    'site:myshopify.com tea',
    'site:myshopify.com chocolate',
    'site:myshopify.com snack',
    'site:myshopify.com organic',
    'site:myshopify.com vegan',
    'site:myshopify.com natural',
    'site:myshopify.com eco',
    'site:myshopify.com sustainable',
    'site:myshopify.com green',
    'site:myshopify.com wellness',
    'site:myshopify.com health',
    'site:myshopify.com vitamin',
    'site:myshopify.com supplement',
    'site:myshopify.com cbd',
    'site:myshopify.com hemp',
    'site:myshopify.com car',
    'site:myshopify.com auto',
    'site:myshopify.com motorcycle',
    'site:myshopify.com bike',
    'site:myshopify.com travel',
    'site:myshopify.com luggage',
    'site:myshopify.com photography',
    'site:myshopify.com camera',
    'site:myshopify.com music',
    'site:myshopify.com instrument',
    'site:myshopify.com audio',
    'site:myshopify.com headphone',
    'site:myshopify.com speaker',
    
    # More two-letter combos (I through Z start)
    'site:myshopify.com ia',
    'site:myshopify.com ib',
    'site:myshopify.com ic',
    'site:myshopify.com id',
    'site:myshopify.com ie',
    'site:myshopify.com if',
    'site:myshopify.com ig',
    'site:myshopify.com ih',
    'site:myshopify.com ii',
    'site:myshopify.com ij',
    'site:myshopify.com ik',
    'site:myshopify.com il',
    'site:myshopify.com im',
    'site:myshopify.com in',
    'site:myshopify.com io',
    'site:myshopify.com ip',
    'site:myshopify.com iq',
    'site:myshopify.com ir',
    'site:myshopify.com is',
    'site:myshopify.com it',
    'site:myshopify.com iu',
    'site:myshopify.com iv',
    'site:myshopify.com iw',
    'site:myshopify.com ix',
    'site:myshopify.com iy',
    'site:myshopify.com iz',
    'site:myshopify.com ja',
    'site:myshopify.com jb',
    'site:myshopify.com jc',
    'site:myshopify.com jd',
    'site:myshopify.com je',
    'site:myshopify.com jf',
    'site:myshopify.com jg',
    'site:myshopify.com jh',
    'site:myshopify.com ji',
    'site:myshopify.com jj',
    'site:myshopify.com jk',
    'site:myshopify.com jl',
    'site:myshopify.com jm',
    'site:myshopify.com jn',
    'site:myshopify.com jo',
    'site:myshopify.com jp',
    'site:myshopify.com jq',
    'site:myshopify.com jr',
    'site:myshopify.com js',
    'site:myshopify.com jt',
    'site:myshopify.com ju',
    'site:myshopify.com jv',
    'site:myshopify.com jw',
    'site:myshopify.com jx',
    'site:myshopify.com jy',
    'site:myshopify.com jz',
    'site:myshopify.com ka',
    'site:myshopify.com kb',
    'site:myshopify.com kc',
    'site:myshopify.com kd',
    'site:myshopify.com ke',
    'site:myshopify.com kf',
    'site:myshopify.com kg',
    'site:myshopify.com kh',
    'site:myshopify.com ki',
    'site:myshopify.com kj',
    'site:myshopify.com kk',
    'site:myshopify.com kl',
    'site:myshopify.com km',
    'site:myshopify.com kn',
    'site:myshopify.com ko',
    'site:myshopify.com kp',
    'site:myshopify.com kq',
    'site:myshopify.com kr',
    'site:myshopify.com ks',
    'site:myshopify.com kt',
    'site:myshopify.com ku',
    'site:myshopify.com kv',
    'site:myshopify.com kw',
    'site:myshopify.com kx',
    'site:myshopify.com ky',
    'site:myshopify.com kz',
    'site:myshopify.com la',
    'site:myshopify.com lb',
    'site:myshopify.com lc',
    'site:myshopify.com ld',
    'site:myshopify.com le',
    'site:myshopify.com lf',
    'site:myshopify.com lg',
    'site:myshopify.com lh',
    'site:myshopify.com li',
    'site:myshopify.com lj',
    'site:myshopify.com lk',
    'site:myshopify.com ll',
    'site:myshopify.com lm',
    'site:myshopify.com ln',
    'site:myshopify.com lo',
    'site:myshopify.com lp',
    'site:myshopify.com lq',
    'site:myshopify.com lr',
    'site:myshopify.com ls',
    'site:myshopify.com lt',
    'site:myshopify.com lu',
    'site:myshopify.com lv',
    'site:myshopify.com lw',
    'site:myshopify.com lx',
    'site:myshopify.com ly',
    'site:myshopify.com lz',
    'site:myshopify.com ma',
    'site:myshopify.com mb',
    'site:myshopify.com mc',
    'site:myshopify.com md',
    'site:myshopify.com me',
    'site:myshopify.com mf',
    'site:myshopify.com mg',
    'site:myshopify.com mh',
    'site:myshopify.com mi',
    'site:myshopify.com mj',
    'site:myshopify.com mk',
    'site:myshopify.com ml',
    'site:myshopify.com mm',
    'site:myshopify.com mn',
    'site:myshopify.com mo',
    'site:myshopify.com mp',
    'site:myshopify.com mq',
    'site:myshopify.com mr',
    'site:myshopify.com ms',
    'site:myshopify.com mt',
    'site:myshopify.com mu',
    'site:myshopify.com mv',
    'site:myshopify.com mw',
    'site:myshopify.com mx',
    'site:myshopify.com my',
    'site:myshopify.com mz',
    'site:myshopify.com na',
    'site:myshopify.com nb',
    'site:myshopify.com nc',
    'site:myshopify.com nd',
    'site:myshopify.com ne',
    'site:myshopify.com nf',
    'site:myshopify.com ng',
    'site:myshopify.com nh',
    'site:myshopify.com ni',
    'site:myshopify.com nj',
    'site:myshopify.com nk',
    'site:myshopify.com nl',
    'site:myshopify.com nm',
    'site:myshopify.com nn',
    'site:myshopify.com no',
    'site:myshopify.com np',
    'site:myshopify.com nq',
    'site:myshopify.com nr',
    'site:myshopify.com ns',
    'site:myshopify.com nt',
    'site:myshopify.com nu',
    'site:myshopify.com nv',
    'site:myshopify.com nw',
    'site:myshopify.com nx',
    'site:myshopify.com ny',
    'site:myshopify.com nz',
    'site:myshopify.com oa',
    'site:myshopify.com ob',
    'site:myshopify.com oc',
    'site:myshopify.com od',
    'site:myshopify.com oe',
    'site:myshopify.com of',
    'site:myshopify.com og',
    'site:myshopify.com oh',
    'site:myshopify.com oi',
    'site:myshopify.com oj',
    'site:myshopify.com ok',
    'site:myshopify.com ol',
    'site:myshopify.com om',
    'site:myshopify.com on',
    'site:myshopify.com oo',
    'site:myshopify.com op',
    'site:myshopify.com oq',
    'site:myshopify.com or',
    'site:myshopify.com os',
    'site:myshopify.com ot',
    'site:myshopify.com ou',
    'site:myshopify.com ov',
    'site:myshopify.com ow',
    'site:myshopify.com ox',
    'site:myshopify.com oy',
    'site:myshopify.com oz',
    'site:myshopify.com pa',
    'site:myshopify.com pb',
    'site:myshopify.com pc',
    'site:myshopify.com pd',
    'site:myshopify.com pe',
    'site:myshopify.com pf',
    'site:myshopify.com pg',
    'site:myshopify.com ph',
    'site:myshopify.com pi',
    'site:myshopify.com pj',
    'site:myshopify.com pk',
    'site:myshopify.com pl',
    'site:myshopify.com pm',
    'site:myshopify.com pn',
    'site:myshopify.com po',
    'site:myshopify.com pp',
    'site:myshopify.com pq',
    'site:myshopify.com pr',
    'site:myshopify.com ps',
    'site:myshopify.com pt',
    'site:myshopify.com pu',
    'site:myshopify.com pv',
    'site:myshopify.com pw',
    'site:myshopify.com px',
    'site:myshopify.com py',
    'site:myshopify.com pz',
    'site:myshopify.com qa',
    'site:myshopify.com qb',
    'site:myshopify.com qc',
    'site:myshopify.com qd',
    'site:myshopify.com qe',
    'site:myshopify.com qf',
    'site:myshopify.com qg',
    'site:myshopify.com qh',
    'site:myshopify.com qi',
    'site:myshopify.com qj',
    'site:myshopify.com qk',
    'site:myshopify.com ql',
    'site:myshopify.com qm',
    'site:myshopify.com qn',
    'site:myshopify.com qo',
    'site:myshopify.com qp',
    'site:myshopify.com qq',
    'site:myshopify.com qr',
    'site:myshopify.com qs',
    'site:myshopify.com qt',
    'site:myshopify.com qu',
    'site:myshopify.com qv',
    'site:myshopify.com qw',
    'site:myshopify.com qx',
    'site:myshopify.com qy',
    'site:myshopify.com qz',
    'site:myshopify.com ra',
    'site:myshopify.com rb',
    'site:myshopify.com rc',
    'site:myshopify.com rd',
    'site:myshopify.com re',
    'site:myshopify.com rf',
    'site:myshopify.com rg',
    'site:myshopify.com rh',
    'site:myshopify.com ri',
    'site:myshopify.com rj',
    'site:myshopify.com rk',
    'site:myshopify.com rl',
    'site:myshopify.com rm',
    'site:myshopify.com rn',
    'site:myshopify.com ro',
    'site:myshopify.com rp',
    'site:myshopify.com rq',
    'site:myshopify.com rr',
    'site:myshopify.com rs',
    'site:myshopify.com rt',
    'site:myshopify.com ru',
    'site:myshopify.com rv',
    'site:myshopify.com rw',
    'site:myshopify.com rx',
    'site:myshopify.com ry',
    'site:myshopify.com rz',
    'site:myshopify.com sa',
    'site:myshopify.com sb',
    'site:myshopify.com sc',
    'site:myshopify.com sd',
    'site:myshopify.com se',
    'site:myshopify.com sf',
    'site:myshopify.com sg',
    'site:myshopify.com sh',
    'site:myshopify.com si',
    'site:myshopify.com sj',
    'site:myshopify.com sk',
    'site:myshopify.com sl',
    'site:myshopify.com sm',
    'site:myshopify.com sn',
    'site:myshopify.com so',
    'site:myshopify.com sp',
    'site:myshopify.com sq',
    'site:myshopify.com sr',
    'site:myshopify.com ss',
    'site:myshopify.com st',
    'site:myshopify.com su',
    'site:myshopify.com sv',
    'site:myshopify.com sw',
    'site:myshopify.com sx',
    'site:myshopify.com sy',
    'site:myshopify.com sz',
    'site:myshopify.com ta',
    'site:myshopify.com tb',
    'site:myshopify.com tc',
    'site:myshopify.com td',
    'site:myshopify.com te',
    'site:myshopify.com tf',
    'site:myshopify.com tg',
    'site:myshopify.com th',
    'site:myshopify.com ti',
    'site:myshopify.com tj',
    'site:myshopify.com tk',
    'site:myshopify.com tl',
    'site:myshopify.com tm',
    'site:myshopify.com tn',
    'site:myshopify.com to',
    'site:myshopify.com tp',
    'site:myshopify.com tq',
    'site:myshopify.com tr',
    'site:myshopify.com ts',
    'site:myshopify.com tt',
    'site:myshopify.com tu',
    'site:myshopify.com tv',
    'site:myshopify.com tw',
    'site:myshopify.com tx',
    'site:myshopify.com ty',
    'site:myshopify.com tz',
    'site:myshopify.com ua',
    'site:myshopify.com ub',
    'site:myshopify.com uc',
    'site:myshopify.com ud',
    'site:myshopify.com ue',
    'site:myshopify.com uf',
    'site:myshopify.com ug',
    'site:myshopify.com uh',
    'site:myshopify.com ui',
    'site:myshopify.com uj',
    'site:myshopify.com uk',
    'site:myshopify.com ul',
    'site:myshopify.com um',
    'site:myshopify.com un',
    'site:myshopify.com uo',
    'site:myshopify.com up',
    'site:myshopify.com uq',
    'site:myshopify.com ur',
    'site:myshopify.com us',
    'site:myshopify.com ut',
    'site:myshopify.com uu',
    'site:myshopify.com uv',
    'site:myshopify.com uw',
    'site:myshopify.com ux',
    'site:myshopify.com uy',
    'site:myshopify.com uz',
    'site:myshopify.com va',
    'site:myshopify.com vb',
    'site:myshopify.com vc',
    'site:myshopify.com vd',
    'site:myshopify.com ve',
    'site:myshopify.com vf',
    'site:myshopify.com vg',
    'site:myshopify.com vh',
    'site:myshopify.com vi',
    'site:myshopify.com vj',
    'site:myshopify.com vk',
    'site:myshopify.com vl',
    'site:myshopify.com vm',
    'site:myshopify.com vn',
    'site:myshopify.com vo',
    'site:myshopify.com vp',
    'site:myshopify.com vq',
    'site:myshopify.com vr',
    'site:myshopify.com vs',
    'site:myshopify.com vt',
    'site:myshopify.com vu',
    'site:myshopify.com vv',
    'site:myshopify.com vw',
    'site:myshopify.com vx',
    'site:myshopify.com vy',
    'site:myshopify.com vz',
    'site:myshopify.com wa',
    'site:myshopify.com wb',
    'site:myshopify.com wc',
    'site:myshopify.com wd',
    'site:myshopify.com we',
    'site:myshopify.com wf',
    'site:myshopify.com wg',
    'site:myshopify.com wh',
    'site:myshopify.com wi',
    'site:myshopify.com wj',
    'site:myshopify.com wk',
    'site:myshopify.com wl',
    'site:myshopify.com wm',
    'site:myshopify.com wn',
    'site:myshopify.com wo',
    'site:myshopify.com wp',
    'site:myshopify.com wq',
    'site:myshopify.com wr',
    'site:myshopify.com ws',
    'site:myshopify.com wt',
    'site:myshopify.com wu',
    'site:myshopify.com wv',
    'site:myshopify.com ww',
    'site:myshopify.com wx',
    'site:myshopify.com wy',
    'site:myshopify.com wz',
    'site:myshopify.com xa',
    'site:myshopify.com xb',
    'site:myshopify.com xc',
    'site:myshopify.com xd',
    'site:myshopify.com xe',
    'site:myshopify.com xf',
    'site:myshopify.com xg',
    'site:myshopify.com xh',
    'site:myshopify.com xi',
    'site:myshopify.com xj',
    'site:myshopify.com xk',
    'site:myshopify.com xl',
    'site:myshopify.com xm',
    'site:myshopify.com xn',
    'site:myshopify.com xo',
    'site:myshopify.com xp',
    'site:myshopify.com xq',
    'site:myshopify.com xr',
    'site:myshopify.com xs',
    'site:myshopify.com xt',
    'site:myshopify.com xu',
    'site:myshopify.com xv',
    'site:myshopify.com xw',
    'site:myshopify.com xx',
    'site:myshopify.com xy',
    'site:myshopify.com xz',
    'site:myshopify.com ya',
    'site:myshopify.com yb',
    'site:myshopify.com yc',
    'site:myshopify.com yd',
    'site:myshopify.com ye',
    'site:myshopify.com yf',
    'site:myshopify.com yg',
    'site:myshopify.com yh',
    'site:myshopify.com yi',
    'site:myshopify.com yj',
    'site:myshopify.com yk',
    'site:myshopify.com yl',
    'site:myshopify.com ym',
    'site:myshopify.com yn',
    'site:myshopify.com yo',
    'site:myshopify.com yp',
    'site:myshopify.com yq',
    'site:myshopify.com yr',
    'site:myshopify.com ys',
    'site:myshopify.com yt',
    'site:myshopify.com yu',
    'site:myshopify.com yv',
    'site:myshopify.com yw',
    'site:myshopify.com yx',
    'site:myshopify.com yy',
    'site:myshopify.com yz',
    'site:myshopify.com za',
    'site:myshopify.com zb',
    'site:myshopify.com zc',
    'site:myshopify.com zd',
    'site:myshopify.com ze',
    'site:myshopify.com zf',
    'site:myshopify.com zg',
    'site:myshopify.com zh',
    'site:myshopify.com zi',
    'site:myshopify.com zj',
    'site:myshopify.com zk',
    'site:myshopify.com zl',
    'site:myshopify.com zm',
    'site:myshopify.com zn',
    'site:myshopify.com zo',
    'site:myshopify.com zp',
    'site:myshopify.com zq',
    'site:myshopify.com zr',
    'site:myshopify.com zs',
    'site:myshopify.com zt',
    'site:myshopify.com zu',
    'site:myshopify.com zv',
    'site:myshopify.com zw',
    'site:myshopify.com zx',
    'site:myshopify.com zy',
    'site:myshopify.com zz',
]

MYSHOPIFY_RE = re.compile(r"https?://([a-z0-9\-]+)\.myshopify\.com", re.IGNORECASE)
MYSHOPIFY_RE_ALT1 = re.compile(r"([a-z0-9\-]+)\.myshopify\.com", re.IGNORECASE)
MYSHOPIFY_RE_ALT2 = re.compile(r"myshopify\.com/([a-z0-9\-]+)", re.IGNORECASE)
MYSHOPIFY_RE_FULL = re.compile(r"https?://([a-z0-9\-]+)\.myshopify\.com[^\s<>\"']*", re.IGNORECASE)

user_data = {}
stop_flags = {}
MAX_PROXY_WORKERS = 800   # Max for Railway Hobby (8GB) - dedicated
MAX_SCRAPE_WORKERS = 500  # Max for Railway Hobby (8GB) - dedicated

_session_cache = {}
_session_lock = threading.Lock()

# File saving for sites.txt
_sites_file_lock = threading.Lock()
# Use /data for Railway volume (persistent) or current dir for local
_sites_file_path = "/data/sites.txt" if os.path.exists("/data") else "sites.txt"
_saved_sites = set()  # Track what we've already saved to avoid duplicates

def get_session(proxy=None):
    key = proxy or 'no_proxy'
    if key not in _session_cache:
        with _session_lock:
            if key not in _session_cache:
                session = requests.Session()
                session.headers.update({
                    'User-Agent': random.choice(USER_AGENTS),
                    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
                    'Accept-Language': 'en-US,en;q=0.5',
                    'Connection': 'keep-alive',
                    'Accept-Encoding': 'gzip, deflate',
                    'DNT': '1'
                })
                _session_cache[key] = session
    return _session_cache[key]

def get_headers():
    return {
        'User-Agent': random.choice(USER_AGENTS),
        'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
        'Accept-Language': 'en-US,en;q=0.5',
        'Connection': 'keep-alive',
        'Accept-Encoding': 'gzip, deflate',
        'DNT': '1'
    }

def parse_proxy(line, proxy_type="http"):
    """Parse proxy line with support for http, socks4, socks5, mix"""
    line = line.strip()
    if not line or line.startswith('#'):
        return None
    
    # For MIX type - keep existing scheme or default to http://
    if proxy_type == "mix":
        # Check if line already has a scheme
        has_scheme = False
        for scheme in ['http://', 'https://', 'socks4://', 'socks5://', 'socks4a://', 'socks5h://']:
            if line.lower().startswith(scheme):
                has_scheme = True
                break
        
        if has_scheme:
            # Already has scheme, return as-is (just validate format)
            try:
                if ':' in line:
                    return line
            except:
                pass
            return None
        else:
            # No scheme, treat as HTTP
            proxy_type = "http"
    
    # Remove any existing scheme for non-mix types
    for scheme in ['http://', 'https://', 'socks4://', 'socks5://', 'socks4a://', 'socks5h://']:
        if line.lower().startswith(scheme):
            line = line[len(scheme):]
            break
    
    try:
        if '@' in line:
            return f"{proxy_type}://{line}"
        elif ':' in line:
            parts = line.split(':')
            if len(parts) == 4:
                return f"{proxy_type}://{parts[2]}:{parts[3]}@{parts[0]}:{parts[1]}"
            return f"{proxy_type}://{line}"
    except:
        pass
    return None

def save_site_to_file(site):
    """Save a single site to sites.txt immediately (thread-safe)"""
    global _saved_sites
    if not site or site in _saved_sites:
        return
    
    with _sites_file_lock:
        # Double-check after acquiring lock
        if site in _saved_sites:
            return
        
        try:
            # Append mode - creates file if it doesn't exist
            with open(_sites_file_path, 'a', encoding='utf-8', errors='replace') as f:
                f.write(f"{site}\n")
                f.flush()  # Ensure it's written immediately
                os.fsync(f.fileno())  # Force write to disk (important for Linux)
            _saved_sites.add(site)
        except Exception as e:
            print(f"❌ Error saving site to file: {e}")

def save_all_sites_to_file(sites):
    """Save all sites to sites.txt (thread-safe, removes duplicates)"""
    if not sites:
        return
    
    with _sites_file_lock:
        try:
            # Read existing sites to avoid duplicates
            existing_sites = set()
            if os.path.exists(_sites_file_path):
                try:
                    with open(_sites_file_path, 'r', encoding='utf-8', errors='replace') as f:
                        existing_sites = set(line.strip() for line in f if line.strip())
                except:
                    pass
            
            # Add new sites
            new_sites = set(sites) - existing_sites
            if new_sites:
                with open(_sites_file_path, 'a', encoding='utf-8', errors='replace') as f:
                    for site in sorted(new_sites):
                        f.write(f"{site}\n")
                    f.flush()
                    os.fsync(f.fileno())  # Force write to disk
            
            # Update saved sites set
            _saved_sites.update(sites)
        except Exception as e:
            print(f"❌ Error saving sites to file: {e}")

# Smart Engine Rotation System
_engine_stats = {}  # Track success/failure per engine
_engine_cooldowns = {}  # Cooldown timestamps for failed engines
_engine_round_robin_index = 0  # For round-robin fallback
_engine_lock = threading.Lock()

def reset_engine_stats():
    """Reset engine statistics - call at start of new scraping session"""
    global _engine_stats, _engine_cooldowns, _engine_round_robin_index
    with _engine_lock:
        _engine_stats = {}
        _engine_cooldowns = {}
        _engine_round_robin_index = 0

def record_engine_result(engine_name, success, urls_found=0):
    """Record success/failure for an engine"""
    with _engine_lock:
        if engine_name not in _engine_stats:
            _engine_stats[engine_name] = {'success': 0, 'fail': 0, 'urls': 0, 'consecutive_fails': 0}
        
        if success:
            _engine_stats[engine_name]['success'] += 1
            _engine_stats[engine_name]['urls'] += urls_found
            _engine_stats[engine_name]['consecutive_fails'] = 0
            # Remove from cooldown if successful
            if engine_name in _engine_cooldowns:
                del _engine_cooldowns[engine_name]
        else:
            _engine_stats[engine_name]['fail'] += 1
            _engine_stats[engine_name]['consecutive_fails'] += 1
            # Add cooldown after 3 consecutive failures
            if _engine_stats[engine_name]['consecutive_fails'] >= 3:
                _engine_cooldowns[engine_name] = time.time() + 30  # 30 second cooldown

def get_engine_score(engine):
    """Calculate engine score based on performance"""
    name = engine['name']
    with _engine_lock:
        if name not in _engine_stats:
            return engine['weight']  # Use default weight for new engines
        
        stats = _engine_stats[name]
        total = stats['success'] + stats['fail']
        if total == 0:
            return engine['weight']
        
        # Success rate (0-1)
        success_rate = stats['success'] / total
        # Bonus for finding URLs
        url_bonus = min(stats['urls'] / 100, 0.3)  # Max 0.3 bonus
        
        # Combined score
        return (engine['weight'] * 0.5) + (success_rate * 0.3) + url_bonus

def select_search_engine():
    """Smart engine selection with performance tracking and cooldowns"""
    global _engine_round_robin_index
    
    current_time = time.time()
    
    with _engine_lock:
        # Filter out engines on cooldown
        available_engines = [
            e for e in SEARCH_ENGINES 
            if e['name'] not in _engine_cooldowns or _engine_cooldowns[e['name']] < current_time
        ]
        
        # If all engines on cooldown, use round-robin on all engines
        if not available_engines:
            _engine_round_robin_index = (_engine_round_robin_index + 1) % len(SEARCH_ENGINES)
            return SEARCH_ENGINES[_engine_round_robin_index]
    
    # Calculate scores for available engines
    scored_engines = [(e, get_engine_score(e)) for e in available_engines]
    total_score = sum(score for _, score in scored_engines)
    
    if total_score <= 0:
        return random.choice(available_engines)
    
    # Weighted random selection based on scores
    rand = random.random() * total_score
    cumulative = 0
    for engine, score in scored_engines:
        cumulative += score
        if rand <= cumulative:
            return engine
    
    return available_engines[0]

def test_proxy_with_search(proxy):
    try:
        engine = random.choice(SEARCH_ENGINES)
        response = requests.get(
            engine['url'],
            params={engine['param']: 'site:myshopify.com test'},
            headers=get_headers(),
            proxies={"http": proxy, "https": proxy},
            timeout=10,
            verify=False,
            allow_redirects=True
        )
        
        if 200 <= response.status_code < 400:
            urls = extract_shopify_urls(response.text)
            return len(response.text) > 100
        return False
    except:
        return False

def _normalize_shopify_url(url):
    if not url:
        return None
    
    url = url.strip().strip('\'"')
    if not url:
        return None
    
    # Remove common trailing punctuation and stray slashes/backslashes
    url = re.sub(r'[\\\/]+$', '', url)
    url = url.rstrip('.,;!?)]}')
    
    if not url:
        return None
    
    if not url.lower().startswith(('http://', 'https://')):
        url = 'https://' + url.lstrip('/')
    
    url = url.replace('http://', 'https://', 1)
    if '#' in url:
        url = url.split('#', 1)[0]
    url = url.strip()
    
    parsed = urllib.parse.urlparse(url)
    netloc = parsed.netloc or parsed.path
    netloc = netloc.lower()
    
    if not netloc or '.myshopify.com' not in netloc:
        return None
    
    netloc = netloc.split('@', 1)[-1]
    netloc = netloc.split(':', 1)[0]
    
    return f"https://{netloc}"


def extract_shopify_urls(html):
    urls = set()
    
    def add_url(candidate):
        normalized = _normalize_shopify_url(candidate)
        if normalized:
            urls.add(normalized)
    
    for match in MYSHOPIFY_RE.finditer(html):
        add_url(match.group(0))
    
    for match in MYSHOPIFY_RE_FULL.finditer(html):
        add_url(match.group(0))
    
    for match in MYSHOPIFY_RE_ALT1.finditer(html):
        domain = match.group(1)
        if domain and len(domain) > 3:
            add_url(f"https://{domain}.myshopify.com")
            add_url(f"http://{domain}.myshopify.com")
    
    for match in MYSHOPIFY_RE_ALT2.finditer(html):
        domain = match.group(1)
        if domain and len(domain) > 3:
            add_url(f"https://{domain}.myshopify.com")
            add_url(f"http://{domain}.myshopify.com")
    
    href_pattern = re.compile(r'href=["\']([^"\']*\.myshopify\.com[^"\']*)["\']', re.IGNORECASE)
    for match in href_pattern.finditer(html):
        add_url(match.group(1))
    
    src_pattern = re.compile(r'src=["\']([^"\']*\.myshopify\.com[^"\']*)["\']', re.IGNORECASE)
    for match in src_pattern.finditer(html):
        add_url(match.group(1))
    
    return list(urls)

def fetch_page_parallel(engine, query, page, proxy=None):
    try:
        params = {engine['param']: query}
        
        if engine['name'] == 'DuckDuckGo':
            params['s'] = (page - 1) * 20
        elif engine['name'] == 'Brave':
            params['offset'] = (page - 1) * 10
        elif engine['name'] == 'Startpage':
            params['page'] = page
        elif engine['name'] == 'Mojeek':
            params['pn'] = page
        
        proxies = {"http": proxy, "https": proxy} if proxy else None
        
        session = get_session(proxy)
        response = session.get(
            engine['url'],
            params=params,
            proxies=proxies,
            timeout=8,
            verify=False,
            allow_redirects=True
        )
        
        if 200 <= response.status_code < 400:
            return extract_shopify_urls(response.text), True
        return [], False
    except:
        return [], False

def search_with_proxy(query, proxy=None, chat_id=None):
    max_retries = 3
    all_urls = set()
    
    for attempt in range(max_retries):
        try:
            engine = select_search_engine()
            engine_name = engine['name']
            
            with ThreadPoolExecutor(max_workers=20) as executor:
                futures = []
                for page in range(1, 51):  # 50 pages for MORE results
                    future = executor.submit(fetch_page_parallel, engine, query, page, proxy)
                    futures.append((page, future))
                
                pages_with_results = 0
                page_urls_total = 0
                for page, future in futures:
                    # Check stop flag before processing each page result
                    if chat_id and stop_flags.get(chat_id, False):
                        # Cancel remaining futures and return what we have
                        for _, f in futures:
                            if not f.done():
                                f.cancel()
                        return list(all_urls) if all_urls else []
                    
                    try:
                        page_urls, success = future.result(timeout=3)  # Reduced from 15s to 3s
                        if success:
                            pages_with_results += 1
                            if page_urls:
                                page_urls_total += len(page_urls)
                                all_urls.update(page_urls)
                                print(f"✅ [{engine_name}] Page {page}: Found {len(page_urls)} URLs")
                            elif page == 1 and not page_urls:
                                pass
                    except:
                        continue
            
            if all_urls:
                # Record success with URL count
                record_engine_result(engine_name, True, len(all_urls))
                print(f"✅ [{engine_name}] Total: {len(all_urls)} unique URLs from {pages_with_results} page(s)")
                return list(all_urls)
            else:
                # Record failure (no results)
                record_engine_result(engine_name, False)
                if attempt < max_retries - 1:
                    time.sleep(0.1)
                continue
                
        except Exception as e:
            # Record failure (exception)
            if 'engine_name' in locals():
                record_engine_result(engine_name, False)
            if attempt < max_retries - 1:
                time.sleep(0.1)
            continue
    
    return list(all_urls) if all_urls else []

def search_tempest(query, max_results=100):

    seen_domains = set()
    offset = 0
    results_per_page = 50

    session = requests.Session()
    session.verify = False

    while len(urls) < max_results and offset < 200:
        try:
            params = {
                "q": query,
                "count": str(results_per_page),
                "offset": str(offset),
            }
            if TEMPEST_COUNTRY:
                params["cc"] = TEMPEST_COUNTRY
                params["mkt"] = f"en-{TEMPEST_COUNTRY}"
            headers = {
                "User-Agent": IPAD_UA,
                "Accept": "application/json",
                "Accept-Language": "en-US,en;q=0.5",
                "Accept-Encoding": "gzip, deflate, br",
            }

            r = session.get(
                TEMPEST_V1_SEARCH,
                params=params,
                headers=headers,
                timeout=30,
                allow_redirects=True,
            )

            if r.status_code != 200 or not r.content:
                break

            data = r.json()
            web_pages = data.get("webPages", {})
            results = web_pages.get("value", [])

            if not results:
                break

            for result in results:
                url = result.get("url", "")
                if not url:
                    continue
                shopify_found = extract_shopify_urls(url)
                if shopify_found:
                    urls.extend(shopify_found)
                snippet = result.get("snippet", "") + " " + result.get("name", "")
                shopify_found2 = extract_shopify_urls(snippet)
                if shopify_found2:
                    urls.extend(shopify_found2)

            offset += results_per_page

            if offset % 100 == 0:
                time.sleep(random.uniform(0.5, 1.5))
            else:
                time.sleep(random.uniform(0.1, 0.3))

        except Exception:
            break

    return list(set(urls))


def search_proxyless(query, chat_id=None):
    """Search using proxyless engines (SearX instances, Yandex, etc.) with curl_cffi browser impersonation"""
    all_urls = set()
    
    # Try multiple engines without proxy
    engines_to_try = random.sample(PROXYLESS_ENGINES, min(3, len(PROXYLESS_ENGINES)))
    
    for engine in engines_to_try:
        if chat_id and stop_flags.get(chat_id, False):
            break
            
        try:
            params = {engine['param']: query}
            
            # Add format param for SearX instances
            if 'SearX' in engine['name'] or 'searx' in engine['url']:
                params['format'] = 'html'
                params['categories'] = 'general'
            
            # Use curl_cffi with browser impersonation if available
            if CURL_CFFI_AVAILABLE:
                response = curl_requests.get(
                    engine['url'],
                    params=params,
                    headers=get_headers(),
                    timeout=8,
                    allow_redirects=True,
                    impersonate=random.choice(BROWSER_IMPERSONATE)
                )
            else:
                response = requests.get(
                    engine['url'],
                    params=params,
                    headers=get_headers(),
                    timeout=8,
                    verify=False,
                    allow_redirects=True
                )
            
            if 200 <= response.status_code < 400:
                urls = extract_shopify_urls(response.text)
                if urls:
                    all_urls.update(urls)
                    print(f"🌐 [{engine['name']}] Found {len(urls)} URLs (proxyless)")
                    
            # Minimal delay for speed
            time.sleep(random.uniform(0.05, 0.15))
            
        except Exception as e:
            continue
    
    return list(all_urls)

def proxyless_worker(found, lock, searches_counter, chat_id, stop_check_func):
    """Worker that scrapes without proxies using lenient search engines"""
    worker_found = 0
    engine_index = 0
    
    # Use curl_cffi session if available
    if CURL_CFFI_AVAILABLE:
        session = curl_requests.Session()
    else:
        session = requests.Session()
        session.verify = False
    
    while True:
        if stop_check_func():
            break
            
        try:
            query = random.choice(DORKS)
            
            # Rotate through proxyless engines
            engine = PROXYLESS_ENGINES[engine_index % len(PROXYLESS_ENGINES)]
            engine_index += 1
            
            params = {engine['param']: query}
            
            # Add format param for SearX instances  
            if 'SearX' in engine['name'] or 'searx' in engine['url']:
                params['format'] = 'html'
                params['categories'] = 'general'
            
            # Use curl_cffi with browser impersonation if available
            if CURL_CFFI_AVAILABLE:
                response = session.get(
                    engine['url'],
                    params=params,
                    headers=get_headers(),
                    timeout=10,
                    allow_redirects=True,
                    impersonate=random.choice(BROWSER_IMPERSONATE)
                )
            else:
                response = session.get(
                    engine['url'],
                    params=params,
                    headers=get_headers(),
                    timeout=10,
                    allow_redirects=True
                )
            
            if 200 <= response.status_code < 400:
                urls = extract_shopify_urls(response.text)
                if urls:
                    with lock:
                        searches_counter[0] += 1
                        for url in urls:
                            if url not in found:
                                found.add(url)
                                worker_found += 1
                                print(f"🌐 [{engine['name']}] [{len(found)}] {url}")
                                save_site_to_file(url)
                else:
                    with lock:
                        searches_counter[0] += 1
            
            # Minimal delay for turbo speed
            time.sleep(random.uniform(0.1, 0.4))
            
        except Exception as e:
            time.sleep(0.2)
            continue
    
    return worker_found

async def test_proxies(chat_id, context, proxy_list):
    working = []
    tested = 0
    total = len(proxy_list)
    lock = threading.Lock()
    
    msg = await context.bot.send_message(
        chat_id, 
        f"🧪 **STRICT TESTING {total:,} PROXIES**\n\n✅ Testing with REAL search queries\n⚡ Only proxies that work with search engines will pass"
    )
    
    def worker():
        nonlocal tested
        while True:
            try:
                proxy = q.get_nowait()
                if test_proxy_with_search(proxy):
                    with lock:
                        working.append(proxy)
                        print(f"✅ Working proxy: {proxy[:50]}")
                with lock:
                    tested += 1
                q.task_done()
            except queue.Empty:
                break
            except:
                with lock:
                    tested += 1
                q.task_done()
    
    q = queue.Queue()
    for proxy in proxy_list:
        q.put(proxy)
    
    with ThreadPoolExecutor(max_workers=min(MAX_PROXY_WORKERS, total)) as executor:
        futures = [executor.submit(worker) for _ in range(min(MAX_PROXY_WORKERS, total))]
        
        while not all(f.done() for f in futures):
            await asyncio.sleep(3)
            with lock:
                if tested > 0:
                    pct = (tested / total) * 100
                    rate = len(working) / tested * 100 if tested > 0 else 0
                    try:
                        await context.bot.edit_message_text(
                            chat_id=chat_id,
                            message_id=msg.message_id,
                            text=f"🧪 **STRICT TESTING PROXIES**\n\n📊 Progress: {tested:,}/{total:,} ({pct:.1f}%)\n✅ Working: {len(working)} ({rate:.1f}%)\n🔍 Testing with real searches\n⚡ {len(SEARCH_ENGINES)} search engines",
                            parse_mode='Markdown'
                        )
                    except:
                        pass
    
    try:
        await context.bot.edit_message_text(
            chat_id=chat_id,
            message_id=msg.message_id,
            text=f"✅ **TESTING COMPLETE**\n\n🎯 Found: {len(working)}/{total} working proxies\n📊 Success rate: {len(working)/total*100:.1f}%\n🔍 All tested with real searches\n🚀 Ready for scraping!",
            parse_mode='Markdown'
        )
    except:
        pass
    
    return working

async def scrape_sites(chat_id, context, proxies):
    found = set()
    searches = 0
    failed_proxies = set()
    lock = threading.Lock()
    start_time = time.time()
    
    # Reset engine stats for fresh session
    reset_engine_stats()
    
    engine_stats = {e['name']: 0 for e in SEARCH_ENGINES}
    
    stop_flags[chat_id] = False
    
    msg = await context.bot.send_message(
        chat_id, 
        f"🔍 **MULTI-ENGINE SCRAPER V4.0**\n\n🎯 Keywords: {len(DORKS)}\n🌐 Proxies: {len(proxies)}\n🔍 Search Engines: {len(SEARCH_ENGINES)}\n⚡ Workers: {min(MAX_SCRAPE_WORKERS, len(proxies))}\n\n💪 Starting...",
        parse_mode='Markdown'
    )
    
    user_data[chat_id]['scraping'] = True
    user_data[chat_id]['found_sites'] = found
    
    proxy_pool = proxies.copy()
    random.shuffle(proxy_pool)
    proxy_index = 0
    
    def get_next_proxy():
        nonlocal proxy_index
        if not proxy_pool:
            return None
        
        attempts = 0
        while attempts < len(proxy_pool):
            proxy = proxy_pool[proxy_index % len(proxy_pool)]
            proxy_index += 1
            
            if proxy not in failed_proxies:
                return proxy
            attempts += 1
        return None
    
    def scraper_worker():
        nonlocal searches
        worker_found = 0
        consecutive_failures = 0
        
        for search_count in range(10000):
            if stop_flags.get(chat_id, False):
                break
            
            if not user_data[chat_id].get('scraping', False):
                break
            
            if consecutive_failures >= 500:
                consecutive_failures = 0  # Reset instead of stopping
                time.sleep(0.5)  # Brief pause then continue
                
            try:
                query = random.choice(DORKS)
                proxy = get_next_proxy()
                
                if not proxy:
                    # Reset failed proxies and try again
                    failed_proxies.clear()
                    proxy = get_next_proxy()
                    if not proxy:
                        break
                
                if stop_flags.get(chat_id, False):
                    break
                    
                urls = search_with_proxy(query, proxy, chat_id)
                
                if stop_flags.get(chat_id, False):
                    break
                
                if urls:
                    consecutive_failures = 0
                    with lock:
                        searches += 1
                        for url in urls:
                            if url not in found:
                                found.add(url)
                                worker_found += 1
                                print(f"✅ [{len(found)}] {url}")
                                # Save site immediately to file
                                save_site_to_file(url)
                else:
                    consecutive_failures += 1
                    with lock:
                        searches += 1
                        # DON'T mark proxy as failed - keep reusing
                
                time.sleep(random.uniform(0.005, 0.02))
                
            except Exception as e:
                consecutive_failures += 1
                with lock:
                    searches += 1
                    # DON'T mark proxy as failed on exception
                time.sleep(0.01)
                continue
        
        return worker_found
    
    # Proxyless worker function for this scraper - Enhanced for V6.0 TURBO with curl_cffi
    def proxyless_scraper_worker():
        nonlocal searches
        worker_found = 0
        engine_index = 0
        consecutive_failures = 0
        
        # Use curl_cffi session if available (MUCH better at bypassing detection)
        if CURL_CFFI_AVAILABLE:
            session = curl_requests.Session()
        else:
            session = requests.Session()
            session.verify = False
        
        for _ in range(10000):  # 10000 searches per proxyless worker
            if stop_flags.get(chat_id, False):
                break
            if not user_data[chat_id].get('scraping', False):
                break
            
            if consecutive_failures >= 15:
                # Engine might be rate limited, pause and reset
                time.sleep(1)
                consecutive_failures = 0
                
            try:
                query = random.choice(DORKS)
                engine = PROXYLESS_ENGINES[engine_index % len(PROXYLESS_ENGINES)]
                engine_index += 1
                
                headers = {
                    'User-Agent': random.choice(USER_AGENTS),
                    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
                    'Accept-Language': 'en-US,en;q=0.5',
                }
                
                # Add custom headers if engine has them (like Brave)
                if 'headers' in engine:
                    headers.update(engine['headers'])
                
                params = {engine['param']: query}
                if 'SearX' in engine['name'] or 'searx' in engine['url'].lower():
                    params['format'] = 'html'
                    params['categories'] = 'general'
                
                # Use curl_cffi with browser impersonation if available
                if CURL_CFFI_AVAILABLE:
                    response = session.get(
                        engine['url'],
                        params=params,
                        headers=headers,
                        timeout=8,
                        allow_redirects=True,
                        impersonate=random.choice(BROWSER_IMPERSONATE)
                    )
                else:
                    response = session.get(
                        engine['url'],
                        params=params,
                        headers=headers,
                        timeout=8,
                        allow_redirects=True
                    )
                
                if 200 <= response.status_code < 400:
                    urls = extract_shopify_urls(response.text)
                    if urls:
                        consecutive_failures = 0
                        with lock:
                            searches += 1
                            for url in urls:
                                if url not in found:
                                    found.add(url)
                                    worker_found += 1
                                    print(f"🌐 [{engine['name']}] [{len(found)}] {url}")
                                    save_site_to_file(url)
                    else:
                        consecutive_failures += 1
                        with lock:
                            searches += 1
                elif response.status_code in [429, 403, 503]:
                    consecutive_failures += 1
                    time.sleep(0.5)
                
                # TURBO delays - minimal waits
                if engine['name'] == 'Brave':
                    time.sleep(random.uniform(0.8, 1.5))
                elif 'SearX' in engine['name']:
                    time.sleep(random.uniform(0.1, 0.4))
                else:
                    time.sleep(random.uniform(0.3, 0.8))
                
            except:
                consecutive_failures += 1
                time.sleep(0.3)
                continue
        
        return worker_found
    
    workers = min(MAX_SCRAPE_WORKERS, len(proxies))
    
    # Only use proxyless if enabled (disable when your IP is blocked!)
    if USE_PROXYLESS_IN_DUAL_MODE:
        proxyless_workers = len(PROXYLESS_ENGINES) * 3
        print(f"🌐 Proxyless workers: {proxyless_workers} (enabled)")
    else:
        proxyless_workers = 0
        print(f"⚠️ Proxyless workers: DISABLED (your IP may be blocked)")
    
    # Start periodic saving task (every 10 seconds)
    last_periodic_save = time.time()
    periodic_save_interval = 10.0
    last_milestone_sent = 0  # Track last 1000 milestone sent
    
    # Store bot and message info for thread-safe updates
    bot = context.bot
    msg_id = msg.message_id
    
    def run_scraping():
        """Run scraping entirely in thread - no async blocking"""
        nonlocal searches, last_periodic_save, last_milestone_sent
        
        with ThreadPoolExecutor(max_workers=workers + max(1, proxyless_workers)) as executor:
            # Start proxy workers
            futures = [executor.submit(scraper_worker) for _ in range(workers)]
            # Start proxyless workers only if enabled
            if proxyless_workers > 0:
                proxyless_futures = [executor.submit(proxyless_scraper_worker) for _ in range(proxyless_workers)]
            else:
                proxyless_futures = []
            all_futures = futures + proxyless_futures
            
            last_update = time.time()
            
            while not all(future.done() for future in all_futures):
                if stop_flags.get(chat_id, False):
                    user_data[chat_id]['scraping'] = False
                    for future in all_futures:
                        if not future.done():
                            future.cancel()
                    break
                    
                time.sleep(0.5)  # Use time.sleep instead of await
                current = time.time()
                
                # Periodic save every 10 seconds
                if current - last_periodic_save >= periodic_save_interval:
                    with lock:
                        if found:
                            save_all_sites_to_file(list(found))
                    last_periodic_save = current
                
                # Auto-send sites every 1000 new sites (sync version)
                current_milestone = (len(found) // 1000) * 1000
                if current_milestone > last_milestone_sent and current_milestone > 0:
                    last_milestone_sent = current_milestone
                    try:
                        with lock:
                            save_all_sites_to_file(list(found))
                        
                        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False, encoding='utf-8') as f:
                            for site in sorted(found):
                                f.write(f"{site}\n")
                            temp_path = f.name
                        
                        # Use requests to send file via Telegram API directly (no async)
                        with open(temp_path, 'rb') as f:
                            requests.post(
                                f"https://api.telegram.org/bot{BOT_TOKEN}/sendDocument",
                                data={'chat_id': chat_id, 'caption': f"🎯 MILESTONE: {len(found):,} SITES!\n\n📤 Auto-sent at {current_milestone:,} sites\n⏱️ Time: {current - start_time:.0f}s"},
                                files={'document': (f"sites_{len(found)}.txt", f)},
                                timeout=30
                            )
                        os.unlink(temp_path)
                        print(f"📤 Auto-sent {len(found)} sites at {current_milestone} milestone")
                    except Exception as e:
                        print(f"⚠️ Auto-send failed: {e}")
                
                # Check stop flag again
                if stop_flags.get(chat_id, False):
                    user_data[chat_id]['scraping'] = False
                    for future in futures:
                        if not future.done():
                            future.cancel()
                    break
                
                # Update status message (sync version via direct API)
                if current - last_update >= 5:  # Update every 5 seconds
                    with lock:
                        elapsed = current - start_time
                        rate = len(found) / max(1, searches) if searches > 0 else 0
                        sites_per_min = (len(found) / max(1, elapsed)) * 60
                        
                        proxyless_status = f"{proxyless_workers}" if proxyless_workers > 0 else "DISABLED"
                        
                        try:
                            requests.post(
                                f"https://api.telegram.org/bot{BOT_TOKEN}/editMessageText",
                                json={
                                    'chat_id': chat_id,
                                    'message_id': msg_id,
                                    'text': f"🔥 PROXY SCRAPER V6.1\n\n🎯 FOUND: {len(found)} SITES\n📊 Searches: {searches:,}\n⚡ Hit rate: {rate:.3f} sites/search\n🚀 Speed: {sites_per_min:.1f} sites/min\n⏱️ Time: {elapsed:.0f}s\n👥 Proxy Workers: {workers}\n🌐 Proxyless: {proxyless_status}\n🔍 Engines: {len(SEARCH_ENGINES)}\n📄 Pages per search: 50\n🔑 Keywords: {len(DORKS)}\n\n💡 /stop for instant results!"
                                },
                                timeout=10
                            )
                            last_update = current
                        except Exception as e:
                            print(f"⚠️ Update message failed: {e}")
        
        # Final status
        try:
            elapsed = time.time() - start_time
            status = "STOPPED" if stop_flags.get(chat_id, False) else "COMPLETE"
            sites_per_min = (len(found) / max(1, elapsed)) * 60
            requests.post(
                f"https://api.telegram.org/bot{BOT_TOKEN}/editMessageText",
                json={
                    'chat_id': chat_id,
                    'message_id': msg_id,
                    'text': f"🎉 {status} - V6.0 TURBO\n\n🎯 {len(found)} SHOPIFY SITES\n📊 Searches: {searches:,}\n⚡ Success: {len(found)/max(1,searches):.3f}\n🚀 Speed: {sites_per_min:.1f}/min\n⏱️ Time: {elapsed:.1f}s\n🔑 {len(DORKS)} keywords\n\n📁 Use /send to get file!"
                },
                timeout=10
            )
        except:
            pass
        
        # Final save
        with lock:
            if found:
                save_all_sites_to_file(list(found))
        
        user_data[chat_id]['found_sites'] = found
        return found
    
    # Run entirely in a separate thread - completely non-blocking
    loop = asyncio.get_event_loop()
    found = await loop.run_in_executor(None, run_scraping)
    
    stop_flags[chat_id] = False
    return list(found)


async def scrape_sites_proxyless_only(chat_id, context):
    """Ultra-fast proxyless-only scraper using all available search engines"""
    found = set()
    searches = 0
    lock = threading.Lock()
    start_time = time.time()
    
    # Reset engine stats for fresh session
    reset_engine_stats()
    
    engine_stats = {e['name']: 0 for e in PROXYLESS_ENGINES}
    engine_stats['Tempest'] = 0  # Add Tempest to engine stats
    
    stop_flags[chat_id] = False
    
    msg = await context.bot.send_message(
        chat_id, 
        f"🚀 **PROXYLESS ULTRA SCRAPER V7.0 + TEMPEST**\n\n"
        f"🔍 Search Engines: {len(PROXYLESS_ENGINES)} + Tempest (Bing)\n"
        f"🎯 Keywords: {len(DORKS)}\n"
        f"👥 Workers: {len(PROXYLESS_ENGINES) * 2 + TEMPEST_WORKERS}\n"
        f"🔥 Tempest Workers: {TEMPEST_WORKERS} (iPad UA, JSON API)\n"
        f"🌍 Country Filter: {TEMPEST_COUNTRY or 'Worldwide'}\n"
        f"⚡ NO PROXIES NEEDED!\n\n"
        f"💪 Starting massive parallel search...",
        parse_mode='Markdown'
    )
    
    user_data[chat_id]['scraping'] = True
    user_data[chat_id]['found_sites'] = found
    
    def proxyless_engine_worker(engine, worker_id):
        """Worker dedicated to one search engine"""
        nonlocal searches
        worker_found = 0
        consecutive_failures = 0
        
        # Use curl_cffi session if available (MUCH better at bypassing detection)
        if CURL_CFFI_AVAILABLE:
            session = curl_requests.Session()
        else:
            session = requests.Session()
            session.verify = False
        
        # Each worker does many searches
        for search_num in range(2500):  # 2500 searches per engine worker for more coverage
            if stop_flags.get(chat_id, False):
                break
            if not user_data[chat_id].get('scraping', False):
                break
            
            if consecutive_failures >= 20:
                # This engine is rate limited, give up on this engine
                print(f"❌ [{engine['name']}] Too many failures ({consecutive_failures}), stopping worker")
                break
                
            try:
                query = random.choice(DORKS)
                
                headers = {
                    'User-Agent': random.choice(USER_AGENTS),
                    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
                    'Accept-Language': 'en-US,en;q=0.5',
                }
                
                # Add custom headers if engine has them
                if 'headers' in engine:
                    headers.update(engine['headers'])
                
                params = {engine['param']: query}
                
                # SearX-specific params
                if 'SearX' in engine['name'] or 'searx' in engine['url'].lower():
                    params['format'] = 'html'
                    params['categories'] = 'general'
                
                # DEBUG: Try request and log errors
                try:
                    if CURL_CFFI_AVAILABLE:
                        response = session.get(
                            engine['url'],
                            params=params,
                            headers=headers,
                            timeout=8,
                            allow_redirects=True,
                            impersonate=random.choice(BROWSER_IMPERSONATE)
                        )
                    else:
                        response = session.get(
                            engine['url'],
                            params=params,
                            headers=headers,
                            timeout=8,
                            allow_redirects=True
                        )
                except Exception as req_err:
                    print(f"🔴 [{engine['name']}] Request error: {type(req_err).__name__}: {str(req_err)[:100]}")
                    consecutive_failures += 1
                    time.sleep(0.3)
                    continue
                
                if 200 <= response.status_code < 400:
                    urls = extract_shopify_urls(response.text)
                    if urls:
                        consecutive_failures = 0
                        with lock:
                            searches += 1
                            engine_stats[engine['name']] += len(urls)
                            for url in urls:
                                if url not in found:
                                    found.add(url)
                                    worker_found += 1
                                    print(f"🌐 [{engine['name']}] [{len(found)}] {url}")
                                    save_site_to_file(url)
                    else:
                        consecutive_failures += 1
                        with lock:
                            searches += 1
                elif response.status_code in [429, 403, 503]:
                    # Rate limited - log it
                    print(f"🟡 [{engine['name']}] Rate limited: {response.status_code}")
                    consecutive_failures += 1
                    time.sleep(0.5)
                else:
                    print(f"🟠 [{engine['name']}] Bad status: {response.status_code}")
                    consecutive_failures += 1
                
                # Delays to avoid rate limiting
                if engine['name'] == 'Brave':
                    time.sleep(random.uniform(2.0, 4.0))  # Brave rate limits aggressively
                elif 'SearX' in engine['name']:
                    time.sleep(random.uniform(0.5, 1.0))
                else:
                    time.sleep(random.uniform(1.0, 2.0))
                
            except Exception as e:
                consecutive_failures += 1
                time.sleep(0.2)
                continue
        
        return worker_found
    
    def tempest_scraper_worker(worker_id):
        nonlocal searches
        worker_found = 0
        consecutive_failures = 0

        session = requests.Session()
        session.verify = False

        for _ in range(5000):
            if stop_flags.get(chat_id, False):
                break
            if not user_data[chat_id].get('scraping', False):
                break

            if consecutive_failures >= 30:
                time.sleep(2)
                consecutive_failures = 0

            try:
                query = random.choice(DORKS)
                offset = random.choice([0, 50, 100])

                params = {
                    "q": query,
                    "count": "50",
                    "offset": str(offset),
                }
                if TEMPEST_COUNTRY:
                    params["cc"] = TEMPEST_COUNTRY
                    params["mkt"] = f"en-{TEMPEST_COUNTRY}"
                headers = {
                    "User-Agent": IPAD_UA,
                    "Accept": "application/json",
                    "Accept-Language": "en-US,en;q=0.5",
                    "Accept-Encoding": "gzip, deflate, br",
                }

                r = session.get(
                    TEMPEST_V1_SEARCH,
                    params=params,
                    headers=headers,
                    timeout=30,
                    allow_redirects=True,
                )

                if r.status_code != 200 or not r.content:
                    consecutive_failures += 1
                    with lock:
                        searches += 1
                    time.sleep(0.2)
                    continue

                try:
                    data = r.json()
                except Exception:
                    consecutive_failures += 1
                    with lock:
                        searches += 1
                    continue

                web_pages = data.get("webPages", {})
                results = web_pages.get("value", [])

                page_urls = set()
                for result in results:
                    url = result.get("url", "")
                    snippet = result.get("snippet", "") + " " + result.get("name", "")
                    for text in [url, snippet]:
                        for su in extract_shopify_urls(text):
                            page_urls.add(su)

                raw_found = extract_shopify_urls(r.text)
                page_urls.update(raw_found)

                if page_urls:
                    consecutive_failures = 0
                    with lock:
                        searches += 1
                        engine_stats['Tempest'] = engine_stats.get('Tempest', 0) + len(page_urls)
                        for url in page_urls:
                            if url not in found:
                                found.add(url)
                                worker_found += 1
                                print(f"🌐 [Tempest] [{len(found)}] {url}")
                                save_site_to_file(url)
                else:
                    consecutive_failures += 1
                    with lock:
                        searches += 1

                time.sleep(random.uniform(0.1, 0.4))

            except Exception:
                consecutive_failures += 1
                time.sleep(0.2)
                continue

        return worker_found

    workers_per_engine = 2
    total_workers = len(PROXYLESS_ENGINES) * workers_per_engine + TEMPEST_WORKERS
    last_milestone_sent = 0
    msg_id = msg.message_id
    
    def run_proxyless_scraping():
        """Run proxyless scraping entirely in thread - no async blocking"""
        nonlocal searches, last_milestone_sent
        last_update = time.time()
        last_periodic_save = time.time()
        
        with ThreadPoolExecutor(max_workers=total_workers) as executor:
            futures = []
            for engine in PROXYLESS_ENGINES:
                for worker_id in range(workers_per_engine):
                    futures.append(executor.submit(proxyless_engine_worker, engine, worker_id))
            for t_id in range(TEMPEST_WORKERS):
                futures.append(executor.submit(tempest_scraper_worker, t_id))
            
            while not all(future.done() for future in futures):
                if stop_flags.get(chat_id, False):
                    user_data[chat_id]['scraping'] = False
                    break
                    
                time.sleep(0.5)  # Use time.sleep instead of await
                current = time.time()
                
                # Periodic save every 10 seconds
                if current - last_periodic_save >= 10:
                    with lock:
                        if found:
                            save_all_sites_to_file(list(found))
                    last_periodic_save = current
                
                # Auto-send sites every 1000 new sites (sync version)
                current_milestone = (len(found) // 1000) * 1000
                if current_milestone > last_milestone_sent and current_milestone > 0:
                    last_milestone_sent = current_milestone
                    try:
                        with lock:
                            save_all_sites_to_file(list(found))
                        
                        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False, encoding='utf-8') as f:
                            for site in sorted(found):
                                f.write(f"{site}\n")
                            temp_path = f.name
                        
                        with open(temp_path, 'rb') as f:
                            requests.post(
                                f"https://api.telegram.org/bot{BOT_TOKEN}/sendDocument",
                                data={'chat_id': chat_id, 'caption': f"🎯 MILESTONE: {len(found):,} SITES!\n\n📤 Auto-sent at {current_milestone:,} sites\n⏱️ Time: {current - start_time:.0f}s"},
                                files={'document': (f"sites_{len(found)}.txt", f)},
                                timeout=30
                            )
                        os.unlink(temp_path)
                        print(f"📤 Auto-sent {len(found)} sites at {current_milestone} milestone")
                    except Exception as e:
                        print(f"⚠️ Auto-send failed: {e}")
                
                # Update status message (sync version via direct API)
                if current - last_update >= 5:
                    with lock:
                        elapsed = current - start_time
                        rate = len(found) / max(1, searches) if searches > 0 else 0
                        sites_per_min = (len(found) / max(1, elapsed)) * 60
                        
                        top_engines = sorted(engine_stats.items(), key=lambda x: x[1], reverse=True)[:5]
                        top_str = '\n'.join([f"  • {name}: {count}" for name, count in top_engines if count > 0])
                        
                        try:
                            requests.post(
                                f"https://api.telegram.org/bot{BOT_TOKEN}/editMessageText",
                                json={
                                    'chat_id': chat_id,
                                    'message_id': msg_id,
                                    'text': f"🔥 PROXYLESS ULTRA V6.0\n\n🎯 FOUND: {len(found)} SITES\n📊 Searches: {searches:,}\n⚡ Hit rate: {rate:.3f}\n🚀 Speed: {sites_per_min:.1f} sites/min\n⏱️ Time: {elapsed:.0f}s\n🌐 Engines: {len(PROXYLESS_ENGINES)}\n👥 Workers: {total_workers}\n\n🏆 TOP ENGINES:\n{top_str if top_str else '  Starting...'}\n\n💡 /stop for instant results!"
                                },
                                timeout=10
                            )
                            last_update = current
                        except Exception as e:
                            print(f"⚠️ Update message failed: {e}")
        
        # Final status
        try:
            elapsed = time.time() - start_time
            status = "STOPPED" if stop_flags.get(chat_id, False) else "COMPLETE"
            sites_per_min = (len(found) / max(1, elapsed)) * 60
            
            top_engines = sorted(engine_stats.items(), key=lambda x: x[1], reverse=True)[:5]
            top_str = '\n'.join([f"  • {name}: {count}" for name, count in top_engines if count > 0])
            
            requests.post(
                f"https://api.telegram.org/bot{BOT_TOKEN}/editMessageText",
                json={
                    'chat_id': chat_id,
                    'message_id': msg_id,
                    'text': f"🎉 {status} - PROXYLESS V6.0\n\n🎯 {len(found)} SHOPIFY SITES\n📊 Searches: {searches:,}\n⚡ Rate: {len(found)/max(1,searches):.3f}\n🚀 Speed: {sites_per_min:.1f}/min\n⏱️ Time: {elapsed:.1f}s\n\n🏆 TOP ENGINES:\n{top_str}\n\n📁 Use /send to get file!"
                },
                timeout=10
            )
        except Exception as e:
            print(f"⚠️ Final status message failed: {e}")
        
        # Final save
        with lock:
            if found:
                save_all_sites_to_file(list(found))
        
        user_data[chat_id]['found_sites'] = found
        stop_flags[chat_id] = False
        return list(found)
    
    # Run entirely in a separate thread - completely non-blocking
    loop = asyncio.get_event_loop()
    result = await loop.run_in_executor(None, run_proxyless_scraping)
    return result


async def toggle_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    """Toggle proxyless mode on/off for dual mode scraping"""
    global USE_PROXYLESS_IN_DUAL_MODE
    
    USE_PROXYLESS_IN_DUAL_MODE = not USE_PROXYLESS_IN_DUAL_MODE
    
    if USE_PROXYLESS_IN_DUAL_MODE:
        status = "✅ **ENABLED**"
        msg = "Proxyless workers will run alongside proxy workers in /done mode."
    else:
        status = "❌ **DISABLED**"
        msg = "Only proxy workers will run in /done mode.\nUse this when your IP is blocked!"
    
    await update.message.reply_text(
        f"🔄 **PROXYLESS MODE TOGGLED**\n\n"
        f"Status: {status}\n\n"
        f"{msg}\n\n"
        f"💡 Use /toggle again to switch.",
        parse_mode='Markdown'
    )


async def proxyless_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    """Start proxyless-only scraping - no proxies needed!"""
    chat_id = update.effective_chat.id
    
    await update.message.reply_text(
        f"🚀 **PROXYLESS MODE ACTIVATED + TEMPEST**\n\n"
        f"🔍 Search Engines: {len(PROXYLESS_ENGINES)} + Tempest (Bing)\n"
        f"• 🔥 Tempest API ({TEMPEST_WORKERS} workers, iPad UA)\n"
        f"• Brave (excellent for Shopify)\n"
        f"• 10+ SearX instances\n"
        f"• Yahoo, Ecosia, Yandex\n"
        f"• Metager, Swisscows, Dogpile\n\n"
        f"🎯 Keywords: {len(DORKS)}+\n"
        f"⚡ NO PROXIES REQUIRED!\n\n"
        f"💪 Starting ultra scraper...",
        parse_mode='Markdown'
    )
    
    user_data[chat_id] = {'proxy_files': [], 'all_proxies': set()}
    stop_flags[chat_id] = False
    
    # Run scraping as background task so bot stays responsive
    async def background_scrape_proxyless():
        try:
            sites = await scrape_sites_proxyless_only(chat_id, context)
            if not stop_flags.get(chat_id, False):
                try:
                    await send_results(chat_id, context, sites)
                except Exception as e:
                    print(f"❌ Error sending results: {e}")
                    try:
                        await context.bot.send_message(
                            chat_id,
                            f"⚠️ Error sending file, but sites are saved!\n\n"
                            f"🎯 Found: {len(sites)} sites\n"
                            f"📁 Check sites.txt file"
                        )
                    except:
                        pass
        except Exception as e:
            print(f"❌ Background proxyless scrape error: {e}")
            import traceback
            traceback.print_exc()
    
    # Start as background task - returns immediately so bot stays responsive
    asyncio.create_task(background_scrape_proxyless())


async def send_results(chat_id, context, sites):
    if sites:
        sorted_sites = sorted(list(set(sites)))
        
        # Ensure all sites are saved to file before sending
        save_all_sites_to_file(sorted_sites)
        
        # Use absolute path for better Linux compatibility
        # Try multiple locations for better compatibility
        filename = None
        temp_locations = [
            os.path.join(tempfile.gettempdir(), f"shopify_v40_{len(sorted_sites)}_sites_{int(time.time())}.txt"),
            os.path.abspath(f"shopify_v40_{len(sorted_sites)}_sites_{int(time.time())}.txt"),
            os.path.join(os.getcwd(), f"shopify_v40_{len(sorted_sites)}_sites_{int(time.time())}.txt"),
        ]
        
        for temp_file in temp_locations:
            try:
                # Write file with explicit encoding and flush
                with open(temp_file, 'w', encoding='utf-8', errors='replace') as f:
                    for site in sorted_sites:
                        f.write(f"{site}\n")
                    f.flush()
                    os.fsync(f.fileno())  # Force write to disk (important for Linux)
                filename = temp_file
                break
            except Exception as e:
                print(f"⚠️ Could not write to {temp_file}: {e}")
                continue
        
        # Last resort: use sites.txt if it exists and other attempts failed
        if not filename and os.path.exists(_sites_file_path):
            filename = os.path.abspath(_sites_file_path)
            print(f"📁 Using sites.txt as fallback: {filename}")
        
        # Send file first to ensure it's always sent
        file_sent = False
        if filename and os.path.exists(filename):
            try:
                with open(filename, 'rb') as f:
                    await context.bot.send_document(
                        chat_id, f,
                        filename=f"shopify_v40_{len(sorted_sites)}_sites.txt",
                        caption=f"🎯 {len(sorted_sites)} sites\n🔍 {len(SEARCH_ENGINES)} engines\n🚀 V4.0"
                    )
                file_sent = True
            except Exception as e:
                print(f"❌ File send error: {e}")
                import traceback
                traceback.print_exc()
                # Try to send file again if first attempt failed
                try:
                    await asyncio.sleep(1)  # Brief delay before retry
                    with open(filename, 'rb') as f:
                        await context.bot.send_document(
                            chat_id, f,
                            filename=f"shopify_v40_{len(sorted_sites)}_sites.txt",
                            caption=f"🎯 {len(sorted_sites)} sites"
                        )
                    file_sent = True
                except Exception as e2:
                    print(f"❌ File send error (retry): {e2}")
                    import traceback
                    traceback.print_exc()
            
            # Clean up temp file (but keep sites.txt)
            if filename != os.path.abspath(_sites_file_path):
                try:
                    if os.path.exists(filename):
                        os.unlink(filename)
                except Exception as e:
                    print(f"⚠️ Could not delete temp file: {e}")
        
        # Send sample message (use HTML to avoid Markdown parsing issues with URLs)
        sample = sorted_sites[:25]
        sample_text = f"🎯 <b>{len(sorted_sites)} UNIQUE SITES (V4.0):</b>\n\n"
        
        for i, site in enumerate(sample, 1):
            # Escape HTML special characters in URLs
            escaped_site = site.replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;')
            sample_text += f"{i}. {escaped_site}\n"
            
        if len(sorted_sites) > 25:
            sample_text += f"\n📁 +{len(sorted_sites)-25} more in file"
        
        sample_text += f"\n\n🏆 <b>MULTI-ENGINE SUCCESS!</b> 🏆"
        
        try:
            await context.bot.send_message(chat_id, sample_text, parse_mode='HTML')
        except Exception as e:
            # Fallback to plain text if HTML also fails
            print(f"❌ Message error: {e}")
            try:
                sample_text_plain = f"🎯 {len(sorted_sites)} UNIQUE SITES (V4.0):\n\n"
                for i, site in enumerate(sample, 1):
                    sample_text_plain += f"{i}. {site}\n"
                if len(sorted_sites) > 25:
                    sample_text_plain += f"\n📁 +{len(sorted_sites)-25} more in file"
                sample_text_plain += f"\n\n🏆 MULTI-ENGINE SUCCESS! 🏆"
                await context.bot.send_message(chat_id, sample_text_plain)
            except Exception as e2:
                print(f"❌ Message error (retry): {e2}")
        
        # Clean up file if it wasn't deleted
        if os.path.exists(filename):
            try:
                os.unlink(filename)
            except:
                pass
    else:
        await context.bot.send_message(
            chat_id, 
            "❌ **NO SITES FOUND**\n\n💡 Reasons:\n• Proxies don't work with search engines\n• Try different proxy source\n• Need higher quality proxies\n\n📁 Send better proxies to try again"
        )

async def start_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    chat_id = update.effective_chat.id
    proxyless_status = "✅ ON" if USE_PROXYLESS_IN_DUAL_MODE else "❌ OFF"
    await update.message.reply_text(
        f"🛍️ **SHOPIFY FINDER V6.2**\n\n"
        f"🚀 **COMMANDS:**\n"
        f"• `/proxyless` - NO proxies needed!\n"
        f"• `/done` - With proxies\n"
        f"• `/toggle` - Turn proxyless ON/OFF\n"
        f"• `/stop` - Stop & get results\n\n"
        f"⚡ **PROXYLESS IN DUAL MODE:** {proxyless_status}\n"
        f"(Use /toggle if your IP is blocked)\n\n"
        f"📁 **WITH PROXY MODE:**\n"
        f"1. Send proxy file(s) (.txt)\n"
        f"2. Select type: HTTP / SOCKS4 / SOCKS5\n"
        f"3. Type /done\n\n"
        f"🧦 **SOCKS SUPPORT:** HTTP, SOCKS4, SOCKS5\n"
        f"🔍 **ENGINES:** {len(SEARCH_ENGINES)} proxy + {len(PROXYLESS_ENGINES)} proxyless",
        parse_mode='Markdown'
    )
    user_data[chat_id] = {'proxy_files': [], 'all_proxies': set(), 'pending_file': None}
    stop_flags[chat_id] = False

async def done_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    chat_id = update.effective_chat.id
    
    if chat_id not in user_data or not user_data[chat_id].get('all_proxies'):
        await update.message.reply_text(
            "❌ **NO PROXIES LOADED**\n\n📁 Send proxy file(s) first, then use /done"
        )
        return
    
    all_proxies = list(user_data[chat_id]['all_proxies'])
    file_count = len(user_data[chat_id].get('proxy_files', []))
    
    await update.message.reply_text(
        f"✅ **BATCH PROCESSING STARTED**\n\n📁 Files received: {file_count}\n🌐 Total unique proxies: {len(all_proxies):,}\n🧪 Starting strict validation...",
        parse_mode='Markdown'
    )
    
    working = await test_proxies(chat_id, context, all_proxies)
    
    if not working:
        await context.bot.send_message(
            chat_id,
            "❌ **NO WORKING PROXIES**\n\n💡 All proxies failed strict testing\n🔍 Proxies must work with search engines\n📁 Try different proxy source"
        )
        user_data[chat_id] = {'proxy_files': [], 'all_proxies': set()}
        return
    
    try:
        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False, encoding='utf-8', errors='replace') as f:
            for proxy in working:
                f.write(f"{proxy.replace('http://', '')}\n")
            filename = f.name
        
        with open(filename, 'rb') as f:
            await context.bot.send_document(
                chat_id, f,
                filename=f"working_proxies_v40_{len(working)}.txt",
                caption=f"📁 {len(working)} validated proxies"
            )
        os.unlink(filename)
    except:
        pass
    
    await context.bot.send_message(
        chat_id,
        f"🚀 **LAUNCHING V6.0 ULTRA SCRAPER**\n\n"
        f"🔧 Working proxies: {len(working)}\n"
        f"🔍 Proxy engines: {len(SEARCH_ENGINES)}\n"
        f"🌐 Proxyless engines: {len(PROXYLESS_ENGINES)}\n"
        f"🎯 Keywords: {len(DORKS)}\n"
        f"👥 Proxy workers: {min(MAX_SCRAPE_WORKERS, len(working))}\n"
        f"👥 Proxyless workers: {len(PROXYLESS_ENGINES) * 3}\n\n"
        f"⚡ DUAL MODE: PROXY + PROXYLESS\n"
        f"💡 /stop for instant results!"
    )
    
    # Run scraping as background task so bot stays responsive
    async def background_scrape():
        try:
            sites = await scrape_sites(chat_id, context, working)
            if not stop_flags.get(chat_id, False):
                try:
                    await send_results(chat_id, context, sites)
                except Exception as e:
                    print(f"❌ Error sending results: {e}")
                    import traceback
                    traceback.print_exc()
                    try:
                        await context.bot.send_message(
                            chat_id,
                            f"⚠️ Error sending results, but sites are saved to sites.txt\n\n🎯 Found: {len(sites)} sites\n📁 Check sites.txt file"
                        )
                    except:
                        pass
        except Exception as e:
            print(f"❌ Background scrape error: {e}")
            import traceback
            traceback.print_exc()
        finally:
            user_data[chat_id] = {'proxy_files': [], 'all_proxies': set()}
    
    # Start as background task - returns immediately so bot stays responsive
    asyncio.create_task(background_scrape())

async def stop_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    chat_id = update.effective_chat.id
    
    stop_flags[chat_id] = True
    if chat_id in user_data:
        user_data[chat_id]['scraping'] = False
    
    await update.message.reply_text(
        "🛑 **EMERGENCY STOP - V4.0**\n\n⚡ Stopping workers...\n⏳ Collecting results...",
        parse_mode='Markdown'
    )
    
    await asyncio.sleep(2)
    
    sites = []
    if chat_id in user_data and 'found_sites' in user_data[chat_id]:
        sites = list(user_data[chat_id]['found_sites'])
    
    if sites:
        try:
            await send_results(chat_id, context, sites)
            await update.message.reply_text(
                f"✅ **STOPPED - V4.0**\n\n🎯 {len(sites)} sites delivered\n📁 Send new proxies to restart",
                parse_mode='Markdown'
            )
        except Exception as e:
            print(f"❌ Error sending results: {e}")
            import traceback
            traceback.print_exc()
            try:
                await update.message.reply_text(
                    f"⚠️ **STOPPED - V4.0**\n\n🎯 {len(sites)} sites found\n📁 Sites saved to sites.txt\n\n⚠️ Error sending file: {str(e)[:200]}",
                    parse_mode='Markdown'
                )
            except:
                pass
    else:
        await update.message.reply_text(
            "🛑 **STOPPED - V4.0**\n\n⚠️ No sites yet\n📁 Send proxies to restart"
        )
    
    if chat_id in user_data and 'scraping' in user_data[chat_id]:
        user_data[chat_id]['scraping'] = False

async def send_cmd(update: Update, context: ContextTypes.DEFAULT_TYPE):
    """Send current sites.txt file - works anytime, even during active scraping"""
    chat_id = update.effective_chat.id
    
    # Check if sites.txt exists
    sites_file = os.path.abspath(_sites_file_path)
    
    # Also check in-memory sites
    memory_sites = len(_saved_sites)
    
    if not os.path.exists(sites_file) and memory_sites == 0:
        await update.message.reply_text(
            "❌ **NO SITES FOUND**\n\n"
            "📁 sites.txt does not exist\n"
            "📝 No sites in memory\n"
            "💡 Run the scraper first to find sites",
            parse_mode='Markdown'
        )
        return
    
    try:
        # Collect all sites from file and memory
        all_sites = set()
        
        # Read from file if exists
        if os.path.exists(sites_file):
            with open(sites_file, 'r', encoding='utf-8', errors='replace') as f:
                for line in f:
                    site = line.strip()
                    if site:
                        all_sites.add(site)
        
        # Add any sites from memory that might not be in file yet
        all_sites.update(_saved_sites)
        
        if not all_sites:
            await update.message.reply_text(
                "⚠️ **NO SITES YET**\n\n"
                "📁 File exists but is empty\n"
                "💡 Scraping may still be in progress...",
                parse_mode='Markdown'
            )
            return
        
        site_count = len(all_sites)
        
        # Create temp file with all sites
        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False, encoding='utf-8') as f:
            for site in sorted(all_sites):
                f.write(f"{site}\n")
            temp_path = f.name
        
        await update.message.reply_text(
            f"📤 **SENDING SITES**\n\n"
            f"🎯 Sites found: {site_count:,}\n"
            f"⏳ Uploading...",
            parse_mode='Markdown'
        )
        
        # Send the file
        with open(temp_path, 'rb') as f:
            await context.bot.send_document(
                chat_id,
                f,
                filename=f"shopify_sites_{site_count}.txt",
                caption=f"🎯 **{site_count:,} SHOPIFY SITES**\n\n"
                        f"🔍 Engines: {len(SEARCH_ENGINES)} proxy + {len(PROXYLESS_ENGINES)} proxyless\n"
                        f"🚀 Shopify Finder V6.2"
            )
        
        # Cleanup temp file
        try:
            os.unlink(temp_path)
        except:
            pass
        
        await context.bot.send_message(
            chat_id,
            f"✅ **SENT SUCCESSFULLY**\n\n🎯 {site_count:,} sites",
            parse_mode='Markdown'
        )
        
    except Exception as e:
        await update.message.reply_text(
            f"❌ **ERROR**\n\n{str(e)[:200]}",
            parse_mode='Markdown'
        )

async def handle_document(update: Update, context: ContextTypes.DEFAULT_TYPE):
    chat_id = update.effective_chat.id
    
    doc = update.message.document
    if not doc.file_name.lower().endswith('.txt'):
        await update.message.reply_text("❌ Send .txt files only!")
        return
    
    if chat_id not in user_data:
        user_data[chat_id] = {'proxy_files': [], 'all_proxies': set(), 'pending_file': None}
    
    # Download and store file content temporarily
    try:
        file = await context.bot.get_file(doc.file_id)
        content = (await file.download_as_bytearray()).decode('utf-8', errors='ignore')
        
        # Count valid proxy lines
        proxy_lines = [line.strip() for line in content.splitlines() if line.strip() and not line.strip().startswith('#')]
        
        if not proxy_lines:
            await update.message.reply_text("❌ No valid proxy lines found in file!")
            return
        
        # Store pending file info
        user_data[chat_id]['pending_file'] = {
            'name': doc.file_name,
            'content': content,
            'count': len(proxy_lines)
        }
        
        # Ask for proxy type
        keyboard = [
            [
                InlineKeyboardButton("🌐 HTTP", callback_data="proxy_type:http"),
                InlineKeyboardButton("🧦 SOCKS4", callback_data="proxy_type:socks4"),
            ],
            [
                InlineKeyboardButton("🧦 SOCKS5", callback_data="proxy_type:socks5"),
                InlineKeyboardButton("🌀 MIX", callback_data="proxy_type:mix")
            ]
        ]
        reply_markup = InlineKeyboardMarkup(keyboard)
        
        # Escape underscores for Markdown
        safe_filename = doc.file_name.replace('_', '\\_')
        
        await update.message.reply_text(
            f"📁 **FILE RECEIVED**\n\n"
            f"📄 File: {safe_filename}\n"
            f"📊 Proxies found: {len(proxy_lines):,}\n\n"
            f"🔧 **What type of proxies are these?**\n"
            f"(Use MIX if file has socks4://, socks5://, http:// prefixes)",
            reply_markup=reply_markup,
            parse_mode='Markdown'
        )
        
    except Exception as e:
        await update.message.reply_text(f"❌ Error: {str(e)[:100]}")


async def handle_proxy_type_callback(update: Update, context: ContextTypes.DEFAULT_TYPE):
    """Handle proxy type selection from inline keyboard"""
    query = update.callback_query
    await query.answer()
    
    chat_id = query.message.chat_id
    
    if not query.data.startswith("proxy_type:"):
        return
    
    proxy_type = query.data.split(":")[1]  # http, socks4, or socks5
    
    if chat_id not in user_data or not user_data[chat_id].get('pending_file'):
        await query.edit_message_text("❌ No pending file. Please send a proxy file first.")
        return
    
    pending = user_data[chat_id]['pending_file']
    content = pending['content']
    filename = pending['name']
    
    # Escape underscores for Markdown
    safe_filename = filename.replace('_', '\\_')
    
    # Parse proxies with selected type
    valid = []
    for line in content.splitlines():
        proxy = parse_proxy(line, proxy_type)
        if proxy and proxy not in user_data[chat_id]['all_proxies']:
            valid.append(proxy)
            user_data[chat_id]['all_proxies'].add(proxy)
    
    # Clear pending file
    user_data[chat_id]['pending_file'] = None
    
    if not valid:
        await query.edit_message_text(
            f"⚠️ **NO NEW PROXIES**\n\n"
            f"All proxies from {safe_filename} were duplicates or invalid\n\n"
            f"📊 Total unique: {len(user_data[chat_id]['all_proxies']):,}",
            parse_mode='Markdown'
        )
        return
    
    user_data[chat_id]['proxy_files'].append(filename)
    
    file_count = len(user_data[chat_id]['proxy_files'])
    total_unique = len(user_data[chat_id]['all_proxies'])
    
    # Set emoji based on type
    if proxy_type == "http":
        type_emoji = "🌐"
    elif proxy_type == "mix":
        type_emoji = "🌀"
    else:
        type_emoji = "🧦"
    type_name = proxy_type.upper()
    
    await query.edit_message_text(
        f"✅ **{type_name} PROXIES LOADED**\n\n"
        f"📁 File: {safe_filename}\n"
        f"{type_emoji} Type: {type_name}\n"
        f"➕ New proxies: {len(valid):,}\n"
        f"📊 Total unique: {total_unique:,}\n"
        f"📂 Files loaded: {file_count}\n\n"
        f"💡 Send more files or type /done to start!",
        parse_mode='Markdown'
    )

async def handle_message(update: Update, context: ContextTypes.DEFAULT_TYPE):
    await update.message.reply_text(
        "📁 **SEND PROXY FILES**\n\n1. Send one or more .txt files\n2. Type /done when ready\n3. Use /start for info"
    )

def load_proxies_from_file(filepath):
    proxies = set()
    if not os.path.exists(filepath):
        print(f"❌ File not found: {filepath}")
        return proxies
    
    print(f"📥 Loading proxies from: {filepath}")
    try:
        with open(filepath, 'r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                proxy = parse_proxy(line.strip())
                if proxy:
                    proxies.add(proxy)
        print(f"✅ Loaded {len(proxies):,} unique proxies")
    except Exception as e:
        print(f"❌ Error reading file: {e}")
    return proxies

def console_test_proxies(proxy_list):
    working = []
    tested = 0
    total = len(proxy_list)
    lock = threading.Lock()
    
    print(f"\n🧪 **TESTING {total:,} PROXIES**")
    print(f"✅ Testing with REAL search queries")
    print(f"⚡ Only proxies that work with search engines will pass\n")
    
    def worker():
        nonlocal tested
        while True:
            try:
                proxy = q.get_nowait()
                if test_proxy_with_search(proxy):
                    with lock:
                        working.append(proxy)
                        print(f"✅ [{len(working)}/{tested}] Working proxy found")
                with lock:
                    tested += 1
                    if tested % 10 == 0:
                        print(f"📊 Progress: {tested:,}/{total:,} ({tested/total*100:.1f}%) | Working: {len(working)} ({len(working)/tested*100:.1f}%)")
                q.task_done()
            except queue.Empty:
                break
            except:
                with lock:
                    tested += 1
                q.task_done()
    
    q = queue.Queue()
    for proxy in proxy_list:
        q.put(proxy)
    
    with ThreadPoolExecutor(max_workers=min(MAX_PROXY_WORKERS, total)) as executor:
        futures = [executor.submit(worker) for _ in range(min(MAX_PROXY_WORKERS, total))]
        
        while not all(f.done() for f in futures):
            time.sleep(2)
    
    print(f"\n✅ **TESTING COMPLETE**")
    print(f"🎯 Found: {len(working)}/{total} working proxies")
    print(f"📊 Success rate: {len(working)/total*100:.1f}%")
    print(f"🚀 Ready for scraping!\n")
    
    return working

def console_scrape_sites(proxies):
    found = set()
    searches = 0
    failed_proxies = set()
    lock = threading.Lock()
    start_time = time.time()
    stop_flag = threading.Event()
    
    print(f"🔍 **MULTI-ENGINE SCRAPER V4.0**\n")
    print(f"🎯 Keywords: {len(DORKS):,}")
    print(f"🌐 Proxies: {len(proxies):,}")
    print(f"🔍 Search Engines: {len(SEARCH_ENGINES)}")
    print(f"⚡ Workers: {min(MAX_SCRAPE_WORKERS, len(proxies))}")
    print(f"📄 Pages per search: 10 (for maximum coverage)")
    print(f"💪 Starting...\n")
    print("💡 Press Ctrl+C to stop and get results\n")
    
    proxy_pool = proxies.copy()
    random.shuffle(proxy_pool)
    proxy_index = 0
    
    def get_next_proxy():
        nonlocal proxy_index
        if not proxy_pool:
            return None
        
        attempts = 0
        while attempts < len(proxy_pool):
            proxy = proxy_pool[proxy_index % len(proxy_pool)]
            proxy_index += 1
            
            if proxy not in failed_proxies:
                return proxy
            attempts += 1
        return None
    
    def scraper_worker():
        nonlocal searches
        worker_found = 0
        consecutive_failures = 0
        
        for search_count in range(10000):
            if stop_flag.is_set():
                break
            
            if consecutive_failures >= 500:
                consecutive_failures = 0  # Reset instead of stopping
                time.sleep(0.5)
                
            try:
                query = random.choice(DORKS)
                proxy = get_next_proxy()
                
                if not proxy:
                    # Reset failed proxies and try again
                    failed_proxies.clear()
                    proxy = get_next_proxy()
                    if not proxy:
                        break
                
                if stop_flag.is_set():
                    break
                    
                urls = search_with_proxy(query, proxy, chat_id)
                
                if stop_flag.is_set():
                    break
                
                if urls:
                    consecutive_failures = 0
                    with lock:
                        searches += 1
                        for url in urls:
                            if url not in found:
                                found.add(url)
                                worker_found += 1
                                print(f"✅ [{len(found)}] {url}")
                                # Save site immediately to file
                                save_site_to_file(url)
                else:
                    consecutive_failures += 1
                    with lock:
                        searches += 1
                        # DON'T mark proxy as failed
                
                time.sleep(random.uniform(0.005, 0.02))
                
            except Exception as e:
                consecutive_failures += 1
                with lock:
                    searches += 1
                time.sleep(0.01)
                continue
        
        return worker_found
    
    workers = min(MAX_SCRAPE_WORKERS, len(proxies))
    futures = []
    
    # Start periodic saving task (every 10 seconds)
    last_periodic_save = time.time()
    periodic_save_interval = 10.0
    
    try:
        with ThreadPoolExecutor(max_workers=workers) as executor:
            futures = [executor.submit(scraper_worker) for _ in range(workers)]
            
            last_update = time.time()
            
            while not all(future.done() for future in futures):
                time.sleep(2)
                current = time.time()
                
                # Periodic save every 10 seconds
                if current - last_periodic_save >= periodic_save_interval:
                    with lock:
                        if found:
                            save_all_sites_to_file(list(found))
                    last_periodic_save = current
                
                if current - last_update >= 2:
                    with lock:
                        elapsed = current - start_time
                        rate = len(found) / max(1, searches) if searches > 0 else 0
                        sites_per_min = (len(found) / max(1, elapsed)) * 60
                        active_proxies = len(proxy_pool) - len(failed_proxies)
                        
                        print(f"\r🔥 FOUND: {len(found):,} SITES | Searches: {searches:,} | Hit rate: {rate:.3f} | Speed: {sites_per_min:.1f}/min | Time: {elapsed:.0f}s | Active proxies: {active_proxies}/{len(proxy_pool)}", end='', flush=True)
                        last_update = current
    except KeyboardInterrupt:
        print("\n\n🛑 Stopping workers...")
        stop_flag.set()
        if futures:
            for future in futures:
                if not future.done():
                    future.cancel()
    
    # Final save of all sites before returning
    with lock:
        if found:
            save_all_sites_to_file(list(found))
    
    elapsed = time.time() - start_time
    sites_per_min = (len(found) / max(1, elapsed)) * 60
    
    print(f"\n\n🎉 **SCRAPING COMPLETE**\n")
    print(f"🎯 **{len(found):,} SHOPIFY SITES**")
    print(f"📊 Searches: {searches:,}")
    print(f"⚡ Success: {len(found)/max(1,searches):.3f}")
    print(f"🚀 Speed: {sites_per_min:.1f}/min")
    print(f"📁 Sites saved to: {os.path.abspath(_sites_file_path)}")
    print(f"⏱️  Time: {elapsed:.1f}s")
    print(f"🔑 {len(DORKS):,} keywords\n")
    
    return list(found)

def save_results_to_file(sites, output_file=None):
    if not sites:
        print("❌ No sites to save")
        return
    
    if not output_file:
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        output_file = f"shopify_sites_{len(sites)}_{timestamp}.txt"
    
    sorted_sites = sorted(list(set(sites)))
    
    try:
        with open(output_file, 'w', encoding='utf-8') as f:
            for site in sorted_sites:
                f.write(f"{site}\n")
        
        print(f"✅ Results saved to: {output_file}")
        print(f"📁 {len(sorted_sites):,} unique sites")
        return output_file
    except Exception as e:
        print(f"❌ Error saving file: {e}")
        return None

def console_mode(proxy_file, output_file=None, skip_test=False):
    print("=" * 80)
    print("🚀 Shopify Multi-Engine Scraper V4.0 - CONSOLE MODE")
    print("=" * 80)
    print(f"🔍 Search Engines: {len(SEARCH_ENGINES)}")
    for engine in SEARCH_ENGINES:
        print(f"  • {engine['name']} ({engine['weight']*100:.0f}%)")
    print(f"🔑 Keywords: {len(DORKS):,}")
    print(f"✅ Strict proxy validation: ENABLED")
    print(f"🔄 Batch processing: ENABLED")
    print(f"🚫 Auto-deduplication: ENABLED")
    print(f"📄 Pages per search: 10 (for maximum coverage)")
    print("=" * 80)
    print()
    
    all_proxies = load_proxies_from_file(proxy_file)
    if not all_proxies:
        print("❌ No valid proxies found. Exiting.")
        return
    
    if not skip_test:
        working = console_test_proxies(all_proxies)
    else:
        print("⚠️  Skipping proxy testing (--skip-test)")
        working = list(all_proxies)
    
    if not working:
        print("❌ No working proxies found. Exiting.")
        return
    
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    working_proxy_file = f"working_proxies_{len(working)}_{timestamp}.txt"
    with open(working_proxy_file, 'w', encoding='utf-8') as f:
        for proxy in working:
            f.write(f"{proxy.replace('http://', '')}\n")
    print(f"💾 Working proxies saved to: {working_proxy_file}\n")
    
    sites = console_scrape_sites(working)
    
    if sites:
        save_results_to_file(sites, output_file)
    else:
        print("❌ No sites found")

async def main_async():
    if not TELEGRAM_AVAILABLE:
        print("❌ Error: Telegram library not available")
        return
    
    print("=" * 80)
    print("🚀 Shopify Multi-Engine Scraper V4.0")
    print("=" * 80)
    print(f"🔍 Search Engines: {len(SEARCH_ENGINES)}")
    for engine in SEARCH_ENGINES:
        print(f"  • {engine['name']} ({engine['weight']*100:.0f}%)")
    print(f"🔑 Keywords: {len(DORKS)}")
    print(f"✅ Strict proxy validation: ENABLED")
    print(f"🔄 Batch processing: ENABLED")
    print(f"🚫 Auto-deduplication: ENABLED")
    print("=" * 80)
    
    # Enable concurrent updates so /send works during scraping
    app = Application.builder().token(BOT_TOKEN).concurrent_updates(True).build()
    
    app.add_handler(CommandHandler("start", start_cmd))
    app.add_handler(CommandHandler("done", done_cmd))
    app.add_handler(CommandHandler("stop", stop_cmd))
    app.add_handler(CommandHandler("send", send_cmd))
    app.add_handler(CommandHandler("proxyless", proxyless_cmd))
    app.add_handler(CommandHandler("toggle", toggle_cmd))  # Toggle proxyless on/off
    app.add_handler(CallbackQueryHandler(handle_proxy_type_callback, pattern="^proxy_type:"))
    app.add_handler(MessageHandler(filters.Document.ALL, handle_document))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, handle_message))
    
    print("✅ Bot V6.1 is LIVE!")
    print("🚀 Commands: /proxyless, /toggle")
    print(f"🌐 Proxyless engines: {len(PROXYLESS_ENGINES)}")
    print(f"🔄 Proxyless in dual mode: {'ON' if USE_PROXYLESS_IN_DUAL_MODE else 'OFF'}")
    
    await app.initialize()
    await app.start()
    await app.updater.start_polling(drop_pending_updates=True)
    
    try:
        while True:
            await asyncio.sleep(1)
    except KeyboardInterrupt:
        print("\n🛑 Shutting down...")
    finally:
        await app.updater.stop()
        await app.stop()
        await app.shutdown()

def main():
    parser = argparse.ArgumentParser(
        description='Shopify Scraper V4.0 - Multi-Engine Edition',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python shfinder2.1.py --console --proxies proxies.txt
  python shfinder2.1.py --console --proxies proxies.txt --output results.txt
  python shfinder2.1.py --console --proxies proxies.txt --skip-test
  python shfinder2.1.py
  python shfinder2.1.py --bot
        """
    )
    
    parser.add_argument(
        '--console', '-c',
        action='store_true',
        help='Run in console/CLI mode instead of Telegram bot mode'
    )
    
    parser.add_argument(
        '--proxies', '-p',
        type=str,
        help='Path to proxy file (required for console mode)'
    )
    
    parser.add_argument(
        '--output', '-o',
        type=str,
        help='Output file for results (optional, auto-generated if not specified)'
    )
    
    parser.add_argument(
        '--skip-test',
        action='store_true',
        help='Skip proxy testing (use all proxies directly)'
    )
    
    parser.add_argument(
        '--bot', '-b',
        action='store_true',
        help='Run in Telegram bot mode (default)'
    )
    
    args = parser.parse_args()
    
    if args.console:
        if not args.proxies:
            print("❌ Error: --proxies is required for console mode")
            print("💡 Usage: python shfinder2.1.py --console --proxies proxies.txt")
            sys.exit(1)
        
        try:
            console_mode(
                proxy_file=args.proxies,
                output_file=args.output,
                skip_test=args.skip_test
            )
        except KeyboardInterrupt:
            print("\n\n🛑 Scraping stopped by user")
        except Exception as e:
            print(f"\n❌ Error: {e}")
            import traceback
            traceback.print_exc()
    
    elif args.bot or not args.console:
        if not TELEGRAM_AVAILABLE:
            print("❌ Error: Telegram library not installed")
            print("💡 Install with: pip install python-telegram-bot")
            print("💡 Or use console mode: python shfinder2.1.py --console --proxies proxies.txt")
            sys.exit(1)
        
        try:
            # Check if we're already in an async context
            try:
                # If there's a running loop, we can't use asyncio.run()
                loop = asyncio.get_running_loop()
                # If we get here, there's a loop - this shouldn't happen in main()
                # but handle it gracefully
                import nest_asyncio
                nest_asyncio.apply()
                asyncio.run(main_async())
            except RuntimeError:
                # No running loop, safe to use asyncio.run()
                asyncio.run(main_async())
        except KeyboardInterrupt:
            print("\n🛑 V4.0 stopped")
        except Exception as e:
            print(f"❌ Error: {e}")
            import traceback
            traceback.print_exc()

if __name__ == "__main__":
    main()