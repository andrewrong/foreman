package state

import (
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// A passing check closes the step's check finding (so the next context.md
// does not show a fixed failure) and resets check_fail; an approving review
// closes the review findings the reviewer was shown. Findings of other steps
// and of the other kind stay open.
func TestScenarioFindingsClosedByPassAndApprove(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-6")
	st := e.step("R-t-6", model.StepBuild, model.StateRunning, model.StageCheck)
	other := e.step("R-t-6", model.StepBuild, model.StateRunning, model.StageCheck)

	wantRow(t, e.transition(st, Event{Name: "check_fail", FailingTests: []string{"TestX"}, LogTail: "FAIL"}).RowID, "S50")
	wantRow(t, e.transition(other, Event{Name: "check_fail", FailingTests: []string{"TestY"}, LogTail: "FAIL"}).RowID, "S50")
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S04")
	wantRow(t, e.transition(st, Event{Name: "run_ok"}).RowID, "S40")
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S05")
	wantRow(t, e.transition(st, Event{Name: "check_pass"}).RowID, "S41")

	open := func(scope, kind string) int {
		return e.count(`SELECT COUNT(*) FROM findings WHERE scope = ? AND kind = ? AND status = 'open'`, scope, kind)
	}
	if open(st, "check") != 0 || open(other, "check") != 1 {
		t.Errorf("open check findings: step %d (want 0), other step %d (want 1)", open(st, "check"), open(other, "check"))
	}
	if n := e.info(st).Counters["check_fail"]; n != 0 {
		t.Errorf("check_fail = %d after check_pass, want 0", n)
	}

	wantRow(t, e.transition(st, dispatchOK()).RowID, "S06")
	review := Event{Name: "review_changes", Head: "abc123", Findings: []FindingIn{
		{Severity: "blocking", Path: "a.go", Line: 3, Problem: "p", CloseWhen: "c"}}}
	wantRow(t, e.transition(st, review).RowID, "S52")
	if open(st, "review") != 1 {
		t.Fatal("review finding not stored open")
	}
	if got := e.count(`SELECT COUNT(*) FROM steps WHERE id = ? AND last_reviewed_sha = 'abc123'`, st); got != 1 {
		t.Error("S52 did not write last_reviewed_sha")
	}
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S04")
	wantRow(t, e.transition(st, Event{Name: "run_ok"}).RowID, "S40")
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S05")
	wantRow(t, e.transition(st, Event{Name: "check_pass"}).RowID, "S41")
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S06")
	wantRow(t, e.transition(st, Event{Name: "review_approve", Head: "def456"}).RowID, "S42")
	if open(st, "review") != 0 {
		t.Error("review finding still open after approval")
	}
}

// l1_pass closes the integrate step's open check findings (implicit_writes);
// a build step's check findings stay open.
func TestScenarioL1PassClosesIntegrateCheckFindings(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-8")
	build := e.step("R-t-8", model.StepBuild, model.StateRunning, model.StageCheck)
	integ := e.step("R-t-8", model.StepIntegrate, model.StateWaiting, model.StageCheck,
		func(s *model.Step) { s.Reason = "ci" })
	wantRow(t, e.transition(build, Event{Name: "check_fail", FailingTests: []string{"TestB"}, LogTail: "FAIL"}).RowID, "S50")
	wantRow(t, e.transition(integ, Event{Name: "l1_fail", FailingTests: []string{"TestI"}, LogTail: "x"}).RowID, "S31")
	open := func(scope string) int {
		return e.count(`SELECT COUNT(*) FROM findings WHERE scope = ? AND kind = 'check' AND status = 'open'`, scope)
	}
	if open(integ) != 1 {
		t.Fatalf("integrate check finding not open after l1_fail: %d", open(integ))
	}
	e.exec(`UPDATE steps SET state = 'waiting', reason = 'ci' WHERE id = ?`, integ) // test setup only: as after S35 and S07
	wantRow(t, e.transition(integ, Event{Name: "l1_pass", SingleStep: "many"}).RowID, "S30")
	if open(integ) != 0 || open(build) != 1 {
		t.Errorf("open check findings after l1_pass: integrate %d (want 0), build step %d (want 1)", open(integ), open(build))
	}
}

// bad_result resets on any valid result and infra on any non-infra result
// (state-machine.yaml counters), so two non-consecutive bad results do not
// add up to the cap.
func TestScenarioBadResultAndInfraCountersReset(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-7")
	st := e.step("R-t-7", model.StepBuild, model.StateRunning, model.StageCode)

	wantRow(t, e.transition(st, Event{Name: "infra"}).RowID, "S71")
	if e.info(st).Counters["infra"] != 1 {
		t.Fatalf("infra counter %v", e.info(st).Counters)
	}
	e.exec(`UPDATE steps SET state = 'running', reason = NULL WHERE id = ?`, st) // the wake-up and re-dispatch, skipped
	wantRow(t, e.transition(st, Event{Name: "bad_result"}).RowID, "S75")
	if c := e.info(st).Counters; c["infra"] != 0 || c["bad_result"] != 1 {
		t.Errorf("after bad_result: infra %d (want 0, a non-infra result), bad_result %d (want 1)", c["infra"], c["bad_result"])
	}
	wantRow(t, e.transition(st, dispatchOK()).RowID, "S04")
	wantRow(t, e.transition(st, Event{Name: "run_ok"}).RowID, "S40")
	if c := e.info(st).Counters["bad_result"]; c != 0 {
		t.Errorf("bad_result = %d after a valid result, want 0", c)
	}
}
