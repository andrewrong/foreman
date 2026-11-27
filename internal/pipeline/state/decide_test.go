package state

import (
	"slices"
	"testing"
)

func mustDecide(t *testing.T, s StepView, r ReqView, e Event) Outcome {
	t.Helper()
	out, err := Decide(s, r, e)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return out
}

func ciStep(counters map[string]int) StepView {
	return StepView{State: "waiting", Stage: "check", Type: "integrate", Reason: "ci", Counters: counters}
}

func TestDecideHold1Rewrite(t *testing.T) {
	// S10: waiting(deps) --deps_done--> queued(same). With a hold it becomes
	// waiting(hold), resume_stage = the stage it would have queued at.
	step := StepView{State: "waiting", Stage: "review", Type: "build", Reason: "deps"}
	ev := Event{Name: "deps_done"}
	free := mustDecide(t, step, ReqView{Hold: HoldNone}, ev)
	if free.RowID != "S10" || free.To != (To{State: "queued", Stage: "review"}) || free.HoldRewritten {
		t.Fatalf("no hold: %+v", free)
	}
	held := mustDecide(t, step, ReqView{Hold: HoldReplan}, ev)
	want := To{State: "waiting", Stage: "review", Reason: "hold", ResumeStage: "review"}
	if held.RowID != "S10" || held.To != want || !held.HoldRewritten {
		t.Fatalf("hold: %+v", held)
	}
	// A non-queued target is left alone under a hold (S02: waiting(deps)).
	disp := mustDecide(t, StepView{State: "queued", Stage: "code", Type: "build"}, ReqView{Hold: HoldOwner},
		Event{Name: "dispatch"})
	if disp.RowID != "S01" || disp.To.Reason != "hold" || disp.HoldRewritten {
		t.Fatalf("dispatch under hold: %+v", disp)
	}
	// Rescue retry goes to queued(first_stage); under hold it is rewritten.
	resc := StepView{State: "waiting", Stage: "review", Type: "integrate", Reason: "rescue"}
	out := mustDecide(t, resc, ReqView{Hold: HoldParked}, Event{Name: "rescue_decision", Decision: "retry"})
	if out.RowID != "S21" || out.To.ResumeStage != "check" || out.To.Reason != "hold" {
		t.Fatalf("S21 under hold: %+v", out)
	}
}

func TestDecideEscalatePseudoTarget(t *testing.T) {
	step := StepView{State: "running", Stage: "check", Type: "build",
		Counters: map[string]int{"check_fail": 3, "rescues": 0}}
	out := mustDecide(t, step, ReqView{}, Event{Name: "check_fail"})
	if out.RowID != "S51+esc" || out.To != (To{State: "waiting", Stage: "check", Reason: "rescue"}) {
		t.Fatalf("rescues below: %+v", out)
	}
	if want := []string{"add_findings:check", "inc:rescues", "create_run:rescuer"}; !slices.Equal(out.Effects, want) {
		t.Fatalf("effects %v want %v", out.Effects, want)
	}
	step.Counters["rescues"] = 2
	out = mustDecide(t, step, ReqView{}, Event{Name: "check_fail"})
	if out.To.Reason != "hold" || !slices.Contains(out.Effects, "req_hold:owner:H5") {
		t.Fatalf("rescues at cap: %+v", out)
	}
}

func TestDecideCounterAtCapSelectsBRow(t *testing.T) {
	ev := Event{Name: "lease_wait"}
	below := mustDecide(t, ciStep(map[string]int{"ci_extensions": 11}), ReqView{}, ev)
	if below.RowID != "S37" || below.To.Reason != "ci" || !slices.Contains(below.Effects, "inc:ci_extensions") {
		t.Fatalf("below: %+v", below)
	}
	at := mustDecide(t, ciStep(map[string]int{"ci_extensions": 12}), ReqView{}, ev)
	if at.RowID != "S37b" || at.To.Reason != "hold" || !slices.Contains(at.Effects, "req_hold:owner:H7") {
		t.Fatalf("at cap: %+v", at)
	}
	over := mustDecide(t, ciStep(map[string]int{"ci_extensions": 99}), ReqView{}, ev)
	if over.RowID != "S37b" {
		t.Fatalf("beyond cap: %+v", over)
	}
}

func TestDecideBudgetOver(t *testing.T) {
	step := StepView{State: "queued", Stage: "code", Type: "build"}
	out := mustDecide(t, step, ReqView{Hold: HoldNone}, Event{Name: "dispatch", Budget: "over"})
	if out.RowID != "S00" || out.To.Reason != "hold" {
		t.Fatalf("%+v", out)
	}
	if want := []string{"req_hold:owner:H4", "record_resume_stage"}; !slices.Equal(out.Effects, want) {
		t.Fatalf("effects %v", out.Effects)
	}
	ok := mustDecide(t, step, ReqView{}, Event{Name: "dispatch", Budget: "ok", Deps: "done", Lease: "ok"})
	if ok.RowID != "S04" || ok.To != (To{State: "running", Stage: "code"}) {
		t.Fatalf("budget ok: %+v", ok)
	}
}

func TestDecideGuardrailHit(t *testing.T) {
	step := StepView{State: "running", Stage: "merge", Type: "integrate"}
	out := mustDecide(t, step, ReqView{}, Event{Name: "guardrail_hit"})
	if out.RowID != "S47" || out.To != (To{State: "waiting", Stage: "merge", Reason: "hold"}) {
		t.Fatalf("%+v", out)
	}
	if want := []string{"req_hold:owner:H6", "record_resume_stage"}; !slices.Equal(out.Effects, want) {
		t.Fatalf("effects %v", out.Effects)
	}
	// Not a merge-stage integrate step: no row, ignored.
	other := mustDecide(t, StepView{State: "running", Stage: "code", Type: "build"}, ReqView{}, Event{Name: "guardrail_hit"})
	if !other.Ignored || other.RowID != "" || len(other.Effects) != 0 {
		t.Fatalf("%+v", other)
	}
}

func TestDecideIgnoredAndErrors(t *testing.T) {
	// Terminal states ignore everything.
	out := mustDecide(t, StepView{State: "done", Type: "build"}, ReqView{}, Event{Name: "dispatch"})
	if !out.Ignored {
		t.Fatalf("%+v", out)
	}
	// waiting(quota) receiving deps_done is a foreign exit: ignored.
	out = mustDecide(t, StepView{State: "waiting", Stage: "code", Type: "build", Reason: "quota"}, ReqView{}, Event{Name: "deps_done"})
	if !out.Ignored {
		t.Fatalf("%+v", out)
	}
	// Unknown event and missing guard input are errors, not silent.
	if _, err := Decide(StepView{State: "queued", Stage: "code", Type: "build"}, ReqView{}, Event{Name: "nope"}); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, err := Decide(StepView{State: "queued", Stage: "code", Type: "build"}, ReqView{}, Event{Name: "dispatch"}); err == nil {
		t.Fatal("dispatch without budget/deps/lease inputs accepted")
	}
	// queued(resume_stage) without a recorded stage is an error.
	if _, err := Decide(StepView{State: "waiting", Stage: "code", Type: "build", Reason: "hold"}, ReqView{}, Event{Name: "hold_released"}); err == nil {
		t.Fatal("hold_released without resume stage accepted")
	}
	// Effects are copies: mutating them does not corrupt the table.
	step := StepView{State: "running", Stage: "merge", Type: "integrate"}
	a := mustDecide(t, step, ReqView{}, Event{Name: "guardrail_hit"})
	a.Effects[0] = "x"
	if b := mustDecide(t, step, ReqView{}, Event{Name: "guardrail_hit"}); b.Effects[0] != "req_hold:owner:H6" {
		t.Fatalf("table mutated: %v", b.Effects)
	}
}

func TestDecideReqParkedAnswers(t *testing.T) {
	for _, hold := range []string{HoldOwner, HoldParked} {
		resume, err := DecideReq(ReqView{Hold: hold}, Event{Name: "owner_answer", Answer: "resume"})
		if err != nil || resume.RowID != "H14" || resume.To != HoldNone {
			t.Fatalf("%s resume: %+v %v", hold, resume, err)
		}
		if !slices.Contains(resume.Effects, "reset:ask.cause_counter") || !slices.Contains(resume.Effects, "set:parked=false") {
			t.Fatalf("H14 effects %v", resume.Effects)
		}
		drop, err := DecideReq(ReqView{Hold: hold}, Event{Name: "owner_answer", Answer: "drop"})
		if err != nil || drop.RowID != "H16" || drop.To != HoldNone ||
			!slices.Equal(drop.Effects, []string{"kill_runs", "drop_all_steps", "set:parked=false"}) {
			t.Fatalf("%s drop: %+v %v", hold, drop, err)
		}
	}
	// A stale answer with no pending ask is ignored.
	out, err := DecideReq(ReqView{Hold: HoldNone}, Event{Name: "owner_answer", Answer: "drop"})
	if err != nil || !out.Ignored {
		t.Fatalf("stale answer: %+v %v", out, err)
	}
}

func TestDecideReqRescueOrOwner(t *testing.T) {
	caps := func(replans, rescues int) ReqView {
		return ReqView{Hold: HoldNone, ReqCounters: map[string]int{"replans": replans, "req_rescues": rescues}}
	}
	ev := Event{Name: "replan_trigger"}
	out, err := DecideReq(caps(1, 0), ev)
	if err != nil || out.RowID != "H01" || out.To != HoldReplan {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = DecideReq(caps(2, 0), ev)
	if err != nil || out.RowID != "H02+roo" || out.To != HoldRescue || !slices.Contains(out.Effects, "inc:req_rescues") {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = DecideReq(caps(2, 2), ev)
	if err != nil || out.To != HoldOwner || !slices.Contains(out.Effects, "create_owner_ask:H5") {
		t.Fatalf("%+v %v", out, err)
	}
	// Lower-priority trigger under owner is ignored (recorded as pending elsewhere).
	out, err = DecideReq(ReqView{Hold: HoldOwner}, ev)
	if err != nil || !out.Ignored {
		t.Fatalf("%+v %v", out, err)
	}
}
