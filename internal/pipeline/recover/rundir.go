package recover

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunsDir is FOREMAN_HOME/runs.
func RunsDir(home string) string { return filepath.Join(home, "runs") }

// RemoveRunDir deletes runs/<run id>/ once the Run's results are committed to
// the store (D-086; the caller orders it after the commit). It removes only a
// direct child of runs/: the run id must be a plain name, the entry's real
// path must lie directly inside the real runs/ directory, and nothing is ever
// followed through a symlink (a link is removed as a link). A missing
// directory is not an error.
func RemoveRunDir(home, runID string) error {
	if runID == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\`) || strings.ContainsRune(runID, 0) {
		return fmt.Errorf("recover: invalid run id %q", runID)
	}
	runs, err := filepath.EvalSymlinks(RunsDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return removeChild(runs, runID)
}

// removeChild removes runs/<name> (runs is already a real path).
func removeChild(runs, name string) error {
	entry := filepath.Join(runs, name)
	fi, err := os.Lstat(entry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return os.Remove(entry) // the link itself, never its target
	case fi.IsDir():
		// realpath containment: after resolving, the entry is still runs/<name>.
		real, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return err
		}
		if real != entry {
			return fmt.Errorf("recover: %s resolves to %s, outside runs/", entry, real)
		}
		return removeTree(entry) // does not follow symlinks inside the tree
	}
	return nil // a regular file is not a run directory: left alone
}

// SweepRunDirs is the startup sweep (D-086, after the fixed steps of D-047):
// it removes every direct child of runs/ whose Run is not pending, running or
// killing in the store. Running sessions (including detached ones waiting to
// be taken over) keep their directory. It returns how many entries it removed.
func SweepRunDirs(ctx context.Context, d Deps) (int, error) {
	runs, err := filepath.EvalSymlinks(RunsDir(d.Home))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	live := map[string]bool{}
	err = d.Stores.Work.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM runs WHERE status IN ('pending', 'running', 'killing')`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			live[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(runs)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if live[e.Name()] {
			continue
		}
		if err := removeChild(runs, e.Name()); err != nil {
			return removed, err
		}
		if _, serr := os.Lstat(filepath.Join(runs, e.Name())); errors.Is(serr, os.ErrNotExist) {
			removed++
		}
	}
	return removed, nil
}

// removeTree is os.RemoveAll that, when it fails, first restores owner write
// and search permission on every directory below path and tries again: a Go
// module cache is read-only unless the toolchain ran with -modcacherw. Symbolic
// links are never followed (WalkDir does not, and a link is not chmod-ed).
func removeTree(path string) error {
	err := os.RemoveAll(path)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, werr error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
