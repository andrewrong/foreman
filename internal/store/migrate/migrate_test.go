package migrate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"

	"github.com/andrewrong/foreman/internal/clock"
)

func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func versions(t *testing.T, db *sql.DB) (vs []int, at string) {
	t.Helper()
	rows, err := db.Query(`SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v, &at); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	return vs, at
}

func TestMigrateIdempotentOrdered(t *testing.T) {
	ctx := context.Background()
	db := openRaw(t)
	clk := clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	fsys := fstest.MapFS{
		"0002_b.sql": {Data: []byte(`INSERT INTO a VALUES (2)`)},
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (n INTEGER)`)},
	}
	for i := 0; i < 2; i++ {
		if err := Apply(ctx, db, fsys, clk); err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
	}
	vs, at := versions(t, db)
	if len(vs) != 2 || vs[0] != 1 || vs[1] != 2 {
		t.Fatalf("versions = %v", vs)
	}
	if at != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("applied_at = %q", at)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM a`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d err %v (migration re-ran?)", n, err)
	}
	// A later migration is applied incrementally.
	fsys["0003_c.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO a VALUES (3)`)}
	if err := Apply(ctx, db, fsys, clk); err != nil {
		t.Fatal(err)
	}
	if vs, _ := versions(t, db); len(vs) != 3 {
		t.Fatalf("versions = %v", vs)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openRaw(t)
	fsys := fstest.MapFS{
		"0001_a.sql":   {Data: []byte(`CREATE TABLE a (n INTEGER)`)},
		"0002_bad.sql": {Data: []byte(`CREATE TABLE b (n INTEGER); INSERT INTO nope VALUES (1)`)},
	}
	if err := Apply(ctx, db, fsys, clock.NewFake(time.Unix(0, 0))); err == nil {
		t.Fatal("want error")
	}
	if vs, _ := versions(t, db); len(vs) != 1 {
		t.Fatalf("versions = %v, want only 1", vs)
	}
	if _, err := db.Exec(`SELECT * FROM b`); err == nil {
		t.Fatal("table b survived failed migration")
	}
}

func TestMigrateNewerRefused(t *testing.T) {
	ctx := context.Background()
	db := openRaw(t)
	clk := clock.NewFake(time.Unix(0, 0))
	newer := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE a (n INTEGER)`)},
		"0002_b.sql": {Data: []byte(`CREATE TABLE b (n INTEGER)`)},
	}
	if err := Apply(ctx, db, newer, clk); err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE a (n INTEGER)`)}}
	if err := Apply(ctx, db, older, clk); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
}

func TestMigrateEmbeddedSetsApply(t *testing.T) {
	for _, name := range []string{Global, Work, CI} {
		fsys, err := FS(name)
		if err != nil {
			t.Fatal(err)
		}
		db := openRaw(t)
		if err := Apply(context.Background(), db, fsys, clock.NewFake(time.Unix(0, 0))); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('k', 'v')`); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
