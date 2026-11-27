package integrate

import (
	"context"
	"slices"
	"strings"

	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/stage/check"
)

// L1State is what one reading of the L1 check runs of a head means.
type L1State int

const (
	L1Pending L1State = iota // not finished; Event is set only at the deadline
	L1Pass
	L1Fail  // a real failure: Event is l1_fail
	L1Rerun // a transient failure; the failed jobs were rerun, no event (ACR-10)
)

// L1Request is one reading. Checks are the check runs of Head (OP-07).
type L1Request struct {
	Head   string
	PR     int
	Call   ops.Call
	Checks []ops.CheckRun
	// BaseRun is the latest push run of the default branch (OP-08), nil if none.
	BaseRun *ops.Run
	// Reruns is how often L1 was already rerun for Head (E-PR.l1_reruns).
	Reruns int
	// MarkRerun records the rerun for Head before the rerun is requested, so a
	// crash in between cannot lead to a second rerun (at most one per head).
	MarkRerun func(ctx context.Context, head string) error
	// DeadlinePassed: only then does a pending L1 become an event (S38, S48).
	DeadlinePassed bool
}

// L1Result is the outcome of ReadL1.
type L1Result struct {
	State L1State
	Event *model.Event
}

// ReadL1 judges the check runs of Head. A failure is classified before it is
// reported (state-machine reconciler, ACR-10): a cancelled job, a kill
// (exit 137), a job without a failing test, or the same job failing on the
// default branch is rerun once per head with OP-09 and gives no event; only a
// failure that survives the rerun, or is not transient, is l1_fail.
func ReadL1(ctx context.Context, env Env, r L1Request) (L1Result, error) {
	switch verdict(r.Checks) {
	case "pass":
		return L1Result{State: L1Pass, Event: &model.Event{Name: model.EventL1Pass, Head: r.Head, PR: r.PR}}, nil
	case "fail":
		return readFailure(ctx, env, r)
	}
	return L1Result{State: L1Pending, Event: pendingEvent(r)}, nil
}

// pendingEvent follows ACR-24: a run in progress extends the deadline
// (ci_progress); no run at all, or one only queued (the runner state cannot
// be read, so it counts as "no online runner"), goes to deadline_passed.
func pendingEvent(r L1Request) *model.Event {
	if !r.DeadlinePassed {
		return nil
	}
	for _, c := range r.Checks {
		if c.Status == "in_progress" {
			return &model.Event{Name: model.EventCIProgress, PR: r.PR, Head: r.Head}
		}
	}
	return &model.Event{Name: model.EventDeadlinePassed, PR: r.PR, Head: r.Head,
		Detail: "no L1 run in progress for the head"}
}

func readFailure(ctx context.Context, env Env, r L1Request) (L1Result, error) {
	runs, err := env.Ops.RunsList(ctx, r.Call, env.Repo, ops.RunsFilter{HeadSHA: r.Head, PerPage: 30})
	if err != nil {
		return L1Result{}, err
	}
	for _, run := range runs {
		if run.Status != "completed" {
			// A rerun flips the run to queued at once, but the check runs of
			// the old attempt are still listed: that failure is stale.
			return L1Result{State: L1Pending, Event: pendingEvent(r)}, nil
		}
	}
	failed := failedRuns(r.Checks)
	transient, tests, err := classify(ctx, env, r, failed)
	if err != nil {
		return L1Result{}, err
	}
	if transient && r.Reruns == 0 {
		if r.MarkRerun != nil {
			if err := r.MarkRerun(ctx, r.Head); err != nil {
				return L1Result{}, err
			}
		}
		for _, run := range runs {
			if !slices.Contains([]string{"success", "neutral", "skipped"}, run.Conclusion) {
				if err := env.Ops.RunRerunFailed(ctx, r.Call, env.Repo, run.ID); err != nil {
					return L1Result{}, err
				}
			}
		}
		return L1Result{State: L1Rerun}, nil
	}
	names := failedChecks(r.Checks)
	if len(tests) == 0 {
		tests = names
	}
	return L1Result{State: L1Fail, Event: &model.Event{Name: model.EventL1Fail, Head: r.Head, PR: r.PR, FailingTests: tests,
		// Only names go into the finding: job logs are not passed through
		// privguard yet, so none of their text is carried on.
		LogTail: "L1 check runs failed: " + strings.Join(names, ", ")}}, nil
}

func failedRuns(checks []ops.CheckRun) []ops.CheckRun {
	var out []ops.CheckRun
	for _, c := range checks {
		if c.Status == "completed" && !slices.Contains([]string{"success", "neutral", "skipped"}, c.Conclusion) {
			out = append(out, c)
		}
	}
	return out
}

// classify reports whether every failed job looks transient, and the failing
// test names read from the logs.
func classify(ctx context.Context, env Env, r L1Request, failed []ops.CheckRun) (bool, []string, error) {
	baseNames, err := baseFailures(ctx, env, r)
	if err != nil {
		return false, nil, err
	}
	var tests []string
	transient := len(failed) > 0
	for _, c := range failed {
		if c.Conclusion == "cancelled" || slices.Contains(baseNames, c.Name) {
			continue
		}
		log, err := env.Ops.JobLogs(ctx, r.Call, env.Repo, c.ID)
		if err != nil {
			transient = false // cannot tell: report it rather than rerun blindly
			continue
		}
		names := check.FailingTests(string(log))
		tests = append(tests, names...)
		if len(names) > 0 && !killed(string(log)) {
			transient = false
		}
	}
	return transient, tests, nil
}

// killed: the runner killed the job (exit code 137, OOM or cancellation).
func killed(log string) bool {
	return strings.Contains(log, "exit code 137") || strings.Contains(log, "exit status 137")
}

// baseFailures lists the names of the check runs that failed on the latest
// push run of the default branch.
func baseFailures(ctx context.Context, env Env, r L1Request) ([]string, error) {
	if r.BaseRun == nil || r.BaseRun.Conclusion != "failure" || r.BaseRun.HeadSHA == "" {
		return nil, nil
	}
	checks, err := env.Ops.ChecksForRef(ctx, r.Call, env.Repo, r.BaseRun.HeadSHA)
	if err != nil {
		return nil, err
	}
	return failedChecks(checks), nil
}
