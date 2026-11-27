// Package migrate applies ordered, embedded SQL migrations to a sqlite
// database and tracks them in schema_migrations.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/andrewrong/foreman/internal/clock"
)

//go:embed sql
var embedded embed.FS

// Names of the three databases.
const (
	Global = "global"
	Work   = "work"
	CI     = "ci"
)

// ErrNewerSchema is returned when the database has applied migrations the
// binary does not know about.
var ErrNewerSchema = errors.New("migrate: database schema is newer than this binary")

// FS returns the embedded migrations for one database name.
func FS(name string) (fs.FS, error) {
	return fs.Sub(embedded, path.Join("sql", name))
}

type migration struct {
	version int
	name    string
	sql     string
}

func load(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migrate: bad migration file name %q", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: duplicate version %d (%s, %s)", v, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{v, e.Name(), string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Apply brings db up to the latest version in fsys. It is idempotent. It
// fails with ErrNewerSchema if db has versions fsys does not contain. Each
// migration runs in its own transaction together with its bookkeeping row.
// The caller must ensure db is the single writer connection.
func Apply(ctx context.Context, db *sql.DB, fsys fs.FS, clk clock.Clock) error {
	migs, err := load(fsys)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	var cur sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&cur); err != nil {
		return err
	}
	latest := 0
	if len(migs) > 0 {
		latest = migs[len(migs)-1].version
	}
	if int(cur.Int64) > latest {
		return fmt.Errorf("%w: database at version %d, binary knows %d", ErrNewerSchema, cur.Int64, latest)
	}
	for _, m := range migs {
		if m.version <= int(cur.Int64) {
			continue
		}
		if err := applyOne(ctx, db, m, clk); err != nil {
			return fmt.Errorf("migrate: %s: %w", m.name, err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, m migration, clk clock.Clock) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	at := clk.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, m.version, at); err != nil {
		return err
	}
	return tx.Commit()
}
