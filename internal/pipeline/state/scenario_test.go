package state

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// (a) Fast-lane happy path: one work step and the integrate step.
func TestScenarioFastLaneHappyPath(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	work := e.step("R-t-1", model.StepBuild, model.StateQueued, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"check_fail": 1} })
	integ := e.step("R-t-1", model.StepIntegrate, model.StateWaiting, model.StageCheck,
		func(s *model.Step) { s.DependsOn = []string{work} })
	e.exec(`INSERT INTO pull_requests (req_id, head_branch, head_sha, state) VALUES ('R-t-1', 'foreman/R-t-1', 'pr-head', 'open')`)

	steps := []struct {
		step, row, state, stage, reason string
		ev                              Event
	}{
		{work, "S04", "running", "code", "", dispatchOK},
		{work, "S40", "queued", "check", "", Event{Name: "run_ok"}},
		{work, "S05", "running", "check", "", dispatchOK},
		{work, "S41", "queued", "review", "", Event{Name: "check_pass"}},
		{work, "S06", "running", "review", "", dispatchOK},
		{work, "S42", "done", "review", "", Event{Name: "review_approve", Head: "c0ffee"}},
		{integ, "S10", "queued", "check", "", Event{Name: "deps_done"}},
		{integ, "S07", "waiting", "check", "ci", dispatchOK},
		{integ, "S29", "queued", "merge", "", Event{Name: "l1_pass", SingleStep: "one"}},
		{integ, "S08", "running", "merge", "", dispatchOK},
		{integ, "S44", "done", "merge", "", Event{Name: "merged"}},
	}
	for _, s := range steps {
		out := e.transition(s.step, s.ev)
		wantRow(t, out.RowID, s.row)
		wantInfo(t, e.info(s.step), s.state, s.stage, s.reason)
	}

	if got := e.info(work).Counters["check_fail"]; got != 0 {
		t.Errorf("check_fail after check_pass = %d, want 0 (reset:check_fail)", got)
	}
	var last string
	e.read(`SELECT last_reviewed_sha FROM steps WHERE id = ?`, []any{work}, &last)
	if last != "c0ffee" {
		t.Errorf("work last_reviewed_sha = %q, want c0ffee (S42)", last)
	}
	e.read(`SELECT last_reviewed_sha FROM pull_requests WHERE req_id = 'R-t-1'`, nil, &last)
	if last != "pr-head" {
		t.Errorf("E-PR.last_reviewed_sha = %q, want pr-head (S29)", last)
	}
	var roles []string
	for _, r := range e.runs() {
		roles = append(roles, r.Role)
		if r.Status != "pending" {
			t.Errorf("run %s status %s, want pending", r.ID, r.Status)
		}
	}
	if want := []string{"coder", "check", "reviewer", "ci_watch", "merge"}; !slices.Equal(roles, want) {
		t.Errorf("run roles %v, want %v", roles, want)
	}
	if got := e.runs()[0].Model; got != "sonnet" {
		t.Errorf("coder model %q, want sonnet", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM runs WHERE id NOT GLOB 'r[0-9a-z]*' OR length(id) > 8`); n != 0 {
		t.Errorf("%d run ids violate D-070 (r + base36, <= 8 chars)", n)
	}
}

// (b) check_fail increments up to the cap, the coder model upgrades after
// cap-1, and the failure at the cap escalates to a rescuer.
func TestScenarioCheckFailCapEscalates(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-2")
	st := e.step("R-t-2", model.StepBuild, model.StateRunning, model.StageCheck)
	fail := Event{Name: "check_fail", FailingTests: []string{"TestX"}, LogTail: "FAIL TestX\nboom"}

	for i := 1; i <= 3; i++ {
		wantRow(t, e.transition(st, fail).RowID, "S50")
		info := e.info(st)
		wantInfo(t, info, "queued", "code", "")
		if info.Counters["check_fail"] != i {
			t.Fatalf("check_fail = %d, want %d", info.Counters["check_fail"], i)
		}
		wantRow(t, e.transition(st, dispatchOK).RowID, "S04")
		wantRow(t, e.transition(st, Event{Name: "run_ok"}).RowID, "S40")
		wantRow(t, e.transition(st, dispatchOK).RowID, "S05")
	}
	// Coder Runs: check_fail 1 -> sonnet; 2 and 3 (cap-1 reached) -> opus.
	var models []string
	for _, r := range e.runs() {
		if r.Role == "coder" {
			models = append(models, r.Model)
		}
	}
	if want := []string{"sonnet", "opus", "opus"}; !slices.Equal(models, want) {
		t.Fatalf("coder models %v, want %v (D-044 retry_upgrade)", models, want)
	}

	out := e.transition(st, fail) // check_fail is at its cap of 3
	wantRow(t, out.RowID, "S51+esc")
	wantInfo(t, e.info(st), "waiting", "check", "rescue")
	if e.info(st).Counters["rescues"] != 1 {
		t.Errorf("rescues = %d, want 1", e.info(st).Counters["rescues"])
	}
	rs := e.runs()
	last := rs[len(rs)-1]
	if last.Role != "rescuer" || last.Scope != "step" || last.StepID != st || last.Model != "opus" {
		t.Errorf("escalation Run %+v, want step-scoped opus rescuer", last)
	}
	if n := e.count(`SELECT COUNT(*) FROM findings WHERE scope = ? AND kind = 'check' AND round BETWEEN 1 AND 4`, st); n != 4 {
		t.Errorf("check findings = %d, want 4 (one per failure, rounds 1..4)", n)
	}
	var tests, tail string
	e.read(`SELECT failing_tests, log_tail FROM findings WHERE scope = ? AND round = 1`, []any{st}, &tests, &tail)
	if tests != `["TestX"]` || tail != "FAIL TestX\nboom" {
		t.Errorf("finding payload %q %q", tests, tail)
	}
}

// (c) A replan hold: steps are held, the planner Run waits for the drain,
// planner_ok applies the plan and release_steps restores resume_stage.
func TestScenarioReplanHoldAndRelease(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-3")
	s1 := e.step("R-t-3", model.StepBuild, model.StateRunning, model.StageCode)
	s2 := e.step("R-t-3", model.StepBuild, model.StateQueued, model.StageReview)
	s3 := e.step("R-t-3", model.StepBuild, model.StateWaiting, model.StageReview, func(s *model.Step) { s.DependsOn = []string{s1} })
	s4 := e.step("R-t-3", model.StepBuild, model.StateRunning, model.StageCode)

	out := e.transition(s1, Event{Name: "spec_conflict", Detail: "X1: api differs"})
	wantRow(t, out.RowID, "S70")
	if len(out.Reqs) != 1 || out.Reqs[0].RowID != "H01" {
		t.Fatalf("nested table B rows %v, want [H01]", rowIDs(out.Reqs))
	}
	if e.hold("R-t-3") != "replan" {
		t.Fatalf("hold = %s", e.hold("R-t-3"))
	}
	i1 := e.info(s1)
	wantInfo(t, i1, "waiting", "code", "hold")
	if i1.Resume != "code" {
		t.Errorf("s1 resume_stage %q, want code (record_resume_stage)", i1.Resume)
	}
	if i := e.info(s2); i.State != "waiting" || i.Reason != "hold" || i.Resume != "review" {
		t.Errorf("queued s2 not held at its stage: %+v", i)
	}
	if i := e.info(s3); i.State != "waiting" || i.Reason != "hold" || i.Resume != "review" {
		t.Errorf("waiting(deps) s3 not held: %+v", i)
	}
	wantInfo(t, e.info(s4), "running", "code", "")
	if len(e.runs()) != 0 {
		t.Fatalf("planner Run created while s4 still runs: %+v", e.runs())
	}

	// s4 finishes its Run: queued(check) is rewritten by HOLD-1 and the
	// requirement is drained, so the planner Run is created in this transaction.
	out = e.transition(s4, Event{Name: "run_ok"})
	wantRow(t, out.RowID, "S40")
	if !out.HoldRewritten {
		t.Error("HOLD-1 did not rewrite queued(check)")
	}
	if i := e.info(s4); i.State != "waiting" || i.Reason != "hold" || i.Resume != "check" {
		t.Errorf("s4 after HOLD-1: %+v", i)
	}
	rs := e.runs()
	if len(rs) != 1 || rs[0].Role != "planner" || rs[0].Model != "opus" || rs[0].StepID != "" {
		t.Fatalf("runs %+v, want one planner Run", rs)
	}
	var snap string
	e.read(`SELECT p.steps FROM runs r JOIN plan_snapshots p ON p.id = r.snapshot_id WHERE r.id = ?`, []any{rs[0].ID}, &snap)
	if strings.Count(snap, `"id"`) != 4 {
		t.Errorf("snapshot %s should list the 4 steps", snap)
	}
	var input string
	e.read(`SELECT input FROM runs WHERE id = ?`, []any{rs[0].ID}, &input)
	if !strings.Contains(input, "X1: api differs") {
		t.Errorf("planner input %s lacks the trigger", input)
	}

	boundaries := []string{"do not touch x"}
	ro := e.transitionReq("R-t-3", Event{Name: "planner_ok", Plan: &PlanDiff{
		RunID: rs[0].ID, BaseSpecVersion: 1, SpecPatch: &SpecPatch{Boundaries: &boundaries},
		Steps: []PlanStep{
			{Op: "keep", ID: s1}, {Op: "keep", ID: s3}, {Op: "keep", ID: s4},
			{Op: "update", ID: s2, Goal: "g2", Acceptance: []string{"A1"}, Touches: []string{"pipeline"}, DependsOn: []string{s1}, Reason: "x"},
			{Op: "add", TempID: "N1", Type: "build", Goal: "new", Acceptance: []string{"A1"}, Touches: []string{"pipeline"}, DependsOn: []string{s2}},
		}}})
	wantRow(t, ro.RowID, "H03")
	if e.hold("R-t-3") != "none" || e.reqCounter("R-t-3", "replans") != 1 {
		t.Errorf("after planner_ok: hold=%s replans=%d", e.hold("R-t-3"), e.reqCounter("R-t-3", "replans"))
	}
	// release_steps: back to queued(resume_stage); update restarted s2 at code.
	for _, w := range []struct {
		id, stage string
	}{{s1, "code"}, {s2, "code"}, {s3, "review"}, {s4, "check"}} {
		wantInfo(t, e.info(w.id), "queued", w.stage, "")
		if e.info(w.id).Resume != "" {
			t.Errorf("%s keeps resume_stage after release", w.id)
		}
	}
	var added, ver int
	e.read(`SELECT COUNT(*) FROM steps WHERE req_id = 'R-t-3' AND seq = 5 AND state = 'queued' AND stage = 'code'`, nil, &added)
	e.read(`SELECT spec_version FROM requirements WHERE id = 'R-t-3'`, nil, &ver)
	if added != 1 || ver != 2 || e.count(`SELECT COUNT(*) FROM req_spec_versions WHERE req_id = 'R-t-3' AND version = 1`) != 1 {
		t.Errorf("add step %d, spec_version %d, saved old version missing", added, ver)
	}
}

// (d) An owner ask records its cause counter; resume (H14) clears it once.
func TestScenarioOwnerAskCauseAndResume(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-4")
	st := e.step("R-t-4", model.StepBuild, model.StateRunning, model.StageCode,
		func(s *model.Step) { s.Counters = model.Counters{"infra": 6} })

	out := e.transition(st, Event{Name: "infra"})
	wantRow(t, out.RowID, "S72")
	if len(out.Reqs) != 1 || out.Reqs[0].RowID != "H13" {
		t.Fatalf("nested rows %v, want [H13]", rowIDs(out.Reqs))
	}
	i := e.info(st)
	wantInfo(t, i, "waiting", "code", "hold")
	if i.Resume != "code" || e.hold("R-t-4") != "owner" {
		t.Fatalf("resume %q hold %s", i.Resume, e.hold("R-t-4"))
	}
	var id, h, subject, cause, status string
	e.read(`SELECT id, h_item, subject, cause_counter, status FROM asks WHERE req_id = 'R-t-4'`, nil, &id, &h, &subject, &cause, &status)
	if id != "Q-t-1" || h != "H7" || subject != st || cause != "infra" || status != "open" {
		t.Fatalf("ask %s %s %s %s %s", id, h, subject, cause, status)
	}
	if n := e.count(`SELECT COUNT(*) FROM asks WHERE req_id = 'R-t-4'`); n != 1 {
		t.Fatalf("%d asks, want exactly 1 per trigger", n)
	}

	if _, err := e.eng.TransitionReq(e.ctx, e.db, "R-t-4", Event{Name: "owner_answer", Answer: "grant"}); err == nil {
		t.Fatal("answer that is not an option of the ask was accepted")
	}
	ro := e.transitionReq("R-t-4", Event{Name: "owner_answer", Answer: "resume"})
	wantRow(t, ro.RowID, "H14")
	i = e.info(st)
	wantInfo(t, i, "queued", "code", "")
	if i.Counters["infra"] != 0 || e.hold("R-t-4") != "none" {
		t.Errorf("after resume: infra=%d hold=%s", i.Counters["infra"], e.hold("R-t-4"))
	}
	e.read(`SELECT status, answer FROM asks WHERE id = 'Q-t-1'`, nil, &status, &h)
	if status != "answered" || h != "resume" {
		t.Errorf("ask %s/%s after answer", status, h)
	}
	// The counter was cleared once: the next infra failure counts from zero.
	wantRow(t, e.transition(st, dispatchOK).RowID, "S04")
	wantRow(t, e.transition(st, Event{Name: "infra"}).RowID, "S71")
	if e.info(st).Counters["infra"] != 1 {
		t.Errorf("infra = %d after one new failure, want 1", e.info(st).Counters["infra"])
	}

	// Escalation with rescues and check_fail both at cap: the ask lists both.
	e.req("R-t-5")
	s5 := e.step("R-t-5", model.StepBuild, model.StateRunning, model.StageCheck,
		func(s *model.Step) { s.Counters = model.Counters{"check_fail": 3, "rescues": 2} })
	out = e.transition(s5, Event{Name: "check_fail"})
	wantRow(t, out.RowID, "S51+esc")
	e.read(`SELECT h_item, cause_counter FROM asks WHERE req_id = 'R-t-5'`, nil, &h, &cause)
	if h != "H5" || cause != "check_fail,rescues" {
		t.Fatalf("ask %s cause %q", h, cause)
	}
	e.transitionReq("R-t-5", Event{Name: "owner_retry"})
	i = e.info(s5)
	wantInfo(t, i, "queued", "check", "")
	if i.Counters["check_fail"] != 0 || i.Counters["rescues"] != 0 {
		t.Errorf("counters after retry: %v", i.Counters)
	}

	// H4: budget cause; resume appends one original budget.
	e.req("R-t-6")
	s6 := e.step("R-t-6", model.StepBuild, model.StateQueued, model.StageCode)
	out = e.transition(s6, Event{Name: "dispatch", Deps: "done", Lease: "ok", Budget: "over"})
	wantRow(t, out.RowID, "S00")
	e.read(`SELECT h_item, cause_counter FROM asks WHERE req_id = 'R-t-6'`, nil, &h, &cause)
	if h != "H4" || cause != "budget" {
		t.Fatalf("H4 ask %s cause %q", h, cause)
	}
	e.transitionReq("R-t-6", Event{Name: "owner_answer", Answer: "resume"})
	var ext int
	e.read(`SELECT budget_extensions FROM requirements WHERE id = 'R-t-6'`, nil, &ext)
	if ext != 1 {
		t.Errorf("budget_extensions = %d, want 1", ext)
	}
	wantInfo(t, e.info(s6), "queued", "code", "")
}

// (e) A panic inside an effect executor rolls the whole transaction back and
// leaves the database writable; an unknown effect is an error with rollback.
func TestScenarioExecutorPanicRollsBack(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-7")
	st := e.step("R-t-7", model.StepBuild, model.StateRunning, model.StageCheck)

	e.eng.exec["add_findings:check"] = func(c *txc, _ string) error { panic("executor exploded") }
	func() {
		defer func() {
			if p := recover(); p != "executor exploded" {
				t.Fatalf("recovered %v, want the executor panic re-raised", p)
			}
		}()
		_, _ = e.eng.Transition(e.ctx, e.db, st, Event{Name: "check_fail"})
		t.Fatal("Transition returned instead of panicking")
	}()
	assertUntouched := func(why string) {
		t.Helper()
		i := e.info(st)
		wantInfo(t, i, "running", "check", "")
		if len(i.Counters) != 0 || e.count(`SELECT COUNT(*) FROM findings`) != 0 {
			t.Fatalf("%s: counters %v findings %d survive", why, i.Counters, e.count(`SELECT COUNT(*) FROM findings`))
		}
	}
	assertUntouched("panic")

	// The write connection is not stuck in a transaction: a normal write works.
	e.eng = NewEngine(e.clk, Config{})
	delete(e.eng.exec, "add_findings:check")
	if _, err := e.eng.Transition(e.ctx, e.db, st, Event{Name: "check_fail"}); err == nil ||
		!strings.Contains(err.Error(), "unknown effect") {
		t.Fatalf("unknown effect: err = %v", err)
	}
	assertUntouched("unknown effect")

	e.eng = NewEngine(e.clk, Config{})
	wantRow(t, e.transition(st, Event{Name: "check_fail"}).RowID, "S50")
	wantInfo(t, e.info(st), "queued", "code", "")
	var n int
	if err := e.db.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("findings after good transition: %d %v", n, err)
	}
}
