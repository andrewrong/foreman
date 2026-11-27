package integrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// HaltInfo describes the default-branch change that stopped pushes (D-050).
type HaltInfo struct{ Branch, Before, After string }

// HaltStore keeps the push-halt flag across restarts (global.db in
// production). The halt stays in force until ClearHalt, which only the owner's
// resolution of H6 may call.
type HaltStore interface {
	LoadHalt(ctx context.Context) (HaltInfo, bool, error)
	SaveHalt(ctx context.Context, h HaltInfo) error
	ClearHalt(ctx context.Context) error
}

// Open reuses the integration directory Init created earlier (foremand
// restart). It refuses a directory that is not what Init makes: a git work
// tree whose "mirror" remote is this mirror, whose "origin" is the mirror's
// origin URL, and whose push.default is "nothing". A directory that fails
// this (for example a clone cut short by a crash) is reported, never silently
// repaired or deleted.
func Open(ctx context.Context, r *git.Runner, m *session.Mirror, path string, id git.Identity) (*Dir, error) {
	def, err := m.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	url, err := m.RemoteURL(ctx)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(filepath.Join(path, ".git")); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("integrate: %s is not an integration checkout (no .git directory)", path)
	}
	d := &Dir{Path: path, Default: def, r: r.WithIdentity(id)}
	want := map[string]string{
		"remote.mirror.url": m.Dir,
		"remote.origin.url": url,
		"push.default":      "nothing",
	}
	for _, key := range []string{"remote.mirror.url", "remote.origin.url", "push.default"} {
		got, err := d.r.Line(ctx, path, "config", "--get", key)
		if err != nil {
			return nil, fmt.Errorf("integrate: %s lacks %s: %w", path, key, err)
		}
		if got != want[key] {
			return nil, fmt.Errorf("integrate: %s has %s = %q, want %q", path, key, got, want[key])
		}
	}
	return d, nil
}

// UseHaltStore attaches the persistent push-halt flag and loads it: a halt
// recorded before a restart is in force again before the first push.
func (d *Dir) UseHaltStore(ctx context.Context, s HaltStore) error {
	_, halted, err := s.LoadHalt(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.store, d.halted = s, halted
	return nil
}

// ResumePushes clears the halt, in memory and in the store. Only the owner
// resolving H6 calls it.
func (d *Dir) ResumePushes(ctx context.Context) error {
	d.mu.Lock()
	s := d.store
	d.mu.Unlock()
	if s != nil {
		if err := s.ClearHalt(ctx); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.halted = false
	d.mu.Unlock()
	return nil
}
