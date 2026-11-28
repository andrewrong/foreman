package integrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

var (
	// ErrPushTargetIsDefault: the push target equals the default branch (D-050).
	ErrPushTargetIsDefault = errors.New("integrate: push target is the default branch")
	// ErrPushHalted: a previous push changed the default branch; all pushes of
	// this project stop until the owner resolves H6.
	ErrPushHalted = errors.New("integrate: pushes halted after default branch changed")
)

// DefaultBranchMovedError is returned when ls-remote shows the default branch
// sha differs before and after a push. The caller raises H6.
type DefaultBranchMovedError struct{ Branch, Before, After string }

func (e *DefaultBranchMovedError) Error() string {
	return fmt.Sprintf("integrate: default branch %s moved during push: %q -> %q", e.Branch, e.Before, e.After)
}

// Halted reports whether pushes were halted by a default-branch change.
func (d *Dir) Halted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.halted
}

// Push pushes the requirement branch with the single explicit refspec
// HEAD:refs/heads/foreman/<req id> (never forced, never the default branch),
// then asserts with ls-remote that the default branch sha is unchanged.
func (d *Dir) Push(ctx context.Context, reqID string) error {
	branch, err := git.RequirementBranch(reqID)
	if err != nil {
		return err
	}
	if branch == d.Default {
		return ErrPushTargetIsDefault
	}
	if d.Halted() {
		return ErrPushHalted
	}
	if _, err := d.r.Run(ctx, d.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("integrate: requirement branch %s not present: %w", branch, err)
	}
	if _, err := d.r.Run(ctx, d.Path, "checkout", "--quiet", "--force", branch); err != nil {
		return err
	}
	pr, err := d.authRunner(ctx)
	if err != nil {
		return err
	}
	before, err := d.remoteDefaultSHA(ctx, pr)
	if err != nil {
		return d.scrub(err)
	}
	if _, err := pr.Run(ctx, d.Path, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return d.scrub(err)
	}
	after, err := d.remoteDefaultSHA(ctx, pr)
	if err != nil {
		return d.scrub(err)
	}
	if before != after {
		d.mu.Lock()
		d.halted = true
		s := d.store
		d.mu.Unlock()
		moved := &DefaultBranchMovedError{Branch: d.Default, Before: before, After: after}
		if s != nil {
			if err := s.SaveHalt(ctx, HaltInfo{Branch: d.Default, Before: before, After: after}); err != nil {
				return errors.Join(moved, fmt.Errorf("integrate: persist push halt: %w", err))
			}
		}
		return moved
	}
	return nil
}

// remoteDefaultSHA returns the remote's default branch sha ("" if absent).
func (d *Dir) remoteDefaultSHA(ctx context.Context, r *git.Runner) (string, error) {
	out, err := r.Line(ctx, d.Path, "ls-remote", "origin", "refs/heads/"+d.Default)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", nil
	}
	return f[0], nil
}
