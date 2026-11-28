// Package api is the harness adapter interface (D-064, protocol-run.yaml):
// each model backend turns a RunSpec into an Invocation (argv, env additions,
// files, stdin) and parses the backend's output stream into typed events.
// The harness never launches processes: the caller hands the Invocation to
// sandbox/launch (both are layer 2 and may not import each other, MD-6).
package api

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"time"
)

// Role is Run.role.
type Role string

const (
	RoleIntake    Role = "intake"
	RolePlanner   Role = "planner"
	RoleCoder     Role = "coder"
	RoleReviewer  Role = "reviewer"
	RoleRescuer   Role = "rescuer"
	RoleDiagnoser Role = "diagnoser"
	RoleImprover  Role = "improver"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleIntake, RolePlanner, RoleCoder, RoleReviewer, RoleRescuer, RoleDiagnoser, RoleImprover:
		return true
	}
	return false
}

// Capabilities are the adapter capability bits (PP-5, D-044, D-051, D-059).
type Capabilities struct {
	CanAdvise         bool // session may consult an advisor model
	CanStopMidrun     bool // token cap can be enforced while the session runs
	ReportsWindows    bool // stream carries quota-window observations
	ReportsSkillCalls bool // stream reports Skill tool calls (D-059)
	ReportsCost       bool // final result carries usage and cost
}

// Skill is one injected skill: its name and the file holding its body.
type Skill struct {
	Name string
	Path string
}

// RunSpec is everything an adapter needs to build one session invocation.
// Model, advisor and skills are resolved by the caller (see Catalog.Plan).
type RunSpec struct {
	RunID string
	Role  Role

	Model        string
	Effort       string        // low|medium|high|xhigh|max; empty = CLI default
	AdvisorModel string        // empty = no advisor
	MaxTurns     int           // required unless the adapter can stop mid-run
	Timeout      time.Duration // required unless the adapter can stop mid-run
	TokenLimit   int64         // Run token cap; enforced per PP-5

	// Subagents enables the sub-agent tool. Default false (D-041).
	Subagents bool
	// Tools overrides the adapter's per-role tool list.
	Tools []string

	WorkDir string // session repo (cwd of the session)
	RunDir  string // run directory holding context.md, result.json

	// PromptFiles are concatenated in order: role prompt first, then
	// context.md (PF-CTX keeps stable content first for prompt caching).
	PromptFiles []string
	Skills      []Skill

	// Extra carries adapter-specific settings (scripted: "script").
	Extra map[string]string
}

// File is a file the caller must create before starting the process.
type File struct {
	Path    string // absolute path
	Content []byte
	Mode    uint32
}

// Invocation is the data boundary to sandbox/launch.
type Invocation struct {
	Argv    []string
	Env     map[string]string // additions to the from-scratch session env
	Stdin   []byte
	Files   []File
	Timeout time.Duration
	// ReadPaths are install paths the process must read (the harness binary
	// and its directory); the sandbox adds them to allowRead (D-080).
	ReadPaths []string
}

// Adapter is a model backend.
type Adapter interface {
	Name() string
	Capabilities() Capabilities
	BuildInvocation(RunSpec) (Invocation, error)
	// ParseStream decodes the session's stdout into events. Malformed lines
	// become ErrorEvent; the sequence ends at EOF.
	ParseStream(io.Reader) iter.Seq[Event]
}

// ErrBudgetFallback is returned when an adapter that cannot stop mid-run is
// given no max_turns or timeout (PP-5).
var ErrBudgetFallback = errors.New("harness: adapter cannot stop mid-run; max_turns and timeout are required")

// CheckBudgetFallback enforces PP-5: without can_stop_midrun the session must
// be bounded by max_turns and timeout.
func CheckBudgetFallback(c Capabilities, s RunSpec) error {
	if c.CanStopMidrun {
		return nil
	}
	if s.MaxTurns <= 0 || s.Timeout <= 0 {
		return fmt.Errorf("%w (run %s: max_turns=%d timeout=%s)", ErrBudgetFallback, s.RunID, s.MaxTurns, s.Timeout)
	}
	return nil
}

// StopMidrun reports whether a session that has consumed used tokens must be
// terminated now. Always false unless the adapter can stop mid-run.
func StopMidrun(c Capabilities, used, limit int64) bool {
	return c.CanStopMidrun && limit > 0 && used > limit
}

// BudgetExceeded is the settle-time check on actual usage (PP-5).
func BudgetExceeded(u Usage, limit int64) bool {
	return limit > 0 && u.Total() > limit
}
