package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// SiteStatus represents the lifecycle of a scraped site.
type SiteStatus string

const (
	StatusPending  SiteStatus = "pending"  // discovered, not yet checked
	StatusChecking SiteStatus = "checking" // currently being checked
	StatusWorking  SiteStatus = "working"  // returned INCORRECT_NUMBER → site is live
	StatusDead     SiteStatus = "dead"     // checkout failed (no products, blocked, etc.)
	StatusError    SiteStatus = "error"    // transient error, will retry
)

// Site is a row in the sites table.
type Site struct {
	ID            int64      `json:"id"`
	URL           string     `json:"url"`
	Status        SiteStatus `json:"status"`
	ErrorCode     string     `json:"error_code,omitempty"`
	ErrorMsg      string     `json:"error_message,omitempty"`
	CheckoutPrice float64    `json:"checkout_price"`
	CheckCount    int        `json:"check_count"`
	LastChecked   *time.Time `json:"last_checked,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// DB wraps the database connection.
type DB struct {
	conn *sql.DB
}

// NewDB connects to PostgreSQL using DATABASE_URL env var.
func NewDB() (*DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}

	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	conn.SetMaxOpenConns(20)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func (db *DB) migrate() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS sites (
			id          BIGSERIAL PRIMARY KEY,
			url         TEXT NOT NULL UNIQUE,
			status      TEXT NOT NULL DEFAULT 'pending',
			error_code  TEXT NOT NULL DEFAULT '',
			error_msg   TEXT NOT NULL DEFAULT '',
			checkout_price NUMERIC(10,2) NOT NULL DEFAULT 0,
			check_count INTEGER NOT NULL DEFAULT 0,
			last_checked TIMESTAMPTZ,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_sites_status ON sites(status);
		CREATE INDEX IF NOT EXISTS idx_sites_url ON sites(url);
		-- Add column if upgrading from older schema
		ALTER TABLE sites ADD COLUMN IF NOT EXISTS checkout_price NUMERIC(10,2) NOT NULL DEFAULT 0;
	`)
	return err
}

// AddSites inserts new sites (ignores duplicates). Returns count of newly added.
func (db *DB) AddSites(urls []string) (int, error) {
	if len(urls) == 0 {
		return 0, nil
	}

	// Batch insert with ON CONFLICT DO NOTHING
	added := 0
	batchSize := 500
	for i := 0; i < len(urls); i += batchSize {
		end := i + batchSize
		if end > len(urls) {
			end = len(urls)
		}
		batch := urls[i:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for j, u := range batch {
			placeholders[j] = fmt.Sprintf("($%d)", j+1)
			args[j] = strings.TrimSpace(u)
		}

		query := fmt.Sprintf(
			"INSERT INTO sites (url) VALUES %s ON CONFLICT (url) DO NOTHING",
			strings.Join(placeholders, ","),
		)

		res, err := db.conn.Exec(query, args...)
		if err != nil {
			return added, fmt.Errorf("insert batch: %w", err)
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}

	return added, nil
}

// ClaimPendingSites atomically grabs up to `limit` pending sites for checking.
// Sets their status to "checking" and returns them.
func (db *DB) ClaimPendingSites(limit int) ([]Site, error) {
	rows, err := db.conn.Query(`
		UPDATE sites
		SET status = 'checking', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM sites
			WHERE status = 'pending'
			   OR (status = 'error' AND check_count < 3)
			ORDER BY created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, url, status, error_code, error_msg, checkout_price, check_count, last_checked, created_at, updated_at
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		var lastChecked sql.NullTime
		if err := rows.Scan(&s.ID, &s.URL, &s.Status, &s.ErrorCode, &s.ErrorMsg,
			&s.CheckoutPrice, &s.CheckCount, &lastChecked, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if lastChecked.Valid {
			s.LastChecked = &lastChecked.Time
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

// UpdateSiteResult updates a site after checking.
func (db *DB) UpdateSiteResult(id int64, status SiteStatus, errorCode, errorMsg string, checkoutPrice float64) error {
	_, err := db.conn.Exec(`
		UPDATE sites
		SET status = $1, error_code = $2, error_msg = $3, checkout_price = $4,
		    check_count = check_count + 1, last_checked = NOW(), updated_at = NOW()
		WHERE id = $5
	`, status, errorCode, errorMsg, checkoutPrice, id)
	return err
}

// RevertToPending puts a site back to pending without incrementing check_count.
// Used when a check failed due to infrastructure issues (Chrome can't start), not site problems.
func (db *DB) RevertToPending(id int64) error {
	_, err := db.conn.Exec(`
		UPDATE sites SET status = 'pending', updated_at = NOW() WHERE id = $1
	`, id)
	return err
}

// GetWorkingSites returns all sites with status "working".
func (db *DB) GetWorkingSites(limit, offset int) ([]Site, int, error) {
	var total int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM sites WHERE status = 'working'`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := db.conn.Query(`
		SELECT id, url, status, error_code, error_msg, checkout_price, check_count, last_checked, created_at, updated_at
		FROM sites WHERE status = 'working'
		ORDER BY last_checked DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		var lastChecked sql.NullTime
		if err := rows.Scan(&s.ID, &s.URL, &s.Status, &s.ErrorCode, &s.ErrorMsg,
			&s.CheckoutPrice, &s.CheckCount, &lastChecked, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if lastChecked.Valid {
			s.LastChecked = &lastChecked.Time
		}
		sites = append(sites, s)
	}
	return sites, total, rows.Err()
}

// GetStats returns counts by status.
func (db *DB) GetStats() (map[string]int, error) {
	rows, err := db.conn.Query(`SELECT status, COUNT(*) FROM sites GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]int{
		"pending":  0,
		"checking": 0,
		"working":  0,
		"dead":     0,
		"error":    0,
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats[status] = count
	}
	return stats, rows.Err()
}

// ResetStuckChecking resets sites stuck in "checking" for over 5 minutes back to pending.
func (db *DB) ResetStuckChecking() (int, error) {
	res, err := db.conn.Exec(`
		UPDATE sites SET status = 'pending', updated_at = NOW()
		WHERE status = 'checking' AND updated_at < NOW() - INTERVAL '5 minutes'
	`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// DeleteSite removes a site by ID.
func (db *DB) DeleteSite(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM sites WHERE id = $1`, id)
	return err
}

// RecheckAllSites resets ALL sites back to pending for re-validation.
// Non-working sites will be deleted by the worker after checking.
// Returns the number of sites reset.
func (db *DB) RecheckAllSites() (int, error) {
	res, err := db.conn.Exec(`
		UPDATE sites SET status = 'pending', check_count = 0, error_code = '', error_msg = '', updated_at = NOW()
		WHERE status != 'pending'
	`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountWorkingUnder15 returns the number of working sites with checkout_price <= $15.
func (db *DB) CountWorkingUnder15() (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM sites WHERE status = 'working' AND checkout_price > 0 AND checkout_price <= 15`).Scan(&count)
	return count, err
}

func init() {
	// Silence unused import warning — the postgres driver registers itself via init()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
