// Package model holds the Go types of the entity model in
// spec/design/entities.yaml (E-REQ, E-SPEC, E-ACC, E-UNK, E-BUG, E-PROOF,
// E-REF, E-STEP). Enums are typed string constants.
package model

import "time"

type Kind string

const (
	KindFeature Kind = "feature"
	KindBug     Kind = "bug"
	KindChore   Kind = "chore"
)

type Source string

const (
	SourceOwner   Source = "owner"
	SourceNightly Source = "nightly"
	SourceProd    Source = "prod"
	SourceImprove Source = "improve"
)

type Priority string

const (
	P0 Priority = "P0"
	P1 Priority = "P1"
	P2 Priority = "P2"
	P3 Priority = "P3"
)

type Hold string

const (
	HoldNone   Hold = "none"
	HoldReplan Hold = "replan"
	HoldRescue Hold = "rescue"
	HoldOwner  Hold = "owner"
	HoldParked Hold = "parked"
)

type Trigger string

const (
	TriggerReplan Trigger = "replan_trigger"
	TriggerRescue Trigger = "rescue_trigger"
	TriggerOwner  Trigger = "owner_trigger"
)

type ReqStatus string

const (
	StatusDraft     ReqStatus = "draft"
	StatusSubmitted ReqStatus = "submitted"
)

type Size string

const (
	SizeS Size = "s"
	SizeM Size = "m"
	SizeL Size = "l"
)

// Phase is the derived Requirement phase (E-REQ derived_phase).
type Phase string

const (
	PhaseDraft       Phase = "draft"
	PhaseDropped     Phase = "dropped"
	PhaseParked      Phase = "parked"
	PhaseDone        Phase = "done"
	PhaseReporting   Phase = "reporting"
	PhaseIntegrating Phase = "integrating"
	PhasePlanning    Phase = "planning"
	PhaseBuilding    Phase = "building"
	PhaseStalled     Phase = "stalled"
)

type Verify string

const (
	VerifyTest    Verify = "test"
	VerifyCommand Verify = "command"
	VerifyReading Verify = "reading"
)

type CauseStatus string

const (
	CauseSuspected CauseStatus = "suspected"
	CauseProven    CauseStatus = "proven"
)

type ProofKind string

const (
	ProofRepro ProofKind = "repro"
	ProofCode  ProofKind = "code"
)

type StepType string

const (
	StepBuild     StepType = "build"
	StepFindCause StepType = "find_cause"
	StepFix       StepType = "fix"
	StepIntegrate StepType = "integrate"
)

type CodeRole string

const (
	RoleCoder     CodeRole = "coder"
	RoleDiagnoser CodeRole = "diagnoser"
)

type StepState string

const (
	StateQueued  StepState = "queued"
	StateRunning StepState = "running"
	StateWaiting StepState = "waiting"
	StateDone    StepState = "done"
	StateDropped StepState = "dropped"
)

type Stage string

const (
	StageCode   Stage = "code"
	StageCheck  Stage = "check"
	StageReview Stage = "review"
	StageMerge  Stage = "merge"
)

// WaitReason is the closed enum for Step.Reason when State is waiting;
// for dropped the reason is free text.
type WaitReason string

const (
	WaitDeps   WaitReason = "deps"
	WaitQuota  WaitReason = "quota"
	WaitInfra  WaitReason = "infra"
	WaitAccess WaitReason = "access"
	WaitRescue WaitReason = "rescue"
	WaitHold   WaitReason = "hold"
	WaitCI     WaitReason = "ci"
	WaitFix    WaitReason = "fix"
)

// ReqCounters is E-REQ req_counters; limits live in state-machine dims.
type ReqCounters struct {
	Replans         int `yaml:"replans" json:"replans"`
	PlannerFailures int `yaml:"planner_failures" json:"planner_failures"`
	ReqRescues      int `yaml:"req_rescues" json:"req_rescues"`
	FixSteps        int `yaml:"fix_steps" json:"fix_steps"`
}

type Budget struct {
	Tokens   int `yaml:"tokens" json:"tokens"`
	QuotaPct int `yaml:"quota_pct" json:"quota_pct"`
}

// Requirement is E-REQ.
type Requirement struct {
	ID              string      `yaml:"id" json:"id"`
	Kind            Kind        `yaml:"kind" json:"kind"`
	Source          Source      `yaml:"source" json:"source"`
	Priority        Priority    `yaml:"priority" json:"priority"`
	SpecVersion     int         `yaml:"spec_version" json:"spec_version"`
	Spec            Spec        `yaml:"spec" json:"spec"`
	Branch          string      `yaml:"branch" json:"branch"`
	Parked          bool        `yaml:"parked" json:"parked"`
	Hold            Hold        `yaml:"hold" json:"hold"`
	PendingTriggers []Trigger   `yaml:"pending_triggers" json:"pending_triggers"`
	ReqCounters     ReqCounters `yaml:"req_counters" json:"req_counters"`
	LastL0SHA       string      `yaml:"last_l0_sha,omitempty" json:"last_l0_sha,omitempty"`
	SessionRepo     string      `yaml:"session_repo,omitempty" json:"session_repo,omitempty"`
	DroppedReason   string      `yaml:"dropped_reason,omitempty" json:"dropped_reason,omitempty"`
	Status          ReqStatus   `yaml:"status" json:"status"`
	Size            Size        `yaml:"size" json:"size"`
	Budget          Budget      `yaml:"budget" json:"budget"`
}

// Spec is E-SPEC.
type Spec struct {
	Title      string       `yaml:"title" json:"title"`
	Goal       string       `yaml:"goal" json:"goal"`
	Current    []Ref        `yaml:"current" json:"current"`
	Change     []string     `yaml:"change" json:"change"`
	Outcome    []Acceptance `yaml:"outcome" json:"outcome"`
	Boundaries []string     `yaml:"boundaries,omitempty" json:"boundaries,omitempty"`
	Unknowns   []Unknown    `yaml:"unknowns,omitempty" json:"unknowns,omitempty"`
	Hints      []string     `yaml:"hints,omitempty" json:"hints,omitempty"`
	Bug        *BugInfo     `yaml:"bug,omitempty" json:"bug,omitempty"`
}

// Acceptance is E-ACC.
type Acceptance struct {
	ID           string `yaml:"id" json:"id"`
	EARS         string `yaml:"ears" json:"ears"`
	Verify       Verify `yaml:"verify" json:"verify"`
	VerifyDetail string `yaml:"verify_detail" json:"verify_detail"`
	SandboxOK    bool   `yaml:"sandbox_ok" json:"sandbox_ok"`
}

// Unknown is E-UNK.
type Unknown struct {
	ID         string `yaml:"id" json:"id"`
	Question   string `yaml:"question" json:"question"`
	AnsweredBy string `yaml:"answered_by,omitempty" json:"answered_by,omitempty"`
}

// BugInfo is E-BUG.
type BugInfo struct {
	Observed    string      `yaml:"observed" json:"observed"`
	Expected    string      `yaml:"expected" json:"expected"`
	Repro       []string    `yaml:"repro" json:"repro"`
	Cause       string      `yaml:"cause,omitempty" json:"cause,omitempty"`
	CauseStatus CauseStatus `yaml:"cause_status" json:"cause_status"`
	CauseProof  *Proof      `yaml:"cause_proof,omitempty" json:"cause_proof,omitempty"`
}

// Proof is E-PROOF.
type Proof struct {
	Kind       ProofKind `yaml:"kind" json:"kind"`
	Cmd        string    `yaml:"cmd,omitempty" json:"cmd,omitempty"`
	OutputLine string    `yaml:"output_line,omitempty" json:"output_line,omitempty"`
	Path       string    `yaml:"path,omitempty" json:"path,omitempty"`
	Line       int       `yaml:"line,omitempty" json:"line,omitempty"`
}

// Ref is E-REF.
type Ref struct {
	Module string `yaml:"module" json:"module"`
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Symbol string `yaml:"symbol,omitempty" json:"symbol,omitempty"`
}

// Counters is E-CNT: fields, limits and reset rules are defined by
// state-machine.yaml dims.counters, so they are kept as a name -> value map.
type Counters map[string]int

// Step is E-STEP. Reason holds a WaitReason value when State is waiting
// and free text when State is dropped.
type Step struct {
	ID              string    `yaml:"id" json:"id"`
	ReqID           string    `yaml:"req_id" json:"req_id"`
	Type            StepType  `yaml:"type" json:"type"`
	CodeRole        CodeRole  `yaml:"code_role" json:"code_role"`
	Goal            string    `yaml:"goal" json:"goal"`
	Acceptance      []string  `yaml:"acceptance" json:"acceptance"`
	DependsOn       []string  `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Touches         []string  `yaml:"touches" json:"touches"`
	State           StepState `yaml:"state" json:"state"`
	Stage           Stage     `yaml:"stage" json:"stage"`
	ResumeStage     Stage     `yaml:"resume_stage,omitempty" json:"resume_stage,omitempty"`
	Reason          string    `yaml:"reason,omitempty" json:"reason,omitempty"`
	Deadline        time.Time `yaml:"deadline,omitempty" json:"deadline,omitempty"`
	Counters        Counters  `yaml:"counters" json:"counters"`
	SessionRepo     string    `yaml:"session_repo,omitempty" json:"session_repo,omitempty"`
	Commit          string    `yaml:"commit,omitempty" json:"commit,omitempty"`
	LastReviewedSHA string    `yaml:"last_reviewed_sha,omitempty" json:"last_reviewed_sha,omitempty"`
}
