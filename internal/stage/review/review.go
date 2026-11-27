// Package review runs the review stage: a reviewer session, then, for an
// approved work step, the merge-back of the step's commits into the
// requirement branch (state-machine reconciler: running(review), reviewer
// result approve). It returns an event value; the caller calls
// state.Transition (MD-7).
package review

import (
	"context"
	"errors"
	"strings"

	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/stage/code"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// Env is what the stage needs besides the request.
type Env struct {
	Session code.Env
	Integ   *integrate.Dir
	Mirror  *session.Mirror
}

// MergeBack asks for the squash merge-back of a work step after approval.
type MergeBack struct {
	ReqID, StepID string
	SessionRepo   string
	SessionBranch string // foreman/<req id> in the session repo
	Subject       string // squash commit subject; the Step-Id trailer is appended
}

// Request is one review Run.
type Request struct {
	Session   code.Request
	MergeBack *MergeBack // nil for integrate steps (S43)
}

// Run executes the reviewer session and maps its verdict to an event.
func Run(ctx context.Context, env Env, req Request) (model.Event, error) {
	res, err := env.Session.RunSession(ctx, req.Session)
	if err != nil {
		return model.Event{}, err
	}
	if res.Parsed == nil {
		return res.Event, nil
	}
	ev := res.Event
	switch {
	case res.Parsed.Status == "spec_conflict":
		ev.Name, ev.Detail = model.EventSpecConflict, res.Parsed.Note
		return ev, nil
	case res.Parsed.Status != "ok":
		ev.Name, ev.Detail = model.EventBadResult, "unsupported result status "+res.Parsed.Status
		return ev, nil
	case res.Parsed.Verdict == "changes":
		ev.Name = model.EventReviewChanges
		if res.Parsed.Findings != nil {
			for _, f := range *res.Parsed.Findings {
				mf := model.Finding{Severity: f.Severity, Problem: f.Problem, CloseWhen: f.CloseWhen}
				if f.Location != nil {
					mf.Path, mf.Line = f.Location.Path, f.Location.Line
				}
				ev.Findings = append(ev.Findings, mf)
			}
		}
		return ev, nil
	}
	if req.MergeBack == nil {
		ev.Name = model.EventReviewApprove
		return ev, nil
	}
	merged, err := mergeBack(ctx, env, *req.MergeBack)
	if err != nil {
		var ce *integrate.ConflictError
		if errors.As(err, &ce) {
			ev.Name, ev.Detail = model.EventMergeBackConfl, strings.Join(ce.Files, ", ")
			return ev, nil
		}
		return model.Event{}, err
	}
	ev.Name, ev.Head = model.EventReviewApprove, merged
	return ev, nil
}

// mergeBack refreshes the mirror view (Mirror.Update, then SyncFromMirror,
// in that order), fetches the step's head from the session repo with the
// hardened fetch and squashes it onto the requirement branch. SquashMergeBack
// is idempotent on the Step-Id trailer, so a repeat after a crash is a no-op.
func mergeBack(ctx context.Context, env Env, m MergeBack) (string, error) {
	if err := env.Mirror.Update(ctx); err != nil {
		return "", err
	}
	if err := env.Integ.SyncFromMirror(ctx); err != nil {
		return "", err
	}
	ref := integrate.StepRefPrefix + "steps/" + m.StepID
	if _, err := env.Integ.FetchFromSession(ctx, m.SessionRepo, m.SessionBranch, ref); err != nil {
		return "", err
	}
	res, err := env.Integ.SquashMergeBack(ctx, integrate.MergeSpec{ReqID: m.ReqID, StepID: m.StepID, StepRef: ref, Subject: m.Subject})
	if err != nil {
		return "", err
	}
	return res.SHA, nil
}
