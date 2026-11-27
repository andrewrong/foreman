// Package sqlite opens foreman's sqlite databases (D-004) with a single
// serialised writer and a separate read pool.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/migrate"
)

// DB is one sqlite database. All writes go through a single connection;
// reads use a separate pool and, under WAL, never block on the writer.
type DB struct {
	path   string
	writer *sql.DB // MaxOpenConns(1): the pool itself serialises writers
	reader *sql.DB
}

func dsn(path string, extra ...string) string {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)", "synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	for _, p := range extra {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode()
}

func openDB(ctx context.Context, path, name string, clk clock.Clock) (*DB, error) {
	w, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	fsys, err := migrate.FS(name)
	if err == nil {
		err = migrate.Apply(ctx, w, fsys, clk)
	}
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("sqlite: %s: %w", filepath.Base(path), err)
	}
	r, err := sql.Open("sqlite", dsn(path, "query_only(1)"))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	return &DB{path: path, writer: w, reader: r}, nil
}

// Write runs fn in one transaction on the single write connection. Calls are
// serialised: a call made while another is in progress waits (or returns
// ctx.Err()). fn's error rolls back; nil commits.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return runTx(ctx, d.writer, false, fn)
}

// Read runs fn in a read-only transaction on the read pool.
func (d *DB) Read(ctx context.Context, fn func(*sql.Tx) error) error {
	return runTx(ctx, d.reader, true, fn)
}

func runTx(ctx context.Context, db *sql.DB, ro bool, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: ro})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// Close closes both pools.
func (d *DB) Close() error {
	e1 := d.reader.Close()
	e2 := d.writer.Close()
	if e1 != nil {
		return e1
	}
	return e2
}
