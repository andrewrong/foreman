// Package state holds the pure decision functions of the step state machine
// (spec/design/state-machine.yaml, tables A and B). Decide and DecideReq map a
// current view plus an event to an outcome; they do no I/O and execute no
// effects (TX-1: effects are returned as data for the transaction layer and
// the reconciler).
package state

import "time"

//go:generate go run ./tablegen

// Hold values of Requirement.hold (req_hold.values). A step-table guard
// hold: set means any value except HoldNone.
const (
	HoldNone   = "none"
	HoldReplan = "replan"
	HoldRescue = "rescue"
	HoldOwner  = "owner"
	HoldParked = "parked"
)

// StepView is the part of a step the step table reads. Counters holds the
// step-level counters by name; a missing counter counts as zero.
// ResumeStage is the stage recorded when the step entered waiting(hold); it
// is only read by rows whose target is queued(resume_stage).
type StepView struct {
	State       string
	Stage       string
	Type        string
	Reason      string
	ResumeStage string
	Counters    map[string]int
}

// ReqView is the part of a requirement the tables read. Hold takes the
// req_hold.values; the empty string is treated as HoldNone. "parked" is a
// hold value (the table keys on it), not a separate flag. ReqCounters holds
// the requirement-level counters by name.
type ReqView struct {
	Hold        string
	ReqCounters map[string]int
}

// Event is an event name plus the guard inputs the tables may consult. Only
// the inputs a matching row actually guards on are read; unused ones may be
// left empty.
type Event struct {
	Name       string
	Decision   string // guard_enums.step_decision (table A) / req_decision (table B)
	Answer     string // guard_enums.answer
	Tier       string // R1|R2|R3
	SingleStep string // one|many
	Budget     string // ok|over
	Deps       string // done|pending
	Lease      string // ok|wait

	// Payload read by the effect executors (Transition, TransitionReq). The
	// tables never read these.

	Head         string      // head sha, set:last_reviewed_sha=head
	Detail       string      // free-text trigger reason (planner/rescuer input, denial reason)
	Guidance     string      // rescuer guidance (inject_guidance)
	Resource     string      // resource of an access request (grant_access, inject_denial)
	DropReason   string      // dropped_reason, required by drop_all_steps (ACR-05)
	Subject      string      // step id the trigger concerns (owner_trigger sent directly)
	AskID        string      // owner_answer / owner_retry: which ask; empty = the only open ask
	AnswerOption string      // option id chosen (defaults to Answer)
	ResetsAt     time.Time   // quota reset time, for the waiting(quota) deadline
	Findings     []FindingIn // add_findings
	FailingTests []string    // add_findings:check
	LogTail      string      // add_findings:check; already redacted by the caller (privguard)
	Plan         *PlanDiff   // apply_plan_diff
	Ask          *AskContent // create_owner_ask text; defaults are generated when nil
}

// To is the resolved target of a step transition. Stage is concrete (same,
// first_stage and resume_stage are resolved). ResumeStage is set only when
// HOLD-1 rewrote a queued target.
type To struct {
	State       string
	Stage       string
	Reason      string
	ResumeStage string
}

// Outcome is the result of Decide. RowID is the step_table id; rows reached
// through the escalate pseudo-target carry "<id>+esc". Effects are the
// verbatim effect strings of the row (plus those of the escalate expansion).
// HoldRewritten reports that HOLD-1 replaced a queued target. When Ignored,
// RowID is empty and there are no effects.
//
// Causes lists the counters that were at_cap in the matched row's guards
// (expanded escalate rows included), as "<name>" without the guard kind.
// Reqs are the table B rows hit by req_hold effects, in execution order
// (Transition only).
type Outcome struct {
	RowID         string
	To            To
	Effects       []string
	Ignored       bool
	HoldRewritten bool
	Causes        []string
	Reqs          []ReqOutcome
}

// ReqOutcome is the result of DecideReq. RowID is the req_hold row id, with
// "+roo" appended for rows reached through rescue_or_owner. To is the new
// hold value.
//
// Causes is as in Outcome. Followups are table B rows run for pending
// triggers once the hold returned to none (TransitionReq only).
type ReqOutcome struct {
	RowID     string
	To        string
	Effects   []string
	Ignored   bool
	Causes    []string
	Followups []ReqOutcome
}

// Row types of the generated tables.

type guard struct{ Dim, Val string }

type target struct{ State, Stage, Reason string }

type stepRow struct {
	ID, Event string
	States    []string
	Reason    string
	Types     []string
	Stages    []string
	Guard     []guard
	To        target
	Effects   []string
}

type escRow struct {
	Guard   []guard
	To      target
	Effects []string
}

type ignoreEntry struct {
	States    []string
	Events    []string
	AllEvents bool
	Rule      string
}

type typeDef struct {
	Name          string
	Stages        []string
	RunningStages []string
	FirstStage    string
}

type waitingReason struct {
	Name   string
	Types  []string
	Stages []string
}

type holdRow struct {
	ID, Event string
	From      []string
	Guard     []guard
	To        string
	Effects   []string
}

type holdEsc struct {
	Guard   []guard
	To      string
	Effects []string
}

type holdIgnore struct{ From, Events []string }
