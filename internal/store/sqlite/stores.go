package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/migrate"
)

// ErrLocked means another process (or Stores) already owns the directory.
var ErrLocked = errors.New("sqlite: directory is locked by another foremand")

// LockFile is the name of the exclusive lock file inside the store directory.
const LockFile = "foremand.lock"

// Stores holds the three databases under one directory plus the exclusive
// directory lock that enforces a single foremand writer.
type Stores struct {
	Global, Work, CI *DB
	lock             *os.File
}

// Open takes an exclusive non-blocking flock on dir/foremand.lock (failing
// fast with ErrLocked), then opens and migrates global.db, work.db and ci.db.
func Open(ctx context.Context, dir string, clk clock.Clock) (*Stores, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(filepath.Join(dir, LockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lf.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, err
	}
	s := &Stores{lock: lf}
	for _, e := range []struct {
		dst  **DB
		name string
	}{{&s.Global, migrate.Global}, {&s.Work, migrate.Work}, {&s.CI, migrate.CI}} {
		db, err := openDB(ctx, filepath.Join(dir, e.name+".db"), e.name, clk)
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		*e.dst = db
	}
	return s, nil
}

// Close closes all databases and releases the directory lock.
func (s *Stores) Close() error {
	var first error
	for _, d := range []*DB{s.Global, s.Work, s.CI} {
		if d == nil {
			continue
		}
		if err := d.Close(); err != nil && first == nil {
			first = err
		}
	}
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
	return first
}
