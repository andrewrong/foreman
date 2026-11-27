// Package session manages foremand's bare mirror and the per-requirement
// session repos cloned from it (D-045, D-050).
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// Mirror is foremand's bare repo at <home>/projects/<p>/repo.git.
type Mirror struct {
	Dir string
	r   *git.Runner
}

// MirrorDir returns the conventional mirror path for project p under home.
func MirrorDir(home, project string) string {
	return filepath.Join(home, "projects", project, "repo.git")
}

// OpenMirror wraps an existing bare mirror.
func OpenMirror(r *git.Runner, dir string) *Mirror { return &Mirror{Dir: dir, r: r} }

// CloneMirror creates the bare mirror from a remote URL or local path. Only
// refs/heads/* are tracked: +refs/heads/*:refs/heads/*.
func CloneMirror(ctx context.Context, r *git.Runner, dir, source string) (*Mirror, error) {
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("session: mirror dir exists: %s", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if _, err := r.Run(ctx, "", "clone", "--bare", "--no-tags", "--", source, dir); err != nil {
		return nil, err
	}
	if err := r.SetConfig(ctx, filepath.Join(dir, "config"), "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
		return nil, err
	}
	return OpenMirror(r, dir), nil
}

// Update fetches from the remote into the mirror (hardened, pruning).
func (m *Mirror) Update(ctx context.Context) error {
	_, err := m.r.Run(ctx, m.Dir, "-c", "fetch.fsckObjects=true", "fetch", "--prune", "--no-tags", "origin")
	return err
}

// DefaultBranch is the branch the mirror's HEAD points at.
func (m *Mirror) DefaultBranch(ctx context.Context) (string, error) {
	return m.r.Line(ctx, m.Dir, "symbolic-ref", "--short", "HEAD")
}

// RemoteURL is the URL of the mirror's origin.
func (m *Mirror) RemoteURL(ctx context.Context) (string, error) {
	return m.r.Line(ctx, m.Dir, "config", "--get", "remote.origin.url")
}

// BranchSHA resolves refs/heads/<branch> in the mirror.
func (m *Mirror) BranchSHA(ctx context.Context, branch string) (string, error) {
	if err := git.ValidateName(branch); err != nil {
		return "", err
	}
	sha, err := m.r.Line(ctx, m.Dir, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	var ge *git.Error
	if errors.As(err, &ge) {
		return "", fmt.Errorf("session: branch %q not in mirror: %w", branch, err)
	}
	return sha, err
}
