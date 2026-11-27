package integrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// ErrMergeConflict is wrapped by *ConflictError.
var ErrMergeConflict = errors.New("integrate: merge-back conflict")

// ConflictError reports the files that conflicted while rebasing the squashed
// step onto the requirement branch. The rebase is aborted before returning.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: %s", ErrMergeConflict, strings.Join(e.Files, ", "))
}
func (e *ConflictError) Unwrap() error { return ErrMergeConflict }

// MergeSpec describes one step to merge back.
type MergeSpec struct {
	ReqID   string
	StepID  string
	StepRef string // local ref from FetchFromSession (under refs/foreman/)
	Subject string // commit subject; a Step-Id trailer is appended
}

// MergeResult is the outcome of SquashMergeBack.
type MergeResult struct {
	SHA           string // requirement branch tip after the call
	AlreadyMerged bool   // a commit with this Step-Id trailer already existed
	Empty         bool   // the step introduced nothing; branch unchanged
}

// SquashMergeBack squashes the step's commits into one commit (parent: the
// merge-base with the requirement branch), rebases it onto the requirement
// branch and fast-forwards the branch (state-machine S42 note). It is
// idempotent on the Step-Id trailer.
func (d *Dir) SquashMergeBack(ctx context.Context, s MergeSpec) (MergeResult, error) {
	branch, err := git.RequirementBranch(s.ReqID)
	if err != nil {
		return MergeResult{}, err
	}
	if err := git.ValidateID(s.StepID); err != nil {
		return MergeResult{}, err
	}
	if err := git.ValidateName(s.StepRef); err != nil || !strings.HasPrefix(s.StepRef, StepRefPrefix) {
		return MergeResult{}, git.ErrInvalidName
	}
	if strings.TrimSpace(s.Subject) == "" || strings.HasPrefix(s.Subject, "-") {
		return MergeResult{}, errors.New("integrate: empty or option-like subject")
	}
	if err := d.ensureBranch(ctx, branch); err != nil {
		return MergeResult{}, err
	}
	reqRef := "refs/heads/" + branch
	tip, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", reqRef)
	if err != nil {
		return MergeResult{}, err
	}
	pat := "^Step-Id: " + regexp.QuoteMeta(s.StepID) + "$"
	if prev, err := d.r.Line(ctx, d.Path, "log", "-n1", "--format=%H", "-E", "--grep="+pat, reqRef); err != nil {
		return MergeResult{}, err
	} else if prev != "" {
		return MergeResult{SHA: tip, AlreadyMerged: true}, nil
	}
	mb, err := d.r.Line(ctx, d.Path, "merge-base", reqRef, s.StepRef)
	if err != nil {
		return MergeResult{}, fmt.Errorf("integrate: no merge base between %s and %s: %w", branch, s.StepRef, err)
	}
	stepTree, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", s.StepRef+"^{tree}")
	if err != nil {
		return MergeResult{}, err
	}
	mbTree, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", mb+"^{tree}")
	if err != nil {
		return MergeResult{}, err
	}
	if stepTree == mbTree {
		return MergeResult{SHA: tip, Empty: true}, nil
	}
	squash, err := d.r.Line(ctx, d.Path, "commit-tree", stepTree, "-p", mb, "-m", s.Subject+"\n\nStep-Id: "+s.StepID)
	if err != nil {
		return MergeResult{}, err
	}
	if _, err := d.r.Run(ctx, d.Path, "checkout", "--quiet", "--force", "--detach", squash); err != nil {
		return MergeResult{}, err
	}
	if _, err := d.r.Run(ctx, d.Path, "rebase", "--quiet", reqRef); err != nil {
		files, _ := d.r.Run(ctx, d.Path, "diff", "--name-only", "--diff-filter=U")
		_, _ = d.r.Run(ctx, d.Path, "rebase", "--abort")
		if _, ok := err.(*git.Error); !ok {
			return MergeResult{}, err
		}
		return MergeResult{}, &ConflictError{Files: strings.Fields(files)}
	}
	rebased, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return MergeResult{}, err
	}
	if _, err := d.r.Run(ctx, d.Path, "checkout", "--quiet", "--force", branch); err != nil {
		return MergeResult{}, err
	}
	if rebased == tip {
		return MergeResult{SHA: tip, Empty: true}, nil
	}
	if _, err := d.r.Run(ctx, d.Path, "merge", "--quiet", "--ff-only", rebased); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{SHA: rebased}, nil
}
