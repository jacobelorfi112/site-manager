package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
)

type SiteStatus string

const (
	StatusPending  SiteStatus = "pending"
	StatusChecking SiteStatus = "checking"
	StatusWorking  SiteStatus = "working"
	StatusDead     SiteStatus = "dead"
	StatusError    SiteStatus = "error"
)

type Site struct {
	ID          int64
	URL         string
	Status      SiteStatus
	CheckCount  int
	LastChecked *time.Time
}

type DB struct {
	conn *sql.DB
}

func NewDB() (*DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(3)
	conn.SetConnMaxLifetime(5 * time.Minute)
	if err := conn.Ping(); err != nil {
		return nil, err
	}
	if err := migrate(conn); err != nil {
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func migrate(conn *sql.DB) error {
	_, err := conn.Exec(`
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
		ALTER TABLE sites ADD COLUMN IF NOT EXISTS checkout_price NUMERIC(10,2) NOT NULL DEFAULT 0;
	`)
	return err
}

// ClaimPending atomically claims up to limit pending sites, sets them to checking.
func (db *DB) ClaimPending(limit int) ([]Site, error) {
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
		RETURNING id, url, status, check_count, last_checked
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		var lc sql.NullTime
		if err := rows.Scan(&s.ID, &s.URL, &s.Status, &s.CheckCount, &lc); err != nil {
			return nil, err
		}
		if lc.Valid {
			s.LastChecked = &lc.Time
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

// MarkWorking saves a site as working with the checkout price.
func (db *DB) MarkWorking(id int64, price float64) error {
	_, err := db.conn.Exec(`
		UPDATE sites
		SET status = 'working', checkout_price = $1, error_code = '', error_msg = '',
		    check_count = check_count + 1, last_checked = NOW(), updated_at = NOW()
		WHERE id = $2
	`, price, id)
	return err
}

// MarkDead deletes a site that failed checking.
func (db *DB) MarkDead(id int64, errorCode, errorMsg string) error {
	_, err := db.conn.Exec(`DELETE FROM sites WHERE id = $1`, id)
	return err
}

// MarkError keeps a site for retry.
func (db *DB) MarkError(id int64, errorCode, errorMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE sites
		SET status = 'error', error_code = $1, error_msg = $2,
		    check_count = check_count + 1, last_checked = NOW(), updated_at = NOW()
		WHERE id = $3
	`, errorCode, errorMsg, id)
	return err
}

// ResetStuck resets sites stuck in 'checking' for over 10 minutes.
func (db *DB) ResetStuck() (int, error) {
	res, err := db.conn.Exec(`
		UPDATE sites SET status = 'pending', updated_at = NOW()
		WHERE status = 'checking' AND updated_at < NOW() - INTERVAL '10 minutes'
	`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetWorking returns all working sites for the dashboard.
func (db *DB) GetWorking(limit, offset int) ([]DashboardSite, int, error) {
	var total int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM sites WHERE status = 'working'`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := db.conn.Query(`
		SELECT id, url, checkout_price, last_checked, created_at
		FROM sites WHERE status = 'working'
		ORDER BY last_checked DESC NULLS LAST
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sites []DashboardSite
	for rows.Next() {
		var s DashboardSite
		var lc sql.NullTime
		var ca sql.NullTime
		if err := rows.Scan(&s.ID, &s.URL, &s.Price, &lc, &ca); err != nil {
			return nil, 0, err
		}
		if lc.Valid {
			s.LastChecked = lc.Time.Format("2006-01-02 15:04:05")
		}
		if ca.Valid {
			s.CreatedAt = ca.Time.Format("2006-01-02 15:04:05")
		}
		sites = append(sites, s)
	}
	return sites, total, rows.Err()
}

// GetStats returns counts grouped by status.
func (db *DB) GetStats() map[string]int {
	rows, err := db.conn.Query(`SELECT status, COUNT(*) FROM sites GROUP BY status`)
	if err != nil {
		log.Printf("[db] stats error: %v", err)
		return nil
	}
	defer rows.Close()
	stats := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		rows.Scan(&status, &count)
		stats[status] = count
	}
	return stats
}

func (db *DB) Close() {
	db.conn.Close()
}
