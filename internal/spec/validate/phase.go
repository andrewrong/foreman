package validate

import "github.com/andrewrong/foreman/internal/spec/model"

// PhaseInput is the summary of facts DerivedPhase needs. Every field is
// a closed value (bool or enum) so the function can be tested exhaustively.
type PhaseInput struct {
	Status               model.ReqStatus
	HasDroppedReason     bool // dropped_reason non-empty
	Parked               bool
	Hold                 model.Hold
	HasPR                bool // at least one PR exists
	AllPRsMerged         bool // meaningful only when HasPR
	ReportGenerated      bool // feedback report exists
	HasSteps             bool
	PlannerRunActive     bool // a planner Run is in progress
	NonTerminalIntegrate bool // an integrate step not in done/dropped
	NonTerminalWork      bool // a build/find_cause/fix step not in done/dropped
}

// StepFlags summarises steps into the HasSteps / NonTerminalIntegrate /
// NonTerminalWork inputs. Terminal states are done and dropped.
func StepFlags(steps []model.Step) (hasSteps, nonTermIntegrate, nonTermWork bool) {
	for _, s := range steps {
		hasSteps = true
		if s.State == model.StateDone || s.State == model.StateDropped {
			continue
		}
		if s.Type == model.StepIntegrate {
			nonTermIntegrate = true
		} else {
			nonTermWork = true
		}
	}
	return
}

// DerivedPhase implements E-REQ derived_phase: the first matching row in
// order wins and the last row (stalled) holds unconditionally, so every
// input maps to exactly one phase.
func DerivedPhase(in PhaseInput) model.Phase {
	prsMerged := in.HasPR && in.AllPRsMerged
	switch {
	case in.Status == model.StatusDraft:
		return model.PhaseDraft
	case in.HasDroppedReason:
		return model.PhaseDropped
	case in.Parked:
		return model.PhaseParked
	case prsMerged && in.ReportGenerated:
		return model.PhaseDone
	case prsMerged:
		return model.PhaseReporting
	case in.NonTerminalIntegrate:
		return model.PhaseIntegrating
	case in.Hold == model.HoldReplan, !in.HasSteps && !in.PlannerRunActive:
		return model.PhasePlanning
	case in.NonTerminalWork:
		return model.PhaseBuilding
	default:
		return model.PhaseStalled
	}
}
