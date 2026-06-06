package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type DashboardSite struct {
	ID          int64   `json:"id"`
	URL         string  `json:"url"`
	Price       float64 `json:"checkout_price"`
	LastChecked string  `json:"last_checked"`
	CreatedAt   string  `json:"created_at"`
}

func StartDashboard(addr string, db *DB) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard(db))
	mux.HandleFunc("/api/sites", handleAPISites(db))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	srv.ListenAndServe()
}

// handleAPISites serves all working sites as JSON including price info.
func handleAPISites(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 1000
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		sites, total, err := db.GetWorking(limit, offset)
		if err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			return
		}
		if sites == nil {
			sites = []DashboardSite{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"total":  total,
			"limit":  limit,
			"offset": offset,
			"sites":  sites,
		})
	}
}

// handleDashboard renders an HTML table of all working sites.
func handleDashboard(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 500
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		sites, total, err := db.GetWorking(limit, offset)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		stats := db.GetStats()

		var rows strings.Builder
		for i, s := range sites {
			priceStr := fmt.Sprintf("$%.2f", s.Price)
			if s.Price == 0 {
				priceStr = "—"
			}
			rows.WriteString(fmt.Sprintf(
				`<tr><td>%d</td><td><a href="%s" target="_blank">%s</a></td><td class="price">%s</td><td>%s</td><td>%s</td></tr>`,
				offset+i+1, s.URL, s.URL, priceStr, s.LastChecked, s.CreatedAt,
			))
		}

		prevOffset := offset - limit
		if prevOffset < 0 {
			prevOffset = 0
		}
		nextOffset := offset + limit

		var pagination strings.Builder
		if offset > 0 {
			pagination.WriteString(fmt.Sprintf(`<a href="/?limit=%d&offset=%d">← Prev</a> `, limit, prevOffset))
		}
		if nextOffset < total {
			pagination.WriteString(fmt.Sprintf(`<a href="/?limit=%d&offset=%d">Next →</a>`, limit, nextOffset))
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, dashboardHTML,
			stats["pending"], stats["checking"], stats["working"], stats["dead"], stats["error"],
			total, offset+1, offset+len(sites),
			rows.String(),
			pagination.String(),
		)
	}
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Site Checker Dashboard</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #0f0f0f; color: #e0e0e0; }
  header { background: #1a1a2e; padding: 20px 32px; border-bottom: 1px solid #2a2a4a; }
  header h1 { font-size: 1.4rem; font-weight: 600; color: #7c83fd; }
  .stats { display: flex; gap: 16px; padding: 20px 32px; flex-wrap: wrap; }
  .stat { background: #1c1c2e; border: 1px solid #2a2a4a; border-radius: 8px; padding: 14px 20px; min-width: 120px; }
  .stat .label { font-size: 0.72rem; text-transform: uppercase; letter-spacing: .05em; color: #888; }
  .stat .value { font-size: 1.6rem; font-weight: 700; margin-top: 4px; }
  .working .value  { color: #4ade80; }
  .pending .value  { color: #facc15; }
  .checking .value { color: #60a5fa; }
  .dead .value     { color: #f87171; }
  .error .value    { color: #fb923c; }
  .table-wrap { padding: 0 32px 32px; overflow-x: auto; }
  .table-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
  .table-header h2 { font-size: 1rem; color: #aaa; }
  .api-link { font-size: 0.8rem; color: #7c83fd; text-decoration: none; }
  .api-link:hover { text-decoration: underline; }
  table { width: 100%; border-collapse: collapse; font-size: 0.88rem; }
  thead tr { background: #1c1c2e; }
  th { padding: 10px 14px; text-align: left; color: #888; font-weight: 500; border-bottom: 1px solid #2a2a4a; white-space: nowrap; }
  td { padding: 9px 14px; border-bottom: 1px solid #1e1e2e; vertical-align: middle; }
  tr:hover td { background: #16162a; }
  td a { color: #7c83fd; text-decoration: none; }
  td a:hover { text-decoration: underline; }
  .price { color: #4ade80; font-weight: 600; }
  .pagination { padding: 16px 32px; display: flex; gap: 12px; }
  .pagination a { color: #7c83fd; text-decoration: none; font-size: 0.9rem; }
  .pagination a:hover { text-decoration: underline; }
</style>
</head>
<body>
<header><h1>Site Checker Dashboard</h1></header>
<div class="stats">
  <div class="stat pending">  <div class="label">Pending</div>  <div class="value">%d</div></div>
  <div class="stat checking"><div class="label">Checking</div><div class="value">%d</div></div>
  <div class="stat working"> <div class="label">Working</div> <div class="value">%d</div></div>
  <div class="stat dead">    <div class="label">Dead</div>    <div class="value">%d</div></div>
  <div class="stat error">   <div class="label">Error</div>   <div class="value">%d</div></div>
</div>
<div class="table-wrap">
  <div class="table-header">
    <h2>Working Sites — %d total (showing %d–%d)</h2>
    <a class="api-link" href="/api/sites">/api/sites JSON ↗</a>
  </div>
  <table>
    <thead><tr>
      <th>#</th><th>URL</th><th>Cheapest Product</th><th>Last Checked</th><th>Discovered</th>
    </tr></thead>
    <tbody>%s</tbody>
  </table>
</div>
<div class="pagination">%s</div>
</body>
</html>`
