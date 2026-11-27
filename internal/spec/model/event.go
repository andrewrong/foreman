package model

// Event names a stage returns to its caller (state-machine.yaml step_table
// events produced by the reconciler).
const (
	EventRunOK          = "run_ok"
	EventCheckPass      = "check_pass"
	EventCheckFail      = "check_fail"
	EventReviewApprove  = "review_approve"
	EventReviewChanges  = "review_changes"
	EventMergeBackConfl = "merge_back_conflict"
	EventL1Pass         = "l1_pass"
	EventL1Fail         = "l1_fail"
	EventCIProgress     = "ci_progress"
	EventLeaseWait      = "lease_wait"
	EventMerged         = "merged"
	EventMergeConflict  = "merge_conflict"
	EventSpecConflict   = "spec_conflict"
	EventInfra          = "infra"
	EventBadResult      = "bad_result"
)

// Event is the value a stage returns after it acted (MD-7: stage packages
// never import internal/pipeline; the scheduler converts the value into a
// state.Event and calls state.Transition). Only the fields the named event
// needs are set.
type Event struct {
	Name         string
	Head         string // head sha the event refers to (set:last_reviewed_sha=head)
	Detail       string // free text: why (bad_result, infra, merge conflict files)
	PR           int    // pull request number (l1_pass, merged)
	FailingTests []string
	LogTail      string // check_fail / l1_fail; the stage already cut it to <= 80 lines
	Findings     []Finding
	Usage        Usage
}

// Finding is one review finding carried by review_changes (PF-RESULT).
type Finding struct {
	Severity  string // blocking|minor
	Path      string
	Line      int
	Problem   string
	CloseWhen string
}

// Usage is the token usage of the Run behind an event, as the model reported
// it; the zero value means "not reported".
type Usage struct {
	Input, Output int64
}
