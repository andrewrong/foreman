package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// ErrIdentityNotNoreply is returned when the session identity email is not a
// noreply address (privguard P4/P7).
var ErrIdentityNotNoreply = errors.New("session: identity email must be a noreply address")

// Spec describes one session repo.
type Spec struct {
	ReqID      string
	BaseBranch string // mirror branch the requirement branch starts from
	Identity   git.Identity
}

// Repo is a created session repo. foremand never runs git with Dir as cwd.
type Repo struct {
	Dir    string
	Branch string // foreman/<req id>
}

// Create clones the mirror with --shared into dir, removes every upstream,
// sets push.default=nothing and the repo-local identity, and checks out the
// requirement branch. After the clone, all git calls use --git-dir/--file with
// a cwd outside the repo (D-056).
func Create(ctx context.Context, r *git.Runner, m *Mirror, dir string, spec Spec) (*Repo, error) {
	branch, err := git.RequirementBranch(spec.ReqID)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(spec.Identity.Email, "noreply") || spec.Identity.Name == "" {
		return nil, ErrIdentityNotNoreply
	}
	sha, err := m.BranchSHA(ctx, spec.BaseBranch)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("session: repo dir exists: %s", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if _, err := r.Run(ctx, "", "clone", "--shared", "--branch", spec.BaseBranch, "--", m.Dir, dir); err != nil {
		return nil, err
	}
	gd := filepath.Join(dir, ".git")
	cfg := filepath.Join(gd, "config")
	if _, err := r.Run(ctx, "", "config", "--file", cfg, "--remove-section", "remote.origin"); err != nil {
		return nil, err
	}
	// The clone's branch.<base> upstream section; absent sections are fine.
	_, _ = r.Run(ctx, "", "config", "--file", cfg, "--remove-section", "branch."+spec.BaseBranch)
	refs, err := r.Run(ctx, "", "--git-dir", gd, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return nil, err
	}
	for _, ref := range strings.Fields(refs) {
		if _, err := r.Run(ctx, "", "--git-dir", gd, "update-ref", "-d", ref); err != nil {
			return nil, err
		}
	}
	for k, v := range map[string]string{
		"push.default": "nothing",
		"user.name":    spec.Identity.Name,
		"user.email":   spec.Identity.Email,
	} {
		if err := r.SetConfig(ctx, cfg, k, v); err != nil {
			return nil, err
		}
	}
	if _, err := r.Run(ctx, "", "--git-dir", gd, "update-ref", "refs/heads/"+branch, sha); err != nil {
		return nil, err
	}
	if _, err := r.Run(ctx, "", "--git-dir", gd, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return nil, err
	}
	return &Repo{Dir: dir, Branch: branch}, nil
}

// Refresh replaces the session repo at dir by a new one cloned from the mirror
// at spec.BaseBranch (the requirement branch, put there with
// Mirror.ImportBranch), so the next session sees what was merged back. The
// old repo is never read or run: git is not executed on it at all (D-056), it
// is only renamed and deleted, and the new clone comes from foremand's own
// mirror. Uncommitted leftovers of the old working tree are discarded; a
// session still running in it must have ended (v1 steps are serial). A crash
// between the renames leaves dir missing or the old repo in place; the next
// Refresh (merge-back is idempotent) finishes the job.
func (m *Mirror) Refresh(ctx context.Context, dir string, spec Spec) (*Repo, error) {
	return Refresh(ctx, m.r, m, dir, spec)
}

// Refresh is Mirror.Refresh with an explicit runner.
func Refresh(ctx context.Context, r *git.Runner, m *Mirror, dir string, spec Spec) (*Repo, error) {
	fresh, old := dir+".new", dir+".old"
	for _, p := range []string{fresh, old} {
		if err := os.RemoveAll(p); err != nil {
			return nil, err
		}
	}
	repo, err := Create(ctx, r, m, fresh, spec)
	if err != nil {
		_ = os.RemoveAll(fresh)
		return nil, err
	}
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(fresh)
		return nil, err
	}
	if err := os.Rename(fresh, dir); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(old); err != nil {
		return nil, err
	}
	repo.Dir = dir
	return repo, nil
}

// Remove deletes a session repo directory. It refuses anything that is not a
// git working repo (has a .git directory).
func Remove(dir string) error {
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !fi.IsDir() {
		return fmt.Errorf("session: %s is not a session repo", dir)
	}
	return os.RemoveAll(dir)
}
