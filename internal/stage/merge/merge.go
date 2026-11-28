// Package merge runs the running(merge) reconciler action (state-machine
// reconciler): bring the pull request up to date with the default branch,
// wait for L1 on the new head, check the guardrails (S47) and merge with the
// head sha pinned. The merge slot and base-health lease were taken by the
// scheduler at dispatch (S08) and are held by it until this returns an event
// that leaves the state. The result is an event value (MD-7).
package merge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
	wsintegrate "github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// Method is the GitHub merge method. D-042 fixes one PR per requirement but
// not the method; squash gives the default branch one commit per requirement.
const Method = "squash"

// Env is what the stage needs. Integ and Mirror are the local views used to
// decide "is the PR behind the base" and "which files did each side change"
// without asking GitHub.
type Env struct {
	Ops    *ops.Ops
	Repo   ops.Repo
	Integ  *wsintegrate.Dir
	Mirror *session.Mirror
	// Guardrails is the project's path list (spec/guardrails.yaml, D-063).
	Guardrails []model.Guardrail
}

// Request is one merge attempt.
type Request struct {
	ReqID    string
	PR       int
	Head     string // head sha foreman last knew (E-PR.head_sha)
	Priority gate.Priority
	Caller   gate.Caller
	// LastReviewed is E-PR.last_reviewed_sha: the head a reviewer (or the
	// single-step shortcut, S29) last accepted.
	LastReviewed string
	// GuardrailApproved is E-PR.guardrail_approved_sha (H21).
	GuardrailApproved string
	// Reruns, MarkRerun: the L1 rerun bookkeeping of E-PR.l1_reruns (head sha
	// -> count).
	Reruns    map[string]int
	MarkRerun func(ctx context.Context, head string) error
	// DeadlinePassed: only then do waiting for L1 or for quota become
	// ci_progress / lease_wait (S48, S45).
	DeadlinePassed bool
}

// Result carries what the call learned. Head is the PR head GitHub reports
// now; the caller stores it in E-PR (it changes after update-branch, and the
// merge and S43's last_reviewed_sha use it).
type Result struct {
	Event *model.Event
	Head  string
}

// Run advances the merge path of the pull request by one step:
//
//  1. already merged: merged (idempotent);
//  2. the PR is behind the default branch: update-branch, then wait;
//  3. the head changed since it was last reviewed and base changes since then
//     touch files of the PR: needs_rereview (S46); disjoint changes only need
//     L1 on the new head, which step 4 waits for (ACR-02);
//  4. L1 must pass on the head being merged; transient failures are rerun
//     once, a real failure is l1_fail (S60);
//  5. the real diff matches a guardrail the owner has not approved for this
//     head: guardrail_hit (S47);
//  6. merge with the head pinned.
//
// A nil event means "nothing to report yet"; the scheduler keeps the lease and
// tries again.
func Run(ctx context.Context, env Env, r Request) (Result, error) {
	call := ops.Call{Priority: r.Priority, Caller: r.Caller}
	pr, err := env.Ops.PRGet(ctx, call, env.Repo, r.PR)
	if err != nil {
		ev, err := waitOrError(err, r)
		return Result{Event: ev, Head: r.Head}, err
	}
	res := Result{Head: pr.Head.SHA}
	if pr.Merged {
		res.Event = &model.Event{Name: model.EventMerged, PR: r.PR, Head: r.Head}
		return res, nil
	}
	if pr.State != "open" {
		return res, fmt.Errorf("merge: PR %d is %s and not merged", r.PR, pr.State)
	}
	ev, ok, err := guarded(ctx, env, r, call, pr)
	if err != nil {
		res.Event, err = waitOrError(err, r)
		return res, err
	}
	if !ok {
		res.Event = ev
		return res, nil
	}
	res.Event, err = merge(ctx, env, call, r, pr.Head.SHA)
	return res, err
}

// guarded runs steps 2 to 5. ok means every guard passed and the PR may be
// merged; otherwise the event (possibly nil) is what to report.
func guarded(ctx context.Context, env Env, r Request, call ops.Call, pr ops.PR) (ev *model.Event, ok bool, err error) {
	head := pr.Head.SHA
	if err := syncLocal(ctx, env, r.ReqID, head); err != nil {
		return nil, false, err
	}
	behind, err := env.Integ.BehindBase(ctx, head)
	if err != nil {
		return nil, false, err
	}
	if behind {
		if _, err := env.Ops.PRUpdateBranch(ctx, call, env.Repo, r.PR, head); err != nil {
			var c client.Conflict
			if errors.As(err, &c) {
				return &model.Event{Name: model.EventMergeConflict, PR: r.PR, Head: head, Detail: c.Message}, false, nil
			}
			return nil, false, err
		}
		return nil, false, nil // GitHub moves the head asynchronously: look again next Tick
	}
	if ev, err := rereview(ctx, env, r, head); err != nil || ev != nil {
		return ev, false, err
	}
	if ev, ok, err := l1(ctx, env, r, call, head); err != nil || !ok {
		return ev, false, err
	}
	files, err := env.Integ.PRFiles(ctx, head)
	if err != nil {
		return nil, false, err
	}
	if hits := model.Hits(env.Guardrails, files); len(hits) > 0 && r.GuardrailApproved != head {
		return &model.Event{Name: model.EventGuardrailHit, PR: r.PR, Head: head,
			Detail: "the diff touches guardrail paths: " + strings.Join(hits, ", ")}, false, nil
	}
	return nil, true, nil
}

// syncLocal refreshes the local views of both sides: the default branch
// (Mirror.Update, then SyncFromMirror, the order T10a requires) and, when
// GitHub's head is not what foreman has locally (update-branch added a merge
// commit), the requirement branch.
func syncLocal(ctx context.Context, env Env, reqID, head string) error {
	if err := env.Mirror.Update(ctx); err != nil {
		return err
	}
	if err := env.Integ.SyncFromMirror(ctx); err != nil {
		return err
	}
	local, err := env.Integ.BranchSHA(ctx, reqID)
	if err != nil {
		return err
	}
	if local == head {
		return nil
	}
	if _, err := env.Integ.AdoptRemote(ctx, reqID); err != nil {
		return err
	}
	return nil
}

// rereview is S46: the head moved since the last review and the default
// branch changed files the PR also changes.
func rereview(ctx context.Context, env Env, r Request, head string) (*model.Event, error) {
	if r.LastReviewed == "" || r.LastReviewed == head {
		return nil, nil
	}
	base, err := env.Integ.BaseFilesSince(ctx, r.LastReviewed)
	if err != nil {
		return nil, err
	}
	mine, err := env.Integ.PRFiles(ctx, r.LastReviewed)
	if err != nil {
		return nil, err
	}
	common := slices.DeleteFunc(slices.Clone(mine), func(f string) bool { return !slices.Contains(base, f) })
	if len(common) == 0 {
		return nil, nil
	}
	return &model.Event{Name: model.EventNeedsRereview, PR: r.PR, Head: head,
		Detail: "base changed files of the PR since the last review: " + strings.Join(common, ", ")}, nil
}

// l1 reads L1 on the head. ok is true only on a pass.
func l1(ctx context.Context, env Env, r Request, call ops.Call, head string) (*model.Event, bool, error) {
	poll, err := env.Ops.Poll(ctx, call, ops.PollInput{Repo: env.Repo, BaseBranch: env.Integ.Default, Active: true,
		Watch: map[int]bool{r.PR: true}})
	if err != nil {
		return nil, false, err
	}
	detail, ok := poll.Details[r.PR]
	if !ok || detail.PR.Head.SHA != head {
		return pendingAtDeadline(r), false, nil
	}
	got, err := ctxmd.ReadL1(ctx, ctxmd.L1Env{Ops: env.Ops, Repo: env.Repo}, ctxmd.L1Request{
		Head: head, PR: r.PR, Call: call, Checks: detail.Checks, BaseRun: poll.BaseRun, Reruns: r.Reruns[head],
		MarkRerun: r.MarkRerun, DeadlinePassed: r.DeadlinePassed})
	if err != nil {
		return nil, false, err
	}
	switch got.State {
	case ctxmd.L1Pass:
		return nil, true, nil
	case ctxmd.L1Fail:
		return got.Event, false, nil
	case ctxmd.L1Pending:
		return pendingAtDeadline(r), false, nil
	}
	return nil, false, nil // rerun requested
}

// pendingAtDeadline: waiting for L1 in running(merge) has one row, ci_progress
// (S48), whatever the reason; it is sent at the deadline only.
func pendingAtDeadline(r Request) *model.Event {
	if !r.DeadlinePassed {
		return nil
	}
	return &model.Event{Name: model.EventCIProgress, PR: r.PR}
}

// Merge performs the merge call alone: the head sha is pinned, a merged PR
// is reported as merged again, GitHub's 409/405 is merge_conflict and a gate
// wait is a nil event.
func Merge(ctx context.Context, env Env, call ops.Call, pr int, head string) (*model.Event, error) {
	p, err := env.Ops.PRGet(ctx, call, env.Repo, pr)
	if err != nil {
		return waitOrError(err, Request{})
	}
	if p.Merged {
		return &model.Event{Name: model.EventMerged, PR: pr, Head: head}, nil
	}
	if p.State != "open" {
		return nil, fmt.Errorf("merge: PR %d is %s and not merged", pr, p.State)
	}
	return merge(ctx, env, call, Request{PR: pr}, head)
}

func merge(ctx context.Context, env Env, call ops.Call, r Request, head string) (*model.Event, error) {
	pr := r.PR
	res, err := env.Ops.PRMerge(ctx, call, env.Repo, pr, head, Method)
	if err != nil {
		var c client.Conflict
		if errors.As(err, &c) {
			return &model.Event{Name: model.EventMergeConflict, PR: pr, Detail: c.Message}, nil
		}
		return waitOrError(err, r)
	}
	if !res.Merged {
		return nil, fmt.Errorf("merge: PR %d not merged: %s", pr, res.Message)
	}
	return &model.Event{Name: model.EventMerged, PR: pr, Head: res.SHA}, nil
}

// waitOrError maps a gate wait or a GitHub rate limit to "try again" (a nil
// event), or lease_wait once the deadline has passed (S45); other errors are
// returned.
func waitOrError(err error, r Request) (*model.Event, error) {
	var w ops.Waiting
	var rl client.RateLimited
	var sl client.SecondaryLimited
	if errors.As(err, &w) || errors.As(err, &rl) || errors.As(err, &sl) {
		if r.DeadlinePassed {
			return &model.Event{Name: model.EventLeaseWait, PR: r.PR}, nil
		}
		return nil, nil
	}
	return nil, err
}
