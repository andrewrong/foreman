package state

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/smcheck"
)

func loadSpec(t *testing.T) *smcheck.Spec {
	t.Helper()
	data, err := os.ReadFile("../../../spec/design/state-machine.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := smcheck.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// counterPos maps a combination index to a counter value for the position:
// below uses 0 or cap-1, at_cap uses cap or cap+1, so both ends are hit.
func counterPos(pos string, capv, salt int) int {
	switch {
	case pos == "below" && salt%2 == 0:
		return 0
	case pos == "below":
		return capv - 1
	case salt%2 == 0:
		return capv
	}
	return capv + 1
}

var setHolds = []string{HoldReplan, HoldRescue, HoldOwner, HoldParked}

// combos enumerates every assignment of the dims over values(dim) and calls
// fn with a StepView-independent input set.
func combos(dims []string, values func(string) []string, fn func(assign map[string]string, salt int)) {
	space := make([][]string, len(dims))
	for i, d := range dims {
		space[i] = values(d)
	}
	salt := 0
	var rec func(i int, assign map[string]string)
	rec = func(i int, assign map[string]string) {
		if i == len(dims) {
			fn(assign, salt)
			salt++
			return
		}
		for _, v := range space[i] {
			assign[dims[i]] = v
			rec(i+1, assign)
		}
	}
	rec(0, map[string]string{})
}

func addDims(dst []string, gs []smcheck.Guard) []string {
	for _, g := range gs {
		if !slices.Contains(dst, g.Dim) {
			dst = append(dst, g.Dim)
		}
	}
	return dst
}

// inputs turns a dim assignment into Decide inputs.
func inputs(assign map[string]string, salt int) (ReqView, Event, map[string]int) {
	req := ReqView{Hold: HoldNone, ReqCounters: map[string]int{}}
	step := map[string]int{}
	var ev Event
	for d, v := range assign {
		kind, name, isCounter := strings.Cut(d, ":")
		switch {
		case isCounter && kind == "counter":
			step[name] = counterPos(v, dimCounters[name], salt)
		case isCounter:
			req.ReqCounters[name] = counterPos(v, dimReqCounters[name], salt)
		case d == "hold" && v == "set":
			req.Hold = setHolds[salt%len(setHolds)]
		case d == "deps":
			ev.Deps = v
		case d == "lease":
			ev.Lease = v
		case d == "tier":
			ev.Tier = v
		case d == "single_step":
			ev.SingleStep = v
		case d == "budget":
			ev.Budget = v
		case d == "decision":
			ev.Decision = v
		case d == "answer":
			ev.Answer = v
		}
	}
	return req, ev, step
}

func checkStepOutcome(t *testing.T, sp *smcheck.Spec, cf smcheck.Config, req ReqView, out Outcome, id string) {
	t.Helper()
	base := strings.TrimSuffix(id, "+esc")
	var row *smcheck.Row
	for i := range sp.Rows {
		if sp.Rows[i].ID == base {
			row = &sp.Rows[i]
		}
	}
	if row == nil {
		t.Fatalf("oracle row %s not in spec", id)
	}
	if !slices.Equal(out.Effects[:len(row.Effects)], row.Effects) {
		t.Errorf("%s: effects %v do not start with the row's %v", id, out.Effects, row.Effects)
	}
	rewritten := holdIsSet(req.Hold) && row.To.State == "queued"
	if out.HoldRewritten != rewritten {
		t.Errorf("%s: HoldRewritten=%v want %v", id, out.HoldRewritten, rewritten)
	}
	if rewritten {
		if out.To.State != "waiting" || out.To.Reason != "hold" || out.To.ResumeStage != out.To.Stage || out.To.Stage == "" {
			t.Errorf("%s: bad HOLD-1 target %+v", id, out.To)
		}
		return
	}
	checkValidTarget(t, cf, id, out.To)
}

// checkValidTarget mirrors smcheck's validTarget on the resolved target.
func checkValidTarget(t *testing.T, cf smcheck.Config, id string, to To) {
	t.Helper()
	var td *typeDef
	for i := range dimTypes {
		if dimTypes[i].Name == cf.Type {
			td = &dimTypes[i]
		}
	}
	switch to.State {
	case "done", "dropped":
		if to.Stage != "" || to.Reason != "" {
			t.Errorf("%s: terminal target has stage/reason %+v", id, to)
		}
	case "queued":
		if !slices.Contains(td.Stages, to.Stage) || to.Reason != "" {
			t.Errorf("%s: bad queued target %+v", id, to)
		}
	case "running":
		if !slices.Contains(td.RunningStages, to.Stage) {
			t.Errorf("%s: bad running target %+v", id, to)
		}
	case "waiting":
		ok := false
		for _, w := range dimWaitingReasons {
			if w.Name == to.Reason && slices.Contains(w.Types, cf.Type) && (w.Stages == nil || slices.Contains(w.Stages, to.Stage)) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s: invalid waiting target %+v for %+v", id, to, cf)
		}
	default:
		t.Errorf("%s: unknown target state %+v", id, to)
	}
}

func stepExhaustive(t *testing.T) (decided, ignored int) {
	sp := loadSpec(t)
	if len(sp.Configs) == 0 || len(sp.Events) == 0 {
		t.Fatal("empty configs/events")
	}
	for _, ev := range sp.Events {
		var dims []string
		for _, r := range sp.Rows {
			if r.Event == ev {
				dims = addDims(dims, r.Guard)
				if r.To.State == "escalate" {
					for _, e := range sp.Escalate {
						dims = addDims(dims, e.Guard)
					}
				}
			}
		}
		for _, cf := range sp.Configs {
			combos(dims, sp.DimValues, func(assign map[string]string, salt int) {
				req, e, counters := inputs(assign, salt)
				e.Name = ev
				step := StepView{State: cf.State, Stage: cf.Stage, Type: cf.Type, Reason: cf.Reason,
					Counters: counters, ResumeStage: firstStageOf(cf.Type)}
				out, err := Decide(step, req, e)
				if err != nil {
					t.Errorf("Decide(%+v, %v, %v): %v", cf, assign, ev, err)
					return
				}
				id, ign := sp.StepOracle(cf, ev, assign)
				if out.Ignored != ign || out.RowID != id {
					t.Errorf("%+v %s %v: Decide=(%q ignored=%v) smcheck=(%q ignored=%v)",
						cf, ev, assign, out.RowID, out.Ignored, id, ign)
					return
				}
				if ign {
					ignored++
					if len(out.Effects) != 0 || out.To != (To{}) {
						t.Errorf("ignored outcome carries data: %+v", out)
					}
					return
				}
				decided++
				checkStepOutcome(t, sp, cf, req, out, id)
			})
		}
	}
	return decided, ignored
}

func reqExhaustive(t *testing.T) (decided, ignored int) {
	sp := loadSpec(t)
	var events []string
	for _, r := range sp.HoldRows {
		if !slices.Contains(events, r.Event) {
			events = append(events, r.Event)
		}
	}
	for _, ig := range sp.HoldIgnored {
		for _, e := range ig.Events {
			if !slices.Contains(events, e) {
				events = append(events, e)
			}
		}
	}
	for _, ev := range events {
		var dims []string
		for _, r := range sp.HoldRows {
			if r.Event == ev {
				dims = addDims(dims, r.Guard)
				if r.To == "rescue_or_owner" {
					for _, e := range sp.HoldEscalate {
						dims = addDims(dims, e.Guard)
					}
				}
			}
		}
		for _, hold := range sp.HoldValues {
			combos(dims, sp.HoldDimValues, func(assign map[string]string, salt int) {
				req, e, _ := inputs(assign, salt)
				req.Hold = hold
				e.Name = ev
				out, err := DecideReq(req, e)
				if err != nil {
					t.Errorf("DecideReq(%s, %v, %s): %v", hold, assign, ev, err)
					return
				}
				id, ign := sp.ReqOracle(hold, ev, assign)
				if out.Ignored != ign || strings.TrimSuffix(out.RowID, "+roo") != id {
					t.Errorf("hold=%s %s %v: DecideReq=(%q ignored=%v) oracle=(%q ignored=%v)",
						hold, ev, assign, out.RowID, out.Ignored, id, ign)
					return
				}
				if ign {
					ignored++
					return
				}
				decided++
				if !slices.Contains(sp.HoldValues, out.To) {
					t.Errorf("%s: target %q is not a hold value", out.RowID, out.To)
				}
			})
		}
	}
	return decided, ignored
}

func TestDecideExhaustive(t *testing.T) {
	d, i := stepExhaustive(t)
	if d == 0 || i == 0 {
		t.Fatalf("degenerate run: decided=%d ignored=%d", d, i)
	}
}

func TestStateTableExhaustive(t *testing.T) {
	t.Run("step", func(t *testing.T) { stepExhaustive(t) })
	t.Run("req", func(t *testing.T) {
		d, i := reqExhaustive(t)
		if d == 0 || i == 0 {
			t.Fatalf("degenerate run: decided=%d ignored=%d", d, i)
		}
	})
}
