package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// A panic inside Write or Read must roll the transaction back, re-panic, and
// leave the connections usable (the single write connection is not stuck in
// an open transaction).
func TestStorePanicRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openStores(t, t.TempDir())
	defer s.Close()

	recovered := func(fn func()) (p any) {
		defer func() { p = recover() }()
		fn()
		return nil
	}

	p := recovered(func() {
		_ = s.Work.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('p', '1')`); err != nil {
				return err
			}
			panic("boom")
		})
	})
	if p != "boom" {
		t.Fatalf("Write did not re-panic with the original value: %v", p)
	}
	// The write connection is free again and the insert was rolled back.
	if err := s.Work.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('p', '2')`)
		return err
	}); err != nil {
		t.Fatalf("write after panic: %v", err)
	}
	var v string
	if err := s.Work.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT value FROM meta WHERE key = 'p'`).Scan(&v)
	}); err != nil || v != "2" {
		t.Fatalf("value = %q (err %v), want 2 (panicked insert must be rolled back)", v, err)
	}

	p = recovered(func() {
		_ = s.Work.Read(ctx, func(tx *sql.Tx) error { panic("rboom") })
	})
	if p != "rboom" {
		t.Fatalf("Read did not re-panic: %v", p)
	}
	if err := s.Work.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT 1`).Scan(new(int)) }); err != nil {
		t.Fatalf("read after panic: %v", err)
	}
}
