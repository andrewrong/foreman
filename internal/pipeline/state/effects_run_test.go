package state

import (
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/spec/model"
)

func TestTransitionIgnoredWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateDone, model.StageReview)
	out := e.transition(st, Event{Name: "check_fail"})
	if !out.Ignored || out.RowID != "" {
		t.Fatalf("outcome %+v, want ignored", out)
	}
	wantInfo(t, e.info(st), "done", "review", "")
	if _, err := e.eng.Transition(e.ctx, e.db, "R-t-1/S9", Event{Name: "dispatch"}); err == nil {
		t.Error("unknown step accepted")
	}
}

func TestHold1AndHoldReleasedAssertion(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1", func(r *model.Requirement) { r.Hold = model.HoldOwner })
	st := e.step("R-t-1", model.StepBuild, model.StateQueued, model.StageReview)
	out := e.transition(st, dispatchOK)
	wantRow(t, out.RowID, "S01")
	i := e.info(st)
	wantInfo(t, i, "waiting", "review", "hold")
	if i.Resume != "review" {
		t.Errorf("resume_stage %q, want review", i.Resume)
	}
	if n := len(e.runs()); n != 0 {
		t.Errorf("%d runs created under a hold", n)
	}
	// hold_released while the hold is still set is refused (T7a note).
	if _, err := e.eng.Transition(e.ctx, e.db, st, Event{Name: "hold_released"}); err == nil {
		t.Fatal("hold_released accepted with hold=owner")
	}
	e.exec(`UPDATE requirements SET hold = 'none' WHERE id = 'R-t-1'`) // test setup only
	wantRow(t, e.transition(st, Event{Name: "hold_released"}).RowID, "S19")
	wantInfo(t, e.info(st), "queued", "review", "")
}

func TestPendingTriggerRunsAfterHoldClears(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 6} })
	wantRow(t, e.transition(st, Event{Name: "infra"}).RowID, "S72") // hold = owner

	// A replan trigger while the owner hold is active is queued, not applied.
	ro := e.transitionReq("R-t-1", Event{Name: "replan_trigger", Detail: "late conflict"})
	if !ro.Ignored || e.hold("R-t-1") != "owner" {
		t.Fatalf("outcome %+v hold %s", ro, e.hold("R-t-1"))
	}
	var pending string
	e.read(`SELECT pending_triggers FROM requirements WHERE id = 'R-t-1'`, nil, &pending)
	if !strings.Contains(pending, "replan_trigger") {
		t.Fatalf("pending_triggers %s", pending)
	}
	// Resume clears the hold; the queued trigger then fires (H01).
	ro = e.transitionReq("R-t-1", Event{Name: "owner_answer", Answer: "resume"})
	wantRow(t, ro.RowID, "H14")
	if got := rowIDsReq(ro.Followups); len(got) != 1 || got[0] != "H01" {
		t.Fatalf("followups %v, want [H01]", got)
	}
	if e.hold("R-t-1") != "replan" {
		t.Errorf("hold = %s, want replan", e.hold("R-t-1"))
	}
	if i := e.info(st); i.State != "waiting" || i.Reason != "hold" {
		t.Errorf("released step not re-held by the replan: %+v", i)
	}
	if rs := e.runs(); len(rs) != 1 || rs[0].Role != "planner" {
		t.Errorf("runs %+v, want the planner Run (hold drained)", rs)
	}
}

func TestIntegrateCheckFailAddsFixStep(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	integ := e.step("R-t-1", model.StepIntegrate, model.StateWaiting, model.StageCheck,
		func(s *model.Step) { s.Reason = "ci"; s.Touches = []string{"store"} })
	out := e.transition(integ, Event{Name: "l1_fail", FailingTests: []string{"TestY"}, LogTail: "x"})
	wantRow(t, out.RowID, "S31")
	wantInfo(t, e.info(integ), "waiting", "check", "fix")
	if e.reqCounter("R-t-1", "fix_steps") != 1 {
		t.Errorf("fix_steps = %d", e.reqCounter("R-t-1", "fix_steps"))
	}
	var fixID, typ, state, stage, forStep, acc, touches string
	e.read(`SELECT id, type, state, stage, fix_for, acceptance, touches FROM steps WHERE type = 'fix'`, nil,
		&fixID, &typ, &state, &stage, &forStep, &acc, &touches)
	if state != "queued" || stage != "code" || forStep != integ || acc != `["F1"]` || touches != `["store"]` {
		t.Errorf("fix step %s %s/%s for %s acc %s touches %s", fixID, state, stage, forStep, acc, touches)
	}
	// fix steps are limited: at the cap the requirement is held for rescue (S32).
	e.exec(`UPDATE requirements SET req_counters = '{"fix_steps": 3}' WHERE id = 'R-t-1'`) // test setup only
	e.exec(`UPDATE steps SET state = 'waiting', reason = 'ci' WHERE id = ?`, integ)        // test setup only
	out = e.transition(integ, Event{Name: "l1_fail", LogTail: "y"})
	wantRow(t, out.RowID, "S32")
	if len(out.Reqs) != 1 || out.Reqs[0].RowID != "H06+roo" || e.hold("R-t-1") != "rescue" {
		t.Errorf("nested %v hold %s", rowIDs(out.Reqs), e.hold("R-t-1"))
	}
	if rs := e.runs(); len(rs) != 1 || rs[0].Role != "rescuer" || rs[0].Scope != "requirement" || rs[0].StepID != "" {
		t.Errorf("runs %+v, want a requirement-scoped rescuer", rs)
	}
}

func TestReviewChangesAddsFindingsAndReviewedSHA(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageReview)
	ev := Event{Name: "review_changes", Head: "abc", Findings: []FindingIn{
		{Severity: "blocking", Path: "a.go", Line: 3, Problem: "p", CloseWhen: "c"}}}
	wantRow(t, e.transition(st, ev).RowID, "S52")
	var sha, status string
	var round int
	e.read(`SELECT last_reviewed_sha FROM steps WHERE id = ?`, []any{st}, &sha)
	e.read(`SELECT status, round FROM findings WHERE scope = ?`, []any{st}, &status, &round)
	if sha != "abc" || status != "open" || round != 1 {
		t.Errorf("sha %q finding %s round %d", sha, status, round)
	}
	if e.info(st).Counters["review_rounds"] != 1 {
		t.Errorf("review_rounds = %d", e.info(st).Counters["review_rounds"])
	}
	// no findings in a review_changes event is an error and rolls back
	e.exec(`UPDATE steps SET state = 'running', stage = 'review' WHERE id = ?`, st) // test setup only
	if _, err := e.eng.Transition(e.ctx, e.db, st, Event{Name: "review_changes", Head: "x"}); err == nil {
		t.Error("review_changes without findings accepted")
	}
	if e.info(st).Counters["review_rounds"] != 1 {
		t.Error("failed transition changed counters")
	}
}

func TestDeadlines(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	now := e.clk.Now()
	deadline := func(id string) time.Time {
		var d string
		e.read(`SELECT COALESCE(deadline, '') FROM steps WHERE id = ?`, []any{id}, &d)
		if d == "" {
			return time.Time{}
		}
		ts, err := time.Parse(timeLayout, d)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 2} })
	wantRow(t, e.transition(st, Event{Name: "infra"}).RowID, "S71")
	// infra counter 3 -> third backoff step, 30m
	if d := deadline(st); !d.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("infra deadline %v, want now+30m", d)
	}
	wantRow(t, e.transition(st, Event{Name: "deadline_passed"}).RowID, "S14")
	if d := deadline(st); !d.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("queued deadline %v, want now+24h", d)
	}
	// quota wait uses resets_at + 5m
	q := e.step("R-t-1", model.StepBuild, model.StateQueued, model.StageCode)
	reset := now.Add(2 * time.Hour)
	wantRow(t, e.transition(q, Event{Name: "dispatch", Deps: "done", Lease: "wait", Budget: "ok", ResetsAt: reset}).RowID, "S03")
	if d := deadline(q); !d.Equal(reset.Add(5 * time.Minute)) {
		t.Errorf("quota deadline %v, want resets_at+5m", d)
	}
	// extend_deadline on a starved queued step, alerting at most once per 6h
	wantRow(t, e.transition(q, Event{Name: "lease_granted"}).RowID, "S12")
	e.clk.Advance(25 * time.Hour)
	wantRow(t, e.transition(q, Event{Name: "deadline_passed"}).RowID, "S09")
	if d := deadline(q); !d.Equal(e.clk.Now().Add(24 * time.Hour)) {
		t.Errorf("extended deadline %v", d)
	}
	wantRow(t, e.transition(q, Event{Name: "deadline_passed"}).RowID, "S09")
	if n := e.count(`SELECT COUNT(*) FROM notify_queue WHERE kind = 'starved'`); n != 1 {
		t.Errorf("%d starved alerts within 6h, want 1", n)
	}
	e.clk.Advance(7 * time.Hour)
	e.transition(q, Event{Name: "deadline_passed"})
	if n := e.count(`SELECT COUNT(*) FROM notify_queue WHERE kind = 'starved'`); n != 2 {
		t.Errorf("%d starved alerts after 7h, want 2", n)
	}
}

func TestOwnerAnswersAndDrops(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	s1 := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 6} })
	s2 := e.step("R-t-1", model.StepBuild, model.StateQueued, model.StageCode)
	wantRow(t, e.transition(s1, Event{Name: "infra"}).RowID, "S72")
	wantRow(t, e.transition(s2, dispatchOK).RowID, "S01")

	// H23: drop only the step the ask is about; the requirement continues.
	ro := e.transitionReq("R-t-1", Event{Name: "owner_answer", Answer: "drop_step", DropReason: "owner: not needed"})
	wantRow(t, ro.RowID, "H23")
	var reason string
	e.read(`SELECT reason FROM steps WHERE id = ?`, []any{s1}, &reason)
	wantInfo(t, e.info(s1), "dropped", "code", reason)
	if reason != "owner: not needed" {
		t.Errorf("dropped reason %q", reason)
	}
	wantInfo(t, e.info(s2), "queued", "code", "")
	if e.hold("R-t-1") != "none" {
		t.Errorf("hold %s", e.hold("R-t-1"))
	}

	// H24: terminate. drop_all_steps requires dropped_reason (ACR-05).
	if _, err := e.eng.TransitionReq(e.ctx, e.db, "R-t-1", Event{Name: "owner_drop"}); err == nil {
		t.Fatal("owner_drop without a reason accepted")
	}
	wantInfo(t, e.info(s2), "queued", "code", "")
	e.exec(`UPDATE steps SET state = 'running' WHERE id = ?`, s2) // test setup only
	e.exec(`INSERT INTO runs (id, req_id, step_id, role, status, spec_version, dir, config_rev, started_at)
		VALUES ('rz', 'R-t-1', ?, 'coder', 'running', 1, 'runs/rz', 1, 'x')`, s2) // test setup only
	wantRow(t, e.transitionReq("R-t-1", Event{Name: "owner_drop", DropReason: "owner terminated"}).RowID, "H24")
	wantInfo(t, e.info(s2), "dropped", "code", "owner terminated")
	var dr, status string
	e.read(`SELECT dropped_reason FROM requirements WHERE id = 'R-t-1'`, nil, &dr)
	e.read(`SELECT status FROM runs WHERE id = 'rz'`, nil, &status)
	if dr != "owner terminated" || status != "killing" {
		t.Errorf("dropped_reason %q, run status %q (kill_runs)", dr, status)
	}
}

func TestParkingAndAskDeadline(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1", func(r *model.Requirement) { r.SessionRepo = "sessions/x" })
	e.exec(`UPDATE requirements SET session_repo_state = 'active' WHERE id = 'R-t-1'`) // test setup only
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 6} })
	e.transition(st, Event{Name: "infra"})
	var dl string
	e.read(`SELECT deadline FROM asks WHERE req_id = 'R-t-1'`, nil, &dl)
	if want := fmtTime(e.clk.Now().Add(72 * time.Hour)); dl != want {
		t.Errorf("ask deadline %s, want sent_at+72h %s", dl, want)
	}
	ro := e.transitionReq("R-t-1", Event{Name: "deadline_passed"})
	wantRow(t, ro.RowID, "H17")
	var srs string
	var parked int
	e.read(`SELECT session_repo_state, parked FROM requirements WHERE id = 'R-t-1'`, nil, &srs, &parked)
	if e.hold("R-t-1") != "parked" || srs != "releasing" || parked != 1 {
		t.Errorf("hold %s repo %s parked %d", e.hold("R-t-1"), srs, parked)
	}
	if e.count(`SELECT COUNT(*) FROM notify_queue WHERE kind = 'notify'`) != 1 {
		t.Error("park did not notify")
	}
	wantRow(t, e.transitionReq("R-t-1", Event{Name: "deadline_passed"}).RowID, "H18")
	// H14 from parked: the ask is still answerable and resume unparks.
	wantRow(t, e.transitionReq("R-t-1", Event{Name: "owner_answer", Answer: "resume"}).RowID, "H14")
	e.read(`SELECT parked FROM requirements WHERE id = 'R-t-1'`, nil, &parked)
	if parked != 0 || e.hold("R-t-1") != "none" {
		t.Errorf("parked %d hold %s after resume", parked, e.hold("R-t-1"))
	}
}

func TestAskMootWhenStepEnds(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 6} })
	e.transition(st, Event{Name: "infra"})
	wantRow(t, e.transition(st, Event{Name: "drop"}).RowID, "S90")
	var status string
	e.read(`SELECT status FROM asks WHERE req_id = 'R-t-1'`, nil, &status)
	if status != "moot" {
		t.Errorf("ask status %s, want moot (subject became terminal)", status)
	}
}

func TestAccessAskGrantAndDeny(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCode)
	out := e.transition(st, Event{Name: "access_request", Tier: "R2", Resource: "net:example"})
	wantRow(t, out.RowID, "S79")
	if e.reqCounter("R-t-1", "access_requests") != 1 {
		t.Error("access_requests not counted")
	}
	wantRow(t, e.transitionReq("R-t-1", Event{Name: "owner_answer", Answer: "grant", Resource: "net:example"}).RowID, "H21")
	var grants string
	e.read(`SELECT access_grants FROM steps WHERE id = ?`, []any{st}, &grants)
	if grants != `["net:example"]` {
		t.Errorf("access_grants %s", grants)
	}
	wantInfo(t, e.info(st), "queued", "code", "")

	// R3 is auto-denied with a denial text; H22 style deny on an R2 ask.
	e.exec(`UPDATE steps SET state = 'running' WHERE id = ?`, st) // test setup only
	out = e.transition(st, Event{Name: "access_request", Tier: "R3", Resource: "prod:db"})
	wantRow(t, out.RowID, "S80")
	var denial string
	e.read(`SELECT denial FROM steps WHERE id = ?`, []any{st}, &denial)
	if denial != "access denied: prod:db" || e.info(st).Counters["access_denied"] != 1 {
		t.Errorf("denial %q counters %v", denial, e.info(st).Counters)
	}
}
