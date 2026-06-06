# Shopify Site Check System — Railway Deployment Guide

## Architecture

```
┌─────────────────┐     POST /sites/add     ┌──────────────────────────┐
│  Scraper Worker  │ ─────────────────────→  │   Checker Service (Go)   │
│   (Python)       │                         │                          │
│                  │                         │  HTTP API:               │
│  Tempest search  │                         │    POST /check           │
│  → find sites    │                         │    POST /sites/add       │
│  → submit to API │                         │    GET  /sites/working   │
└─────────────────┘                         │    GET  /sites/stats     │
                                            │    GET  /sites/export    │
                                            │    GET  /health          │
                                            │                          │
                                            │  Background Worker:      │
                                            │    picks pending sites   │
                                            │    checks via Chrome     │
                                            │    INCORRECT_NUMBER      │
                                            │      → marks "working"   │
                                            └───────────┬──────────────┘
                                                        │
                                                        ▼
                                            ┌──────────────────────────┐
                                            │   PostgreSQL Database    │
                                            │                          │
                                            │  sites table:            │
                                            │    url, status,          │
                                            │    error_code,           │
                                            │    check_count,          │
                                            │    last_checked          │
                                            └──────────────────────────┘
```

## Site Statuses

| Status     | Meaning |
|-----------|---------|
| `pending`  | Discovered by scraper, waiting to be checked |
| `checking` | Currently being checked by the worker |
| `working`  | ✅ Checkout is live (returned INCORRECT_NUMBER or other decline) |
| `dead`     | Checkout broken (no products, blocked, etc.) |
| `error`    | Transient error, will retry (up to 3 times) |

## Railway Setup (3 services + 1 database)

### Step 1: Create a new Railway project
1. Go to https://railway.app and create a new project
2. Connect your GitHub repo (or deploy from local)

### Step 2: Add PostgreSQL
1. Click "+ New" → "Database" → "PostgreSQL"
2. Railway auto-generates `DATABASE_URL` — it'll be shared with your services

### Step 3: Deploy the Checker Service
1. Click "+ New" → "Service" → Connect repo
2. Set the **Root Directory** to the project root
3. It will auto-detect `Dockerfile`
4. Add these environment variables:
   - `DATABASE_URL` → Reference the PostgreSQL variable
   - `CHROME_POOL_SIZE` → `3` (start small, increase if needed)
   - `API_KEY` → Generate a random secret (e.g. `openssl rand -hex 32`)
5. Enable **Public Networking** if you want external access

### Step 4: Deploy the Scraper Worker
1. Click "+ New" → "Service" → Connect same repo
2. Set **Dockerfile Path** to `Dockerfile.scraper`
3. Add these environment variables:
   - `CHECKER_URL` → Use Railway's internal networking: `http://checker.railway.internal:8080`
     (replace "checker" with whatever you named the checker service)
   - `API_KEY` → Same secret as the checker
   - `TEMPEST_COUNTRY` → `US` (or leave empty for worldwide)
   - `SCRAPER_SEARCHES` → `500` (searches per cycle)
   - `SCRAPER_CYCLE_DELAY` → `30` (seconds between cycles)
   - `SCRAPER_BATCH_SIZE` → `50` (sites per API call)

### Step 5: Verify
- Check the checker logs — you should see "Database connected, site management enabled"
- Check the scraper logs — you should see sites being found and submitted
- Hit `GET /sites/stats` to see counts
- Hit `GET /sites/working` to see live sites
- Hit `GET /sites/export` to download a text file of all working sites

## API Reference

### `POST /sites/add` — Submit discovered sites
```json
{
  "urls": [
    "https://store1.myshopify.com",
    "https://store2.myshopify.com"
  ]
}
```
Response:
```json
{"added": 2, "received": 2}
```

### `GET /sites/stats` — Get pipeline statistics
```json
{
  "total": 1500,
  "by_status": {
    "pending": 800,
    "checking": 5,
    "working": 450,
    "dead": 200,
    "error": 45
  }
}
```

### `GET /sites/working?limit=100&offset=0` — List working sites
```json
{
  "total": 450,
  "limit": 100,
  "offset": 0,
  "sites": [
    {
      "id": 42,
      "url": "https://cool-store.myshopify.com",
      "status": "working",
      "error_code": "INCORRECT_NUMBER",
      "last_checked": "2026-03-20T10:30:00Z"
    }
  ]
}
```

### `GET /sites/export` — Download working sites as text file
Returns plain text, one URL per line.

### `POST /check` — Original card check endpoint (unchanged)
Still works exactly as before.

## Environment Variables Summary

### Checker Service
| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | (required) | PostgreSQL connection string |
| `PORT` | `8080` | HTTP port (Railway sets this) |
| `CHROME_POOL_SIZE` | `10` | Number of Chrome processes |
| `API_KEY` | (none) | Shared auth secret |

### Scraper Worker
| Variable | Default | Description |
|----------|---------|-------------|
| `CHECKER_URL` | `http://localhost:8080` | Checker service URL |
| `API_KEY` | (none) | Shared auth secret |
| `TEMPEST_COUNTRY` | `US` | 2-letter country code or empty |
| `SCRAPER_SEARCHES` | `500` | Tempest searches per cycle |
| `SCRAPER_BATCH_SIZE` | `50` | Sites per API submission |
| `SCRAPER_CYCLE_DELAY` | `30` | Seconds between scrape cycles |

## Recommended Railway Settings (PRO Plan)

### Checker Service
- **CPU**: 2 vCPUs
- **Memory**: 4 GB (Chrome is hungry)
- **CHROME_POOL_SIZE**: 3-5 (each process uses ~400MB)

### Scraper Worker
- **CPU**: 0.5 vCPU
- **Memory**: 256 MB
