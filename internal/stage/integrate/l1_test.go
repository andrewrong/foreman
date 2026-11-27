package integrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
)

const rerunPath = "POST /repos/acme/widgets/actions/runs/"

func (w *world) reruns() int {
	n := 0
	for _, r := range w.gh.requests {
		if strings.HasPrefix(r, rerunPath) && strings.HasSuffix(r, "/rerun-failed-jobs") {
			n++
		}
	}
	return n
}

func (w *world) l1(checks []ops.CheckRun, reruns int, marked *[]string) L1Request {
	return L1Request{Head: w.headSHA, PR: 7, Call: ops.Call{Priority: gate.P2}, Checks: checks, Reruns: reruns,
		MarkRerun: func(_ context.Context, head string) error { *marked = append(*marked, head); return nil }}
}

func failedJob(id int64, name, conclusion string) ops.CheckRun {
	return ops.CheckRun{ID: id, Name: name, Status: "completed", Conclusion: conclusion}
}

// test:l1-failure-classification (ACR-10): transient failures are rerun once
// per head without an event; real ones, and a failure that survives the rerun,
// are l1_fail.
func TestL1FailureClassification(t *testing.T) {
	ctx := context.Background()
	failedRun := []ops.Run{{ID: 41, Status: "completed", Conclusion: "failure", HeadSHA: "h"}}
	const realLog = "=== RUN TestSum\n--- FAIL: TestSum (0.00s)\nFAIL\n"
	cases := []struct {
		name       string
		checks     []ops.CheckRun
		logs       map[int64]string
		base       bool
		wantState  L1State
		wantReruns int
		wantTests  []string
	}{
		{"cancelled job", []ops.CheckRun{failedJob(1, "ci", "cancelled")}, nil, false, L1Rerun, 1, nil},
		{"no failing test in the log", []ops.CheckRun{failedJob(1, "ci", "failure")}, map[int64]string{1: "npm ERR! network\n"}, false, L1Rerun, 1, nil},
		{"killed by the runner", []ops.CheckRun{failedJob(1, "ci", "failure")},
			map[int64]string{1: "--- FAIL: TestSum\nProcess completed with exit code 137.\n"}, false, L1Rerun, 1, nil},
		{"same failure on the base", []ops.CheckRun{failedJob(1, "ci", "failure")}, map[int64]string{1: realLog}, true, L1Rerun, 1, nil},
		{"real failing test", []ops.CheckRun{failedJob(1, "ci", "failure")}, map[int64]string{1: realLog}, false, L1Fail, 0, []string{"TestSum"}},
		{"log text is redacted before names are read", []ops.CheckRun{failedJob(1, "ci", "failure")},
			map[int64]string{1: "--- FAIL: TestSum/zed" + "@corp." + "io\n"}, false, L1Fail, 0, []string{"TestSum/[redacted:email]"}},
		{"unreadable log is not guessed", []ops.CheckRun{failedJob(1, "ci", "failure")}, nil, false, L1Fail, 0, []string{"ci"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.gh.runs, w.gh.logs = failedRun, c.logs
			req := w.l1(c.checks, 0, new([]string))
			marked := []string{}
			req.MarkRerun = func(_ context.Context, head string) error { marked = append(marked, head); return nil }
			if c.base {
				w.gh.baseRun = &ops.Run{ID: 9, Status: "completed", Conclusion: "failure", Event: "push", HeadBranch: "main", HeadSHA: "basesha"}
				w.gh.baseChecks = []ops.CheckRun{failedJob(2, "ci", "failure")}
				req.BaseRun = w.gh.baseRun
			}
			res, err := ReadL1(ctx, w.env, req)
			if err != nil || res.State != c.wantState {
				t.Fatalf("state %v err %v, want %v", res.State, err, c.wantState)
			}
			if w.reruns() != c.wantReruns || len(marked) != c.wantReruns {
				t.Errorf("reruns %d, marked %v, want %d", w.reruns(), marked, c.wantReruns)
			}
			if c.wantState == L1Rerun {
				if res.Event != nil || marked[0] != w.headSHA {
					t.Errorf("a rerun must give no event and record the head: %+v %v", res.Event, marked)
				}
				return
			}
			if res.Event == nil || res.Event.Name != model.EventL1Fail || !slices.Equal(res.Event.FailingTests, c.wantTests) {
				t.Errorf("event %+v, want l1_fail with %v", res.Event, c.wantTests)
			}
		})
	}
}

// The second failure of a head is l1_fail, never a second rerun; and the
// stale failure still listed right after a rerun (the run is queued again) is
// not read as the rerun's result.
func TestL1RerunOncePerHeadAndStaleFailure(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	checks := []ops.CheckRun{failedJob(1, "ci", "cancelled")}
	w.gh.runs = []ops.Run{{ID: 41, Status: "completed", Conclusion: "cancelled"}}
	var marked []string

	res, err := ReadL1(ctx, w.env, w.l1(checks, 0, &marked))
	if err != nil || res.State != L1Rerun || w.reruns() != 1 {
		t.Fatalf("first failure: %v %v, reruns %d", res.State, err, w.reruns())
	}
	// Right after the rerun the old check run is still listed but the workflow
	// run is queued: pending, no event, no further rerun.
	w.gh.runs = []ops.Run{{ID: 41, Status: "queued"}}
	res, err = ReadL1(ctx, w.env, w.l1(checks, 1, &marked))
	if err != nil || res.State != L1Pending || res.Event != nil || w.reruns() != 1 {
		t.Fatalf("stale failure: %v %+v %v, reruns %d", res.State, res.Event, err, w.reruns())
	}
	// The rerun finished and failed again: now it is a real l1_fail.
	w.gh.runs = []ops.Run{{ID: 41, Status: "completed", Conclusion: "cancelled"}}
	res, err = ReadL1(ctx, w.env, w.l1(checks, 1, &marked))
	if err != nil || res.State != L1Fail || res.Event == nil || res.Event.Name != model.EventL1Fail {
		t.Fatalf("second failure: %v %+v %v", res.State, res.Event, err)
	}
	if w.reruns() != 1 || len(marked) != 1 {
		t.Errorf("reruns %d, marked %v: want exactly one per head", w.reruns(), marked)
	}
}

// ACR-24: only at the deadline does pending become an event, and only a run in
// progress extends it.
func TestL1PendingEventsOnlyAtDeadline(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var marked []string
	inProgress := []ops.CheckRun{{ID: 1, Name: "ci", Status: "in_progress"}}
	queued := []ops.CheckRun{{ID: 1, Name: "ci", Status: "queued"}}
	for _, c := range []struct {
		name   string
		checks []ops.CheckRun
		late   bool
		want   string
	}{
		{"in progress before the deadline", inProgress, false, ""},
		{"in progress at the deadline", inProgress, true, model.EventCIProgress},
		{"queued at the deadline", queued, true, model.EventDeadlinePassed},
		{"no run at the deadline", nil, true, model.EventDeadlinePassed},
	} {
		req := w.l1(c.checks, 0, &marked)
		req.DeadlinePassed = c.late
		res, err := ReadL1(ctx, w.env, req)
		got := ""
		if res.Event != nil {
			got = res.Event.Name
		}
		if err != nil || res.State != L1Pending || got != c.want {
			t.Errorf("%s: state %v event %q err %v, want pending/%q", c.name, res.State, got, err, c.want)
		}
	}
}
