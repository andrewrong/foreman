package revision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/andrewrong/foreman/internal/config/schema"
)

// RevisionFile names the file in a snapshot that holds its revision number.
const RevisionFile = "REVISION"

// Snapshot copies the effective config into runDir/config as read-only files
// (0444, directories 0555) together with a REVISION file (C-13), and returns
// the revision number. The config must load; an invalid config yields an
// error and no snapshot. Call it at Run start; the run reads its config with
// schema.Load(runDir) from then on. The caller removes a run directory with
// RemoveSnapshot (read-only directories need their mode restored first).
func (s *Store) Snapshot(ctx context.Context, runDir string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := schema.Load(s.home); err != nil {
		return 0, fmt.Errorf("config snapshot refused: %w", err)
	}
	var rev int64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(MAX(rev),0) FROM config_revisions`).Scan(&rev)
	})
	if err != nil {
		return 0, err
	}
	files, err := scanDisk(s.root())
	if err != nil {
		return 0, err
	}
	return rev, WriteSnapshot(runDir, rev, files)
}

// WriteSnapshot writes files (relative path -> content) and the revision
// number under runDir/config, read-only.
func WriteSnapshot(runDir string, rev int64, files map[string]string) error {
	root := filepath.Join(runDir, schema.Dir)
	if _, err := os.Lstat(root); err == nil {
		return fmt.Errorf("revision: snapshot already exists: %s", root)
	}
	all := map[string]string{RevisionFile: strconv.FormatInt(rev, 10) + "\n"}
	for k, v := range files {
		all[k] = v
	}
	dirs := map[string]bool{root: true}
	for rel, body := range all {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		dirs[filepath.Dir(p)] = true
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o444); err != nil {
			return err
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, d := range ordered {
		if err := os.Chmod(d, 0o555); err != nil {
			return err
		}
	}
	return nil
}

// ReadSnapshotRevision returns the revision recorded in runDir's snapshot.
func ReadSnapshotRevision(runDir string) (int64, error) {
	b, err := os.ReadFile(filepath.Join(runDir, schema.Dir, RevisionFile))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

// RemoveSnapshot deletes runDir/config, first restoring write permission on
// its directories.
func RemoveSnapshot(runDir string) error {
	root := filepath.Join(runDir, schema.Dir)
	var errs []error
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			errs = append(errs, os.Chmod(p, 0o700))
		}
		return nil
	})
	return errors.Join(append(errs, os.RemoveAll(root))...)
}
