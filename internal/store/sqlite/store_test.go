package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/migrate"
)

func openStores(t *testing.T, dir string) *Stores {
	t.Helper()
	s, err := Open(context.Background(), dir, clock.NewFake(time.Unix(1_700_000_000, 0)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestStoreSingleWriter(t *testing.T) {
	ctx := context.Background()
	s := openStores(t, t.TempDir())
	defer s.Close()

	const n = 32
	var active, overlaps atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Work.Write(ctx, func(tx *sql.Tx) error {
				if active.Add(1) != 1 {
					overlaps.Add(1)
				}
				defer active.Add(-1)
				if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES (?, 'x')`, fmt.Sprint("k", i)); err != nil {
					return err
				}
				for j := 0; j < 20; j++ { // give other goroutines a chance to overlap
					runtime.Gosched()
				}
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("write failed: %v", err)
		}
	}
	if overlaps.Load() != 0 {
		t.Errorf("writes overlapped %d times", overlaps.Load())
	}
	var cnt int
	err := s.Work.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&cnt)
	})
	if err != nil || cnt != n {
		t.Fatalf("committed rows = %d (err %v), want %d", cnt, err, n)
	}
}

func TestStoreWriteWaitsForInProgressWrite(t *testing.T) {
	ctx := context.Background()
	s := openStores(t, t.TempDir())
	defer s.Close()

	inFirst := make(chan struct{})
	release := make(chan struct{})
	var secondStarted atomic.Bool
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = s.Global.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('a', '1')`); err != nil {
				return err
			}
			close(inFirst)
			<-release
			if secondStarted.Load() {
				return errors.New("second write ran while first was in progress")
			}
			return nil
		})
	}()
	<-inFirst
	go func() {
		defer wg.Done()
		err2 = s.Global.Write(ctx, func(tx *sql.Tx) error {
			secondStarted.Store(true)
			_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('b', '2')`)
			return err
		})
	}()
	for i := 0; i < 1000; i++ {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if err1 != nil || err2 != nil {
		t.Fatalf("err1=%v err2=%v", err1, err2)
	}
	if !secondStarted.Load() {
		t.Fatal("second write never ran")
	}
}

func TestStoreReadDuringWriteAndRollback(t *testing.T) {
	ctx := context.Background()
	s := openStores(t, t.TempDir())
	defer s.Close()

	boom := errors.New("boom")
	err := s.CI.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('r', '1')`); err != nil {
			return err
		}
		// A read during an open write transaction must not block or fail.
		if err := s.CI.Read(ctx, func(rt *sql.Tx) error {
			var c int
			return rt.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&c)
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	var c int
	_ = s.CI.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&c) })
	if c != 0 {
		t.Fatalf("rolled back write left %d rows", c)
	}
	// Reader is read-only.
	err = s.CI.Read(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('w', '1')`)
		return err
	})
	if err == nil {
		t.Fatal("write through Read succeeded")
	}
}

func TestStoreSecondOpenFails(t *testing.T) {
	dir := t.TempDir()
	s := openStores(t, dir)
	_, err := Open(context.Background(), dir, clock.NewFake(time.Unix(0, 0)))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open err = %v, want ErrLocked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openStores(t, dir) // lock released on Close
	s2.Close()
}

func TestStoreReopenIdempotentAndPragmas(t *testing.T) {
	dir := t.TempDir()
	openStores(t, dir).Close()
	s := openStores(t, dir)
	defer s.Close()
	var mode string
	var fk int
	err := s.Work.Write(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
			return err
		}
		return tx.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	})
	if err != nil || mode != "wal" || fk != 1 {
		t.Fatalf("mode=%q fk=%d err=%v", mode, fk, err)
	}
}

func TestStoreNewerSchemaRefused(t *testing.T) {
	dir := t.TempDir()
	s := openStores(t, dir)
	err := s.Work.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (9999, 'x')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err = Open(context.Background(), dir, clock.NewFake(time.Unix(0, 0)))
	if !errors.Is(err, migrate.ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
	// The lock must have been released by the failed Open.
	s2, err := Open(context.Background(), t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}
