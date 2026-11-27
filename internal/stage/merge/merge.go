// Package merge runs the running(merge) reconciler action: merge the pull
// request with the head sha pinned (state-machine reconciler). The merge
// slot and base-health lease were taken by the scheduler at dispatch (S08)
// and are held by it until this returns an event. The result is an event
// value (MD-7).
//
// M1 scope: update-branch, waiting for L1 after it, the guardrail check
// (S47) and needs_rereview (S46) are not implemented; the head the PR had at
// l1_pass is merged as is.
package merge

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// Method is the GitHub merge method. D-042 fixes one PR per requirement but
// not the method; squash gives the default branch one commit per requirement.
const Method = "squash"

// Env is what the stage needs.
type Env struct {
	Ops  *ops.Ops
	Repo ops.Repo
}

// Request is one merge attempt.
type Request struct {
	PR       int
	Head     string // the sha L1 passed on; pinned in the merge call
	Priority gate.Priority
	Caller   gate.Caller
}

// Run merges the PR. It returns merged (also when the PR was already merged:
// idempotent), merge_conflict for GitHub's 409/405, or a nil event when the
// gate asks to wait; the scheduler then keeps the lease and tries again.
func Run(ctx context.Context, env Env, r Request) (*model.Event, error) {
	call := ops.Call{Priority: r.Priority, Caller: r.Caller}
	pr, err := env.Ops.PRGet(ctx, call, env.Repo, r.PR)
	if err != nil {
		return waitOrError(err)
	}
	if pr.Merged {
		return &model.Event{Name: model.EventMerged, PR: r.PR, Head: r.Head}, nil
	}
	if pr.State != "open" {
		return nil, fmt.Errorf("merge: PR %d is %s and not merged", r.PR, pr.State)
	}
	res, err := env.Ops.PRMerge(ctx, call, env.Repo, r.PR, r.Head, Method)
	if err != nil {
		var c client.Conflict
		if errors.As(err, &c) {
			return &model.Event{Name: model.EventMergeConflict, PR: r.PR, Detail: c.Message}, nil
		}
		return waitOrError(err)
	}
	if !res.Merged {
		return nil, fmt.Errorf("merge: PR %d not merged: %s", r.PR, res.Message)
	}
	return &model.Event{Name: model.EventMerged, PR: r.PR, Head: res.SHA}, nil
}

func waitOrError(err error) (*model.Event, error) {
	var w ops.Waiting
	var rl client.RateLimited
	var sl client.SecondaryLimited
	if errors.As(err, &w) || errors.As(err, &rl) || errors.As(err, &sl) {
		return nil, nil
	}
	return nil, err
}
