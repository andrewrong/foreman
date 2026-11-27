// Package integrate is foremand's own integration directory: a non-bare
// --shared clone of the mirror in which session commits are fetched, squashed,
// rebased and pushed. It is never the owner's checkout, and git is never run
// with a session repo as cwd (D-045, D-050, D-056).
package integrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// StepRefPrefix is the namespace local refs fetched from session repos must use.
const StepRefPrefix = "refs/foreman/"

// Dir is an initialised integration directory.
type Dir struct {
	Path    string
	Default string // default branch of the project (never pushed to)
	r       *git.Runner

	mu     sync.Mutex
	halted bool
}

// Init clones the mirror (--shared) into path. The "mirror" remote is the
// mirror; "origin" is re-pointed at the real remote URL, which is the only
// push destination. Identity is the commit identity for squash/rebase.
func Init(ctx context.Context, r *git.Runner, m *session.Mirror, path string, id git.Identity) (*Dir, error) {
	def, err := m.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	url, err := m.RemoteURL(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("integrate: dir exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := r.Run(ctx, "", "clone", "--shared", "--no-checkout", "--", m.Dir, path); err != nil {
		return nil, err
	}
	d := &Dir{Path: path, Default: def, r: r.WithIdentity(id)}
	for _, args := range [][]string{
		{"remote", "rename", "origin", "mirror"},
		{"remote", "add", "origin", url},
		{"config", "push.default", "nothing"},
	} {
		if _, err := d.r.Run(ctx, path, args...); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// SyncFromMirror updates refs/remotes/mirror/* from the (already updated) mirror.
func (d *Dir) SyncFromMirror(ctx context.Context) error {
	_, err := d.r.Run(ctx, d.Path, "fetch", "--prune", "--no-tags", "mirror")
	return err
}

// FetchFromSession fetches refs/heads/<branch> of a session repo into the
// local ref localRef (under refs/foreman/) and returns its sha. The session
// repo is only a fetch source (a path argument); it is never the cwd, and the
// hardened -c options also reach the spawned upload-pack.
func (d *Dir) FetchFromSession(ctx context.Context, sessionRepo, branch, localRef string) (string, error) {
	if err := git.ValidateName(branch); err != nil {
		return "", err
	}
	if err := git.ValidateName(localRef); err != nil || !strings.HasPrefix(localRef, StepRefPrefix) {
		return "", git.ErrInvalidName
	}
	if !filepath.IsAbs(sessionRepo) {
		return "", fmt.Errorf("integrate: session repo path must be absolute: %q", sessionRepo)
	}
	_, err := d.r.Run(ctx, d.Path, "-c", "fetch.fsckObjects=true", "fetch", "--no-tags",
		"--no-recurse-submodules", "--", sessionRepo, "+refs/heads/"+branch+":"+localRef)
	if err != nil {
		return "", err
	}
	return d.r.Line(ctx, d.Path, "rev-parse", "--verify", "--end-of-options", localRef+"^{commit}")
}

// ensureBranch makes refs/heads/<branch> exist locally, starting from the
// mirror's copy of the branch or, failing that, the mirror's default branch.
func (d *Dir) ensureBranch(ctx context.Context, branch string) error {
	if _, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil
	}
	start := "refs/remotes/mirror/" + branch
	if _, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", "--quiet", start); err != nil {
		start = "refs/remotes/mirror/" + d.Default
	}
	_, err := d.r.Run(ctx, d.Path, "update-ref", "refs/heads/"+branch, start)
	return err
}
