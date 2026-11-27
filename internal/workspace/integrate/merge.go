package integrate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

var stepHeadRE = regexp.MustCompile(`(?m)^Step-Head: ([0-9a-f]{40})$`)

// lastMergedHead returns the newest session head recorded on the requirement
// branch (Step-Head trailer) that is an ancestor of stepRef, "" if none.
func (d *Dir) lastMergedHead(ctx context.Context, reqRef, stepRef string) (string, error) {
	out, err := d.r.Run(ctx, d.Path, "log", "--format=%B%x00", "-E", "--grep=^Step-Head: [0-9a-f]{40}$", reqRef)
	if err != nil {
		return "", err
	}
	for _, m := range stepHeadRE.FindAllStringSubmatch(out, -1) {
		if _, err := d.r.Run(ctx, d.Path, "merge-base", "--is-ancestor", m[1], stepRef); err == nil {
			return m[1], nil
		}
	}
	return "", nil
}

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

// SquashMergeBack squashes the step's commits into one commit, rebases it onto
// the requirement branch and fast-forwards the branch (state-machine S42
// note). It is idempotent on the Step-Id trailer.
//
// The squash starts where the session branch was when an earlier step of the
// requirement was merged back: that step's session head is recorded in a
// Step-Head trailer, and the newest recorded head that is an ancestor of this
// step's head is the parent of the squash. Without it the session branch's
// whole history since the merge base would be squashed again and collide with
// the already merged earlier steps (add/add on every file they created).
// With no earlier step the parent is the merge-base with the requirement
// branch.
func (d *Dir) SquashMergeBack(ctx context.Context, s MergeSpec) (MergeResult, error) {
	branch, err := git.RequirementBranch(s.ReqID)
	if err != nil {
		return MergeResult{}, err
	}
	if err := git.ValidateStepID(s.StepID); err != nil {
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
	if prev, err := d.lastMergedHead(ctx, reqRef, s.StepRef); err != nil {
		return MergeResult{}, err
	} else if prev != "" {
		mb = prev
	}
	stepHead, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", s.StepRef+"^{commit}")
	if err != nil {
		return MergeResult{}, err
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
	squash, err := d.r.Line(ctx, d.Path, "commit-tree", stepTree, "-p", mb, "-m", s.Subject+"\n\nStep-Head: "+stepHead+"\nStep-Id: "+s.StepID)
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
