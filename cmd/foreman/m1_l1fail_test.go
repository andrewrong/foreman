package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const sumDocGo = "package sum\n\n// Sum adds two ints; the integration fix documents it.\nfunc Sum(a, b int) int { return a + b }\n"

// L1 failure is a real failing test: reconciler classification says real,
// l1_fail -> S31 adds the check finding, a fix step (type fix) and parks the
// integrate step on waiting(fix). The fix step runs code/check/review with the
// finding in its context and edits a file step 1 created, merges back (the
// requirement branch now has two commits), and S35 sends the integrate step
// through L1 again on the new head; with a fix step the requirement is no
// longer single-step, so the pass goes through integrate review (S30, S43)
// before the merge.
func TestM1L1FailBecomesFixStep(t *testing.T) {
	s := newScenario(t)
	failHead := ""
	s.gh.plan = func(head string, _ int) ciOutcome {
		if failHead == "" {
			failHead = head // the first head fails, later heads pass
		}
		if head == failHead {
			return ciOutcome{Conclusion: "failure", Log: "=== RUN   TestSumIntegration\n--- FAIL: TestSumIntegration (0.00s)\nFAIL\n"}
		}
		return ciOutcome{Conclusion: "success"}
	}
	s.q.push("coder", coderCommits("implement Sum", okFiles()))
	s.atStep(step2, "waiting", "check", "fix", 80)
	firstHead := s.remoteHead()

	fix := reqID + "/S3"
	var typ, fixFor, acc, goal string
	query(s.t, s.app, `SELECT type, COALESCE(fix_for, ''), acceptance, goal FROM steps WHERE id = '`+fix+`'`, &typ, &fixFor, &acc, &goal)
	if typ != "fix" || fixFor != step2 {
		t.Fatalf("fix step: type %s fix_for %s", typ, fixFor)
	}
	var ids []string
	if err := json.Unmarshal([]byte(acc), &ids); err != nil || len(ids) != 1 {
		t.Fatalf("fix step acceptance %q", acc)
	}
	var kind, scope, tests, status string
	query(t, s.app, `SELECT kind, scope, failing_tests, status FROM findings WHERE id = '`+ids[0]+`'`, &kind, &scope, &tests, &status)
	if kind != "check" || scope != step2 || tests != `["TestSumIntegration"]` || status != "open" {
		t.Errorf("finding %s: kind %s scope %s tests %s status %s", ids[0], kind, scope, tests, status)
	}
	if n := s.reqCounter("fix_steps"); n != 1 {
		t.Errorf("fix_steps %d, want 1", n)
	}
	if s.gh.reruns() != 0 {
		t.Errorf("a real failure was rerun %d times", s.gh.reruns())
	}

	s.q.push("coder", coderFor("document Sum", map[string]string{"sum.go": sumDocGo}, ids[0]))
	s.finish()
	s.wantRows("S04", "S40", "S05", "S41", "S06", "S42", "S10", "S07", "S31",
		"S04", "S40", "S05", "S41", "S06", "S42", "S35", "S07", "S30", "S06", "S43", "S08", "S44")
	if s.count("S29") != 0 || s.count("S60") != 0 {
		t.Errorf("rows %v: a many-step requirement took the single-step path", s.rows)
	}
	if n := s.reqCounter("fix_steps"); n != 1 {
		t.Errorf("fix_steps %d after the fix, want 1", n)
	}

	// The fix coder's context carries the finding: acceptance line and the
	// integrate step's check failure as "Previous failure".
	coders := s.runsOf("coder")
	if len(coders) != 2 {
		t.Fatalf("coder runs %+v", coders)
	}
	ctx := s.contextOf(coders[1].ID)
	cur := ctx["Current step "+fix]
	if !strings.Contains(cur, ids[0]) || !strings.Contains(cur, "check failed: TestSumIntegration") {
		t.Errorf("fix step section %q lacks the finding", cur)
	}
	if prev := ctx["Previous failure"]; !strings.Contains(prev, "Failing tests: TestSumIntegration") {
		t.Errorf("Previous failure %q", prev)
	}

	// The PR head moved with the fix; the merge pinned the final head, L1 ran on
	// both heads, and the requirement branch holds both step commits.
	finalHead := s.remoteHead()
	if finalHead == firstHead || s.pr("head_sha") != finalHead {
		t.Errorf("heads: first %s final %s row %s", firstHead, finalHead, s.pr("head_sha"))
	}
	if len(s.gh.created) != 1 || len(s.gh.merges) != 1 || s.gh.merges[0]["sha"] != finalHead {
		t.Errorf("created %d, merges %+v, want one PR and a merge pinned to %s", len(s.gh.created), s.gh.merges, finalHead)
	}
	if n, _ := s.f.git.Line(context.Background(), s.f.remote, "rev-list", "--count", "refs/heads/main..refs/heads/foreman/"+reqID); n != "2" {
		t.Errorf("requirement branch has %s commits beyond main, want 2 (step and fix)", n)
	}
	var fs string
	query(t, s.app, `SELECT status FROM findings WHERE id = '`+ids[0]+`'`, &fs)
	if fs != "closed" {
		t.Errorf("integrate step's check finding %s is %s after L1 passed again (S30), want closed (implicit write of l1_pass)", ids[0], fs)
	}
}

// Transient L1 failures are rerun through OP-09 without an event, once per
// head (ACR-10). Cancelled job then green: no l1_fail ever; the single-step
// shortcut (S29) goes on to the merge.
func TestM1L1ClassificationRerunsCancelledJob(t *testing.T) {
	s := newScenario(t)
	s.gh.plan = func(_ string, attempt int) ciOutcome {
		if attempt == 0 {
			return ciOutcome{Conclusion: "cancelled"}
		}
		return ciOutcome{Conclusion: "success"}
	}
	s.finish()
	s.wantRows("S07", "S29", "S08", "S44")
	if s.count("S31") != 0 || s.count("S32") != 0 {
		t.Errorf("rows %v: a cancelled job reached l1_fail", s.rows)
	}
	if n := s.gh.reruns(); n != 1 {
		t.Errorf("reruns %d, want exactly 1", n)
	}
	if got := s.pr("l1_reruns"); got != `{"`+s.remoteHead()+`":1}` {
		t.Errorf("l1_reruns %s, want one rerun of the head", got)
	}
}

// A failed job whose log shows no failing test is transient too.
func TestM1L1ClassificationNoFailingTestIsRerun(t *testing.T) {
	s := newScenario(t)
	s.gh.plan = func(_ string, attempt int) ciOutcome {
		if attempt == 0 {
			return ciOutcome{Conclusion: "failure", Log: "Error: the operation was canceled. runner lost communication\n"}
		}
		return ciOutcome{Conclusion: "success"}
	}
	s.finish()
	s.wantRows("S07", "S29", "S08", "S44")
	if s.count("S31") != 0 || s.gh.reruns() != 1 {
		t.Errorf("rows %v, reruns %d: want one rerun and no l1_fail", s.rows, s.gh.reruns())
	}
}

// The second failure of a head is l1_fail: exactly one rerun was made, and the
// l1_fail follows the rerun's own result, not the stale failure the fake
// keeps listing for one poll after the rerun.
func TestM1L1SecondFailureIsL1Fail(t *testing.T) {
	s := newScenario(t)
	s.gh.plan = func(string, int) ciOutcome { return ciOutcome{Conclusion: "cancelled"} }
	s.atStep(step2, "waiting", "check", "fix", 80)
	s.wantRows("S07", "S31")
	if n := s.gh.reruns(); n != 1 {
		t.Errorf("reruns %d, want exactly 1 for the head", n)
	}
	head := s.remoteHead()
	s.gh.set(func() {
		if h := s.gh.heads[head]; h.attempt != 1 || h.phase != 1 {
			t.Errorf("l1_fail came at attempt %d phase %d: not after the rerun finished", h.attempt, h.phase)
		}
	})
	if s.count("S31") != 1 || s.reqCounter("fix_steps") != 1 {
		t.Errorf("S31 hit %d times, fix_steps %d", s.count("S31"), s.reqCounter("fix_steps"))
	}
}

// ci_progress extends the 35m deadline only when it passes (S38, ACR-24: a run
// in progress), never at each poll; the Run stays; at the cap (12) the next
// deadline holds the requirement for the owner (S38b, H7).
func TestM1CIProgressExtendsDeadlineOnlyAtDeadline(t *testing.T) {
	s := newScenario(t)
	s.gh.pending = true
	s.atStep(step2, "waiting", "check", "ci", 80)
	for i := 1; i <= 12; i++ {
		deadline := s.deadlineOf(step2)
		s.clk.Advance(deadline.Add(-5 * time.Second).Sub(s.clk.Now()))
		s.tick() // polls just before the deadline: nothing happens
		s.tick()
		if n := s.count("S38"); n != i-1 {
			t.Fatalf("round %d: S38 hit %d times before the deadline", i, n)
		}
		s.clk.Advance(5 * time.Second)
		s.tick()
		s.tick()
		if n := s.count("S38"); n != i {
			t.Fatalf("round %d: S38 hit %d times after the deadline, want %d (once per deadline)", i, n, i)
		}
		if got := s.counter(step2, "ci_extensions"); got != i {
			t.Fatalf("ci_extensions %d, want %d", got, i)
		}
		if !s.deadlineOf(step2).After(deadline) {
			t.Fatalf("round %d: deadline not extended", i)
		}
		if st, _, why := s.stepPos(step2); st != "waiting" || why != "ci" {
			t.Fatalf("round %d: step %s/%s", i, st, why)
		}
	}
	// The ci_watch Run lives on across extensions (same Run, never ended).
	if runs := s.runsOf("ci_watch"); len(runs) != 1 || runs[0].Status == "ended" {
		t.Errorf("ci_watch runs %+v: the Run must continue across extensions", runs)
	}
	s.clk.Advance(36 * time.Minute)
	s.tick()
	s.wantRows("S07", "S38", "S38b")
	if st, _, why := s.stepPos(step2); st != "waiting" || why != "hold" {
		t.Errorf("step %s/%s at the cap, want waiting/hold", st, why)
	}
	if got := s.openAsks(); len(got) != 1 || got[0] != "H7 "+step2 {
		t.Errorf("open asks %v, want one H7 about the integrate step", got)
	}
	if h := s.hold(); h != "owner" {
		t.Errorf("hold %s, want owner", h)
	}
}

// lease_wait: the poll hits the GitHub rate limit; the step stays silent until
// its deadline, then the deadline is extended (S37, not an infra count) and
// the Run goes on.
func TestM1LeaseWaitExtendsDeadlineOnlyAtDeadline(t *testing.T) {
	s := newScenario(t)
	s.atStep(step2, "waiting", "check", "ci", 80)
	s.gh.set(func() { s.gh.limited, s.gh.resetUnix = true, s.clk.Now().Unix() })
	for i := 0; i < 5; i++ {
		s.tick()
	}
	if s.count("S37") != 0 || s.count("S38") != 0 {
		t.Fatalf("rows %v: deadline events before the deadline", s.rows)
	}
	deadline := s.deadlineOf(step2)
	s.clk.Advance(deadline.Add(time.Second).Sub(s.clk.Now()))
	s.tick()
	s.tick()
	if s.count("S37") != 1 {
		t.Fatalf("rows %v: want exactly one S37 at the deadline", s.rows)
	}
	if got := s.counter(step2, "ci_extensions"); got != 1 {
		t.Errorf("ci_extensions %d, want 1", got)
	}
	if got := s.counter(step2, "infra"); got != 0 {
		t.Errorf("infra counter %d: a quota wait must not count as infra", got)
	}
	if runs := s.runsOf("ci_watch"); len(runs) != 1 || runs[0].Status == "ended" {
		t.Errorf("ci_watch runs %+v", runs)
	}
	if !s.deadlineOf(step2).After(deadline) {
		t.Error("deadline not extended")
	}
}
