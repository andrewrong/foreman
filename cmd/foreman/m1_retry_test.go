package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	brokenTst = "package sum\n\nimport \"testing\"\n\nfunc TestSumBroken(t *testing.T) {\n\tt.Fatal(\"Sum is not finished\")\n}\n"
	extraGo   = "package sum\n\n// Add3 adds three ints.\nfunc Add3(a, b, c int) int { return Sum(Sum(a, b), c) }\n"
)

func okFiles() map[string]string {
	return map[string]string{"sum.go": sumGo, "sum_test.go": sumTst}
}

// finish ticks until every step is done.
func (s *scenario) finish() {
	s.t.Helper()
	s.until(80, "all steps done", s.allDone)
}

// A failing L0 sends the step back to code (S50) and the next coder Run is
// given the check finding: failing test names and the log tail, under the
// "Previous failure" section of PF-CTX. The second commit passes and the
// requirement reaches done; the fixed finding is closed and the counter reset.
func TestM1CheckFailRetryReachesNextContext(t *testing.T) {
	s := newScenario(t)
	s.q.push("coder", coderCommits("first try", map[string]string{"sum.go": sumGo, "sum_test.go": brokenTst}))
	s.q.push("coder", coderCommits("fix the test", okFiles()))
	s.finish()
	s.wantRows("S04", "S40", "S05", "S50", "S04", "S40", "S05", "S41", "S06", "S42", "S10", "S07", "S29", "S08", "S44")

	coders := s.runsOf("coder")
	if len(coders) != 2 {
		t.Fatalf("coder runs %+v, want 2", coders)
	}
	checks := s.runsOf("check")
	if len(checks) != 2 || checks[0].Outcome != "check_failed" || checks[1].Outcome != "ok" {
		t.Errorf("check runs %+v, want check_failed then ok", checks)
	}
	first, second := s.contextOf(coders[0].ID), s.contextOf(coders[1].ID)
	if _, ok := first["Previous failure"]; ok {
		t.Error("first coder context has a Previous failure section")
	}
	prev, ok := second["Previous failure"]
	if !ok {
		t.Fatalf("second coder context lacks the Previous failure section: %v", second)
	}
	if !strings.Contains(prev, "Failing tests: TestSumBroken") {
		t.Errorf("failing test name missing from %q", prev)
	}
	if !strings.Contains(prev, "--- FAIL: TestSumBroken") {
		t.Errorf("log tail missing from %q", prev)
	}
	// PF-CTX order: the failure comes after the stable requirement content.
	if strings.Index(s.rawContext(coders[1].ID), "## Previous failure") < strings.Index(s.rawContext(coders[1].ID), "## Requirement") {
		t.Error("Previous failure rendered before the requirement")
	}

	var status string
	query(t, s.app, `SELECT status FROM findings WHERE scope = 'R-m1/S1' AND kind = 'check'`, &status)
	if status != "closed" {
		t.Errorf("check finding status %s after check_pass, want closed", status)
	}
	if n := s.counter(step1, "check_fail"); n != 0 {
		t.Errorf("check_fail counter %d after check_pass, want 0", n)
	}
	// One failure is below cap-1: no model upgrade yet.
	if got := models(coders); strings.Join(got, ",") != "sonnet,sonnet" {
		t.Errorf("coder models %v", got)
	}
}

// review_changes: the reviewer's findings go back to the coder (S52,
// review_rounds++), whose rerun context lists them as open findings. The
// reviewer's second context carries only the delta since last_reviewed_sha
// and is free of coder-only material.
func TestM1ReviewChangesRoundAndDelta(t *testing.T) {
	s := newScenario(t)
	s.q.push("coder", coderCommits("implement Sum", okFiles()))
	s.q.push("coder", coderCommits("address review", map[string]string{"extra.go": extraGo}))
	s.q.push("reviewer", reviewerSays("changes", "sum.go", 3, "Sum needs a three-argument companion"))
	s.q.push("reviewer", reviewerSays("approve", "sum.go", 3))

	s.until(40, "back in code after review_changes", func() bool {
		st, stage, _ := s.stepPos(step1)
		return st == "queued" && stage == "code" && s.counter(step1, "review_rounds") == 1
	})
	reviewed := s.sessionHead()
	var last string
	query(t, s.app, `SELECT COALESCE(last_reviewed_sha, '') FROM steps WHERE id = 'R-m1/S1'`, &last)
	if last != reviewed {
		t.Fatalf("last_reviewed_sha %q, want the head the reviewer saw %q", last, reviewed)
	}
	s.finish()
	s.wantRows("S04", "S40", "S05", "S41", "S06", "S52", "S04", "S40", "S05", "S41", "S06", "S42", "S10", "S07", "S29", "S08", "S44")

	coders, reviewers := s.runsOf("coder"), s.runsOf("reviewer")
	if len(coders) != 2 || len(reviewers) != 2 {
		t.Fatalf("coder runs %d reviewer runs %d, want 2/2", len(coders), len(reviewers))
	}
	if reviewers[0].Outcome != "review_changes" || reviewers[1].Outcome != "ok" {
		t.Errorf("reviewer outcomes %+v", reviewers)
	}

	rerun := s.contextOf(coders[1].ID)
	open, ok := rerun["Open findings"]
	if !ok || !strings.Contains(open, "[blocking]") || !strings.Contains(open, "sum.go:3") ||
		!strings.Contains(open, "three-argument companion") {
		t.Errorf("coder rerun context lacks the open finding: %q", open)
	}
	if _, ok := s.contextOf(coders[0].ID)["Open findings"]; ok {
		t.Error("first coder context has open findings")
	}

	r1, r2 := s.contextOf(reviewers[0].ID), s.contextOf(reviewers[1].ID)
	if got := diffFiles(r1["Diff since last review"]); strings.Join(got, ",") != "sum.go,sum_test.go" {
		t.Errorf("round 1 diff files %v, want the whole step", got)
	}
	if got := diffFiles(r2["Diff since last review"]); strings.Join(got, ",") != "extra.go" {
		t.Errorf("round 2 diff files %v, want only the delta since last_reviewed_sha", got)
	}
	if !strings.Contains(r2["Open findings"], "three-argument companion") {
		t.Errorf("round 2 reviewer context lacks the open finding: %q", r2["Open findings"])
	}
	for _, banned := range []string{"Previous failure", "Other steps", "Discoveries and answered unknowns", "Relevant files"} {
		if _, ok := r2[banned]; ok {
			t.Errorf("reviewer context has coder-only section %q", banned)
		}
	}

	var status string
	query(t, s.app, `SELECT status FROM findings WHERE scope = 'R-m1/S1' AND kind = 'review'`, &status)
	if status != "closed" {
		t.Errorf("review finding status %s after approval, want closed", status)
	}
	// Review round 1 reached cap-1 of 2: the rerun coder uses the upgrade (D-044).
	if got := models(coders); strings.Join(got, ",") != "sonnet,opus" {
		t.Errorf("coder models %v, want sonnet then opus", got)
	}
}

// retry_upgrade at the integration level: the Run row and the harness
// invocation built for it carry opus once check_fail reached cap-1 (2 of 3),
// and a passing check resets the counter.
func TestM1RetryModelEscalation(t *testing.T) {
	s := newScenario(t)
	bad := coderCommits("broken", map[string]string{"sum.go": sumGo, "sum_test.go": brokenTst})
	s.q.push("coder", bad)
	s.q.push("coder", coderCommits("still broken", map[string]string{"extra.go": extraGo}))
	s.q.push("coder", coderCommits("fixed", okFiles()))
	s.finish()
	s.wantRows("S50", "S50", "S41", "S42")

	coders := s.runsOf("coder")
	want := []string{"sonnet", "sonnet", "opus"}
	if got := models(coders); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("coder run models %v, want %v", got, want)
	}
	for _, r := range coders {
		if got := s.invocationModel(r.ID); got != r.Model {
			t.Errorf("run %s: invocation model %q, run row %q", r.ID, got, r.Model)
		}
	}
	// Reviewers keep the configured model; there is no upgrade for them.
	for _, r := range s.runsOf("reviewer") {
		if r.Model != "opus" || s.invocationModel(r.ID) != "opus" {
			t.Errorf("reviewer run %+v", r)
		}
	}
}

// At the cap the failure escalates: the step waits for a rescuer (not part of
// M1, so its Run stays pending), no further coder Run is created and the
// requirement does not move on, however long the clock runs.
func TestM1CheckFailCapEscalatesToRescue(t *testing.T) {
	s := newScenario(t)
	for i := 1; i <= 4; i++ { // a repeated identical commit would be "nothing to commit"
		s.q.push("coder", coderCommits(fmt.Sprintf("attempt %d", i), map[string]string{"sum.go": sumGo, "sum_test.go": brokenTst,
			"attempt.txt": fmt.Sprintf("%d\n", i)}))
	}
	s.until(60, "escalation", func() bool { _, _, reason := s.stepPos(step1); return reason == "rescue" })
	s.wantRows("S50", "S50", "S50", "S51+esc")
	if n := s.count("S50"); n != 3 {
		t.Errorf("S50 hit %d times, want 3 (check_fail cap)", n)
	}
	coders := s.runsOf("coder")
	if got := models(coders); strings.Join(got, ",") != "sonnet,sonnet,opus,opus" {
		t.Errorf("coder models %v: want the upgrade from the third Run on", got)
	}

	for i := 0; i < 12; i++ { // a day of ticks
		s.clk.Advance(2 * time.Hour)
		s.tick()
	}
	if got := len(s.runsOf("coder")); got != 4 {
		t.Errorf("%d coder runs after escalation, want 4", got)
	}
	st, stage, reason := s.stepPos(step1)
	if st != "waiting" || stage != "check" || reason != "rescue" {
		t.Errorf("step %s/%s/%s, want waiting/check/rescue", st, stage, reason)
	}
	rescuers := s.runsOf("rescuer")
	if len(rescuers) != 1 || rescuers[0].Status != "pending" || rescuers[0].Model != "opus" {
		t.Errorf("rescuer runs %+v, want one pending opus Run", rescuers)
	}
	if n := s.counter(step1, "rescues"); n != 1 {
		t.Errorf("rescues %d, want 1", n)
	}
	var hold string
	query(t, s.app, `SELECT hold FROM requirements WHERE id = 'R-m1'`, &hold)
	t.Logf("requirement hold after step escalation: %s", hold)
	if st2, _, why := s.stepPos(step2); st2 != "waiting" || why != "deps" {
		t.Errorf("integrate step %s/%s, want waiting/deps", st2, why)
	}
	if len(s.runsOf("reviewer")) != 0 || len(s.gh.created) != 0 {
		t.Error("work went on past the escalated step")
	}
}

// A session that exits cleanly with an invalid result.json is a bad_result:
// the same stage reruns (S75) and a valid result resets the counter.
func TestM1BadResultRerunsAndResets(t *testing.T) {
	s := newScenario(t)
	s.q.push("coder", coderBadResult())
	s.q.push("coder", coderBadResult())
	s.q.push("coder", coderCommits("implement Sum", okFiles()))
	s.finish()
	s.wantRows("S04", "S75", "S04", "S75", "S04", "S40", "S05", "S41", "S06", "S42")

	coders := s.runsOf("coder")
	if len(coders) != 3 || coders[0].Outcome != "bad_result" || coders[1].Outcome != "bad_result" || coders[2].Outcome != "ok" {
		t.Fatalf("coder runs %+v", coders)
	}
	if n := s.counter(step1, "bad_result"); n != 0 {
		t.Errorf("bad_result counter %d after a valid result, want 0 (any_valid_result)", n)
	}
	if got := models(coders); strings.Join(got, ",") != "sonnet,sonnet,sonnet" {
		t.Errorf("bad results must not upgrade the model: %v", got)
	}
}

// bad_result at its cap (2) escalates like any other cap: the step waits for
// the rescuer and no further coder Run starts.
func TestM1BadResultCapEscalates(t *testing.T) {
	s := newScenario(t)
	s.q.push("coder", coderBadResult())
	s.until(40, "escalation", func() bool { _, _, reason := s.stepPos(step1); return reason == "rescue" })
	s.wantRows("S75", "S75", "S76+esc")
	for i := 0; i < 10; i++ {
		s.tick()
	}
	if got := len(s.runsOf("coder")); got != 3 {
		t.Errorf("%d coder runs, want 3 (two reruns, then escalation)", got)
	}
	if st, _, reason := s.stepPos(step1); st != "waiting" || reason != "rescue" {
		t.Errorf("step %s/%s, want waiting/rescue", st, reason)
	}
	if rs := s.runsOf("rescuer"); len(rs) != 1 || rs[0].Status != "pending" {
		t.Errorf("rescuer runs %+v", rs)
	}
}
