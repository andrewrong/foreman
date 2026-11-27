package state

import (
	"fmt"
	"slices"
	"strings"
)

// expandGuards merges an escalate expansion's guard over a row's guard: a
// dimension present in both takes the expansion's value, others are appended
// (the same rule smcheck applies).
func expandGuards(base, over []guard) []guard {
	g := slices.Clone(base)
	for _, x := range over {
		if i := slices.IndexFunc(g, func(y guard) bool { return y.Dim == x.Dim }); i >= 0 {
			g[i].Val = x.Val
		} else {
			g = append(g, x)
		}
	}
	return g
}

// xrows is step_table with every to: escalate row replaced by its expansions.
var xrows = expandRows()

func expandRows() []stepRow {
	var out []stepRow
	for _, r := range stepRows {
		if r.To.State != "escalate" {
			out = append(out, r)
			continue
		}
		for _, e := range escRows {
			x := r
			x.ID = r.ID + "+esc"
			x.Guard = expandGuards(r.Guard, e.Guard)
			x.To = e.To
			x.Effects = append(slices.Clone(r.Effects), e.Effects...)
			out = append(out, x)
		}
	}
	return out
}

// Derived once from the table, for the rule-based ignored entries.
var exitByReason, runningEvents = deriveExits()

func deriveExits() (map[string][]string, []string) {
	by := map[string][]string{}
	var running []string
	for _, r := range stepRows {
		if r.Reason != "" {
			by[r.Reason] = append(by[r.Reason], r.Event)
		}
		if slices.Contains(r.States, "running") {
			running = append(running, r.Event)
		}
	}
	return by, running
}

func (r *stepRow) matchesConfig(s StepView) bool {
	if !slices.Contains(r.States, s.State) {
		return false
	}
	if r.Reason != "" && r.Reason != s.Reason {
		return false
	}
	if !slices.Contains(r.Types, s.Type) {
		return false
	}
	return s.Stage == "" || r.Stages == nil || slices.Contains(r.Stages, s.Stage)
}

func isIgnored(s StepView, ev string) bool {
	for _, ig := range ignoreEntries {
		if ig.States != nil {
			if !slices.Contains(ig.States, s.State) {
				continue
			}
			if ig.AllEvents || slices.Contains(ig.Events, ev) {
				return true
			}
			continue
		}
		switch ig.Rule {
		case "waiting_foreign_exit":
			if s.State != "waiting" || slices.Contains(exitByReason[s.Reason], ev) {
				continue
			}
			for reason, evs := range exitByReason {
				if reason != s.Reason && slices.Contains(evs, ev) {
					return true
				}
			}
		case "running_foreign_result":
			if s.State == "running" && slices.Contains(runningEvents, ev) {
				return true
			}
		}
	}
	return false
}

func holdIsSet(h string) bool { return h != "" && h != HoldNone }

// counterIs reports below/at_cap for a counter value against its cap. A
// counter at or above its cap is at_cap. ok is false for an unknown counter.
func counterIs(caps map[string]int, counts map[string]int, name string) (string, bool) {
	limit, ok := caps[name]
	if !ok {
		return "", false
	}
	if counts[name] >= limit {
		return "at_cap", true
	}
	return "below", true
}

// dimValue computes the value of one guard dimension from the inputs.
func dimValue(dim string, s *StepView, req ReqView, ev Event) string {
	if kind, name, ok := strings.Cut(dim, ":"); ok {
		var v string
		switch kind {
		case "counter":
			if s != nil {
				v, _ = counterIs(dimCounters, s.Counters, name)
			}
		case "req_counter":
			v, _ = counterIs(dimReqCounters, req.ReqCounters, name)
		}
		return v
	}
	switch dim {
	case "hold":
		if holdIsSet(req.Hold) {
			return "set"
		}
		return HoldNone
	case "decision":
		return ev.Decision
	case "answer":
		return ev.Answer
	case "tier":
		return ev.Tier
	case "single_step":
		return ev.SingleStep
	case "budget":
		return ev.Budget
	case "deps":
		return ev.Deps
	case "lease":
		return ev.Lease
	}
	return ""
}

func guardsHold(g []guard, s *StepView, req ReqView, ev Event) bool {
	for _, d := range g {
		if dimValue(d.Dim, s, req, ev) != d.Val {
			return false
		}
	}
	return true
}

func firstStageOf(typ string) string {
	for _, t := range dimTypes {
		if t.Name == typ {
			return t.FirstStage
		}
	}
	return ""
}

func resolveTo(s StepView, t target) (To, error) {
	switch t.State {
	case "done", "dropped":
		return To{State: t.State}, nil
	case "queued":
		stage := t.Stage
		switch stage {
		case "same":
			stage = s.Stage
		case "first_stage":
			stage = firstStageOf(s.Type)
		case "resume_stage":
			if s.ResumeStage == "" {
				return To{}, fmt.Errorf("state: target queued(resume_stage) but step has no resume stage")
			}
			stage = s.ResumeStage
		}
		return To{State: "queued", Stage: stage}, nil
	case "running":
		stage := t.Stage
		if stage == "same" || stage == "" {
			stage = s.Stage
		}
		return To{State: "running", Stage: stage}, nil
	case "waiting":
		return To{State: "waiting", Stage: s.Stage, Reason: t.Reason}, nil
	}
	return To{}, fmt.Errorf("state: unresolvable target state %q", t.State)
}

// Decide computes the transition of one step for one event: the unique
// matching step_table row (escalate expanded), its resolved target, and its
// effects, with HOLD-1 applied. Events the table lists as ignored for the
// step's configuration yield Ignored=true. A (config, event) with neither a
// row nor an ignore entry, or whose guard inputs select no row or more than
// one, is an error; for valid inputs that never happens.
func Decide(step StepView, req ReqView, ev Event) (Outcome, error) {
	var cands, hits []*stepRow
	for i := range xrows {
		r := &xrows[i]
		if r.Event == ev.Name && r.matchesConfig(step) {
			cands = append(cands, r)
			if guardsHold(r.Guard, &step, req, ev) {
				hits = append(hits, r)
			}
		}
	}
	if len(cands) == 0 {
		if isIgnored(step, ev.Name) {
			return Outcome{Ignored: true}, nil
		}
		return Outcome{}, fmt.Errorf("state: no row and not ignored: %s/%s/%s/%s event=%s",
			step.State, step.Stage, step.Type, step.Reason, ev.Name)
	}
	if len(hits) != 1 {
		return Outcome{}, fmt.Errorf("state: %d rows match %s/%s/%s/%s event=%s (want exactly 1)",
			len(hits), step.State, step.Stage, step.Type, step.Reason, ev.Name)
	}
	r := hits[0]
	to, err := resolveTo(step, r.To)
	if err != nil {
		return Outcome{}, fmt.Errorf("%s: %w", r.ID, err)
	}
	out := Outcome{RowID: r.ID, To: to, Effects: slices.Clone(r.Effects)}
	// HOLD-1: with a requirement hold, queued(X) becomes waiting(hold) with
	// resume_stage = X.
	if holdIsSet(req.Hold) && to.State == "queued" {
		out.To = To{State: "waiting", Stage: to.Stage, Reason: "hold", ResumeStage: to.Stage}
		out.HoldRewritten = true
	}
	return out, nil
}
