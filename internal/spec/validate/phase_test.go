package validate

import (
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

var allPhases = map[model.Phase]bool{
	model.PhaseDraft: true, model.PhaseDropped: true, model.PhaseParked: true,
	model.PhaseDone: true, model.PhaseReporting: true, model.PhaseIntegrating: true,
	model.PhasePlanning: true, model.PhaseBuilding: true, model.PhaseStalled: true,
}

func TestDerivedPhaseTotal(t *testing.T) {
	statuses := []model.ReqStatus{model.StatusDraft, model.StatusSubmitted, ""}
	holds := []model.Hold{model.HoldNone, model.HoldReplan, model.HoldRescue, model.HoldOwner, model.HoldParked, ""}
	seen := map[model.Phase]int{}
	n := 0
	for _, st := range statuses {
		for _, h := range holds {
			for bits := 0; bits < 1<<9; bits++ {
				b := func(i int) bool { return bits&(1<<i) != 0 }
				in := PhaseInput{
					Status: st, Hold: h,
					HasDroppedReason: b(0), Parked: b(1), HasPR: b(2), AllPRsMerged: b(3),
					ReportGenerated: b(4), HasSteps: b(5), PlannerRunActive: b(6),
					NonTerminalIntegrate: b(7), NonTerminalWork: b(8),
				}
				p := DerivedPhase(in)
				if !allPhases[p] {
					t.Fatalf("input %+v mapped to non-phase %q", in, p)
				}
				seen[p]++
				n++
			}
		}
	}
	if n != 3*6*512 {
		t.Fatalf("enumerated %d combinations", n)
	}
	for p := range allPhases {
		if seen[p] == 0 {
			t.Errorf("phase %s is unreachable", p)
		}
	}
}

func TestDerivedPhaseOrder(t *testing.T) {
	sub := PhaseInput{Status: model.StatusSubmitted, Hold: model.HoldNone, HasSteps: true}
	with := func(f func(*PhaseInput)) PhaseInput { in := sub; f(&in); return in }
	cases := []struct {
		name string
		in   PhaseInput
		want model.Phase
	}{
		{"draft wins over everything", with(func(i *PhaseInput) { i.Status = model.StatusDraft; i.Parked = true; i.HasDroppedReason = true }), model.PhaseDraft},
		{"dropped over parked", with(func(i *PhaseInput) { i.HasDroppedReason = true; i.Parked = true }), model.PhaseDropped},
		{"parked over done", with(func(i *PhaseInput) { i.Parked = true; i.HasPR = true; i.AllPRsMerged = true; i.ReportGenerated = true }), model.PhaseParked},
		{"done", with(func(i *PhaseInput) { i.HasPR = true; i.AllPRsMerged = true; i.ReportGenerated = true }), model.PhaseDone},
		{"reporting", with(func(i *PhaseInput) { i.HasPR = true; i.AllPRsMerged = true }), model.PhaseReporting},
		{"merged flag without PR does not count", with(func(i *PhaseInput) { i.AllPRsMerged = true; i.ReportGenerated = true }), model.PhaseStalled},
		{"integrating", with(func(i *PhaseInput) { i.NonTerminalIntegrate = true; i.NonTerminalWork = true }), model.PhaseIntegrating},
		{"planning by hold", with(func(i *PhaseInput) { i.Hold = model.HoldReplan; i.NonTerminalWork = true }), model.PhasePlanning},
		{"planning without steps", with(func(i *PhaseInput) { i.HasSteps = false }), model.PhasePlanning},
		{"planner running without steps is not planning", with(func(i *PhaseInput) { i.HasSteps = false; i.PlannerRunActive = true }), model.PhaseStalled},
		{"building", with(func(i *PhaseInput) { i.NonTerminalWork = true }), model.PhaseBuilding},
		{"stalled", sub, model.PhaseStalled},
	}
	for _, c := range cases {
		if got := DerivedPhase(c.in); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestStepFlags(t *testing.T) {
	steps := []model.Step{
		{Type: model.StepBuild, State: model.StateDone},
		{Type: model.StepIntegrate, State: model.StateQueued},
		{Type: model.StepFix, State: model.StateDropped},
	}
	has, integ, work := StepFlags(steps)
	if !has || !integ || work {
		t.Errorf("got has=%v integrate=%v work=%v", has, integ, work)
	}
	if has, integ, work = StepFlags(nil); has || integ || work {
		t.Error("empty steps must give all false")
	}
}
