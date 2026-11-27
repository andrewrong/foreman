// Package cache stores ETag-validated GET responses in global.db table
// forge_etags (forge.yaml FG-3). It knows nothing about HTTP; internal/forge/
// client drives it through its Cache interface.
package cache

import (
	"context"
	"database/sql"
	"errors"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// Limits from FG-3.
const (
	MaxBodyBytes  = 256 << 10
	MaxTotalBytes = 64 << 20
)

// Cache is the forge_etags store.
type Cache struct {
	db       *sqlite.DB
	clk      clock.Clock
	maxTotal int64
}

// New returns a Cache over global.db. maxTotal <= 0 means MaxTotalBytes.
func New(db *sqlite.DB, clk clock.Clock, maxTotal int64) *Cache {
	if maxTotal <= 0 {
		maxTotal = MaxTotalBytes
	}
	return &Cache{db: db, clk: clk, maxTotal: maxTotal}
}

// Lookup returns the stored ETag and body for url.
func (c *Cache) Lookup(ctx context.Context, url string) (etag string, body []byte, ok bool, err error) {
	err = c.db.Read(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRowContext(ctx, `SELECT etag, body FROM forge_etags WHERE url = ?`, url).Scan(&etag, &body)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		ok = e == nil
		return e
	})
	return
}

// Store saves a response. Bodies larger than MaxBodyBytes are not stored (and
// drop any older entry for the url). After the write, oldest entries by
// fetched_at are evicted until the total body size fits.
func (c *Cache) Store(ctx context.Context, url, etag string, body []byte) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		if len(body) > MaxBodyBytes || etag == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM forge_etags WHERE url = ?`, url)
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO forge_etags (url, etag, body, fetched_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT(url) DO UPDATE SET etag = excluded.etag, body = excluded.body, fetched_at = excluded.fetched_at`,
			url, etag, body, c.clk.Now().UnixMilli()); err != nil {
			return err
		}
		return c.evict(ctx, tx)
	})
}

// Touch refreshes fetched_at after a 304 so a still-valid entry is evicted last.
func (c *Cache) Touch(ctx context.Context, url string) error {
	return c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE forge_etags SET fetched_at = ? WHERE url = ?`, c.clk.Now().UnixMilli(), url)
		return err
	})
}

// Size returns the total stored body bytes.
func (c *Cache) Size(ctx context.Context) (n int64, err error) {
	err = c.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(body)), 0) FROM forge_etags`).Scan(&n)
	})
	return
}

func (c *Cache) evict(ctx context.Context, tx *sql.Tx) error {
	for {
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(body)), 0) FROM forge_etags`).Scan(&total); err != nil {
			return err
		}
		if total <= c.maxTotal {
			return nil
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM forge_etags WHERE url = (SELECT url FROM forge_etags ORDER BY fetched_at, url LIMIT 1)`)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
	}
}
