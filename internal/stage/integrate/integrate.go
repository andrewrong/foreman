// Package integrate runs the waiting(ci) reconciler action of integrate
// steps (state-machine reconciler: "push 需求分支；按 head 分支查找或创建 PR；
// 轮询 L1"). Every call is idempotent: the push is a plain fast-forward of
// the same branch, the PR is found by its head branch name, the poll only
// reads. Events are values; the caller calls state.Transition (MD-7).
package integrate

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
	"github.com/andrewrong/foreman/internal/workspace/git"
	wsintegrate "github.com/andrewrong/foreman/internal/workspace/integrate"
)

// Env is what the stage needs.
type Env struct {
	Integ *wsintegrate.Dir
	Ops   *ops.Ops
	Repo  ops.Repo
}

// Request is one reconcile of a waiting(ci) integrate step.
type Request struct {
	ReqID, Title, Body string
	PR                 int // known PR number from the pull_requests row, 0 if none yet
	Priority           gate.Priority
	Caller             gate.Caller
	// DeadlinePassed is set when the step's 35m ci deadline has passed: only
	// then do "still running" and "quota wait" become ci_progress / lease_wait
	// events (state-machine dims.counters.ci_extensions).
	DeadlinePassed bool
}

// Result is what the call learned. PR and Head are facts for the
// pull_requests row; Event is nil while L1 is still running.
type Result struct {
	PR    int
	Head  string
	Event *model.Event
}

// Run pushes, finds or creates the PR and polls L1 once.
func Run(ctx context.Context, env Env, r Request) (Result, error) {
	branch, err := git.RequirementBranch(r.ReqID)
	if err != nil {
		return Result{}, err
	}
	if err := env.Integ.Push(ctx, r.ReqID); err != nil {
		return Result{}, fmt.Errorf("integrate: push: %w", err)
	}
	head, err := env.Integ.BranchSHA(ctx, r.ReqID)
	if err != nil {
		return Result{}, err
	}
	res := Result{PR: r.PR, Head: head}
	call := ops.Call{Priority: r.Priority, Caller: r.Caller}

	watch := map[int]bool{}
	if r.PR > 0 {
		watch[r.PR] = true
	}
	poll, err := env.Ops.Poll(ctx, call, ops.PollInput{Repo: env.Repo, BaseBranch: env.Integ.Default, Active: true, Watch: watch})
	if err != nil {
		return res, quotaOrError(err, r, &res)
	}
	var pr *ops.PR
	for i := range poll.Open {
		if poll.Open[i].Head.Ref == branch {
			pr = &poll.Open[i]
		}
	}
	if pr == nil {
		created, err := env.Ops.PRCreate(ctx, call, env.Repo, ops.NewPR{Title: r.Title, Head: branch, Base: env.Integ.Default, Body: r.Body})
		if err != nil {
			return res, quotaOrError(err, r, &res)
		}
		res.PR = created.Number
		return res, nil // L1 starts now; the next poll reads it
	}
	res.PR = pr.Number
	detail, ok := poll.Details[pr.Number]
	if !ok || detail.PR.Head.SHA != head {
		return res, pending(r, &res) // GitHub has not seen our head yet
	}
	switch verdict(detail.Checks) {
	case "pass":
		res.Event = &model.Event{Name: model.EventL1Pass, Head: head, PR: pr.Number}
		return res, nil
	case "fail":
		failed := failedChecks(detail.Checks)
		res.Event = &model.Event{Name: model.EventL1Fail, Head: head, PR: pr.Number, FailingTests: failed,
			LogTail: "L1 check runs failed: " + strings.Join(failed, ", ")}
		return res, nil
	}
	return res, pending(r, &res)
}

// pending is "L1 not finished": silent until the deadline, then ci_progress.
func pending(r Request, res *Result) error {
	if r.DeadlinePassed {
		res.Event = &model.Event{Name: model.EventCIProgress, PR: res.PR}
	}
	return nil
}

// quotaOrError maps a gate wait or a GitHub rate limit to lease_wait (only at
// the deadline, S37); every other error is returned.
func quotaOrError(err error, r Request, res *Result) error {
	var w ops.Waiting
	var rl client.RateLimited
	var sl client.SecondaryLimited
	if errors.As(err, &w) || errors.As(err, &rl) || errors.As(err, &sl) {
		if r.DeadlinePassed {
			res.Event = &model.Event{Name: model.EventLeaseWait, PR: res.PR}
		}
		return nil
	}
	return err
}

// verdict folds the check runs of the head commit: "pass" only when there is
// at least one run and all have completed successfully (zero runs is not a
// pass); "fail" when any completed run failed; otherwise "pending".
func verdict(checks []ops.CheckRun) string {
	if len(checks) == 0 {
		return "pending"
	}
	pass := true
	for _, c := range checks {
		if c.Status != "completed" {
			pass = false
			continue
		}
		if !slices.Contains([]string{"success", "neutral", "skipped"}, c.Conclusion) {
			return "fail"
		}
	}
	if pass {
		return "pass"
	}
	return "pending"
}

func failedChecks(checks []ops.CheckRun) []string {
	var out []string
	for _, c := range checks {
		if c.Status == "completed" && !slices.Contains([]string{"success", "neutral", "skipped"}, c.Conclusion) {
			out = append(out, c.Name)
		}
	}
	return out
}
