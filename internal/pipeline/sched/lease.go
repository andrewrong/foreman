package sched

import "github.com/andrewrong/foreman/internal/resource/gate"

// stageRole is the Run role of the stage a step is queued at.
func stageRole(typ, stage string) string {
	switch {
	case stage == "code":
		return "coder"
	case stage == "review":
		return "reviewer"
	case stage == "check" && typ == "integrate":
		return "ci_watch"
	case stage == "check":
		return "check"
	}
	return "merge"
}

// demand is the Gate Demand of dispatching a step at its current stage.
func (s *Scheduler) demand(st stepRow, req reqRow) gate.Demand {
	var items []gate.Item
	switch {
	case st.Stage == "code" || st.Stage == "review":
		items = []gate.Item{{Resource: "local/slots/session", Amount: 1}}
	case st.Stage == "check" && st.Type != "integrate":
		items = []gate.Item{{Resource: "local/slots/l0-check", Amount: 1}}
	case st.Stage == "check":
		items = []gate.Item{{Resource: s.d.Slots.REST, Amount: 1}}
	default: // merge: S08 lease holds the merge slot (+ base health) and content-create
		items = []gate.Item{{Resource: s.d.Slots.Merge, Amount: 1}, {Resource: s.d.Slots.Write, Amount: 1}}
	}
	return gate.Demand{
		Items:    items,
		Priority: priorityOf(req.Priority),
		Caller: gate.Caller{Project: s.d.Project, RequirementID: req.ID, StepID: st.ID, Stage: st.Stage,
			Role: stageRole(st.Type, st.Stage)},
		Essential: st.Stage == "merge",
	}
}

func priorityOf(p string) gate.Priority {
	switch p {
	case "P0":
		return gate.P0
	case "P1":
		return gate.P1
	case "P3":
		return gate.P3
	}
	return gate.P2
}

func (s *Scheduler) opsCall(req reqRow, st stepRow) Call {
	return Call{Priority: priorityOf(req.Priority), Caller: gate.Caller{Project: s.d.Project, RequirementID: req.ID,
		StepID: st.ID, Stage: st.Stage, Role: stageRole(st.Type, st.Stage)}}
}
