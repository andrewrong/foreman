package sched

import (
	"context"
	"time"

	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// The interfaces below are everything the scheduler asks of stages, the
// workspace and the forge (D-078, MD-12). The composition root (cmd/foreman)
// implements them over the stage, workspace, forge, harness and sandbox
// packages and injects them; this package imports none of those. Stages hand
// back event values (model.Event) and the scheduler alone calls
// state.Transition (MD-7, C-01).

// Roles of the Runs of session stages.
const (
	RoleCoder    = "coder"
	RoleReviewer = "reviewer"
)

// Call names who a forge operation or a Gate lease is made for.
type Call struct {
	Priority gate.Priority
	Caller   gate.Caller
}

// Slots are the Gate resource names a dispatch demands that belong to other
// modules: the forge's REST and write budgets and the repository's merge
// slot. The composition root fills them from the packages that own them; the
// merge slot key must be the one forge ingests base health under (RES-SLOTS,
// RES-BASE).
type Slots struct {
	REST, Write, Merge string
}

// Forge is what the scheduler needs from the forge.
type Forge interface {
	// EnsureFresh refreshes the rate-limit readings when stale (FG-2).
	EnsureFresh(ctx context.Context) error
	// RefreshBase polls the repository once so the base-health reading the
	// merge lease depends on is fresh.
	RefreshBase(ctx context.Context, call Call)
}

// DiffSpec selects the diff a reviewer reads. Base empty means the default
// branch of the mirror; Branch set means the requirement branch (the
// integrate step reviews the pull request) instead of the step's own ref.
type DiffSpec struct {
	StepID, Branch, Base string
}

// Workspace is what the scheduler needs from the repositories.
type Workspace interface {
	// CreateSessionRepo creates the requirement's session repository in dir
	// from the mirror (D-045) and returns its path.
	CreateSessionRepo(ctx context.Context, reqID, dir string) (string, error)
	// Diff returns the diff of a step; unreadable means empty.
	Diff(ctx context.Context, d DiffSpec) string
}

// Proc is a session process the scheduler started and waits for.
type Proc interface {
	Done() <-chan struct{}
}

// Acc is one acceptance item of a Brief.
type Acc struct{ ID, EARS string }

// StepLine is one line about another step of the requirement.
type StepLine struct{ ID, Type, Goal, State string }

// Failure is the open kind=check finding of the previous attempt (ACR-01).
type Failure struct {
	FailingTests []string
	LogTail      string
}

// FindingLine is one open finding.
type FindingLine struct{ ID, Severity, Location, Problem, CloseWhen string }

// Brief is what a session is told, gathered from the database (protocol-run
// PF-CTX); the stage renders it.
type Brief struct {
	Role, Constitution, Goal string
	Outcome                  []Acc
	Boundaries               []string
	StepID, StepGoal         string
	StepAcceptance           []Acc
	Others                   []StepLine
	LastFailure              *Failure
	OpenFindings             []FindingLine
	Diff                     string // reviewers only
}

// MergeBack is the squash merge a reviewer's approval of a build or fix step
// performs (nil for integrate steps).
type MergeBack struct {
	ReqID, StepID, SessionRepo, SessionBranch, Subject string
}

// SessionJob is one Run of a session stage (code, review).
type SessionJob struct {
	RunID, StepID, Role, Model, RunDir, SessionRepo string
	Acceptance                                      []string
	Brief                                           Brief
	Lease                                           *gate.Lease
	MergeBack                                       *MergeBack
}

// SessionStage is a stage that runs a model session in the background.
type SessionStage interface {
	// Timeout is how long a session may run.
	Timeout() time.Duration
	// Start prepares the Run and launches the session. beforeLaunch is called
	// after the preparation and before the process is forked (the scheduler
	// takes launch.lock there); an error from it is returned unchanged.
	Start(ctx context.Context, job SessionJob, beforeLaunch func() error) (Proc, error)
	// Collect reads what the session left and returns the event of the Run.
	// exited is the process this scheduler started, nil when unknown (after a
	// restart); timedOut says it was killed for running too long.
	Collect(ctx context.Context, job SessionJob, exited Proc, timedOut bool) (model.Event, error)
}

// CheckJob is one L0 run.
type CheckJob struct {
	RunID, StepID, SessionRepo, SessionBranch, RunDir string
}

// CheckStage runs L0 on the head of a step.
type CheckStage interface {
	Run(ctx context.Context, job CheckJob) (model.Event, error)
}

// IntegrateJob is one attempt of the integrate stage (push, pull request, L1).
type IntegrateJob struct {
	ReqID, Title, Body string
	PR                 int
	Call               Call
	DeadlinePassed     bool
	Reruns             map[string]int // E-PR.l1_reruns: head sha -> count
	MarkRerun          func(ctx context.Context, head string) error
}

// IntegrateResult is what the integrate stage learned; Event is nil when
// there is nothing to report yet.
type IntegrateResult struct {
	PR    int
	Head  string
	Event *model.Event
}

// IntegrateStage pushes the requirement branch, finds or opens the pull
// request and reads L1 once.
type IntegrateStage interface {
	Run(ctx context.Context, job IntegrateJob) (IntegrateResult, error)
}

// MergeJob is one attempt of the merge stage.
type MergeJob struct {
	ReqID                           string
	PR                              int
	Head                            string
	Call                            Call
	LastReviewed, GuardrailApproved string
	Reruns                          map[string]int
	MarkRerun                       func(ctx context.Context, head string) error
	DeadlinePassed                  bool
}

// MergeResult is what the merge stage learned; Head is the pull request head
// GitHub reported, which moves under update-branch.
type MergeResult struct {
	Head  string
	Event *model.Event
}

// MergeStage brings the pull request up to date and merges it.
type MergeStage interface {
	Run(ctx context.Context, job MergeJob) (MergeResult, error)
}
