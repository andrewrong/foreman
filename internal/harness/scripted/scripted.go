// Package scripted is a fake model that is a real process (D-064). A JSON
// script says which files to write (result.json, code edits), which git
// commands to run (commits) and which stream-json to print, so the pipeline
// can run the full session protocol without Claude.
//
// The child is the host binary itself: Argv[0] is os.Args[0] and the child
// mode is selected by the FOREMAN_SCRIPTED_CHILD environment variable, which
// the host's main (or a test binary's TestMain) honours by calling
// MaybeRunChild before anything else. No extra cmd package is needed.
package scripted

import (
	"errors"
	"io"
	"iter"
	"os"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/resource/gate"
)

// Environment variables of the child protocol.
const (
	EnvChild  = "FOREMAN_SCRIPTED_CHILD"  // "1" selects child mode
	EnvScript = "FOREMAN_SCRIPTED_SCRIPT" // path of the script JSON
	EnvRunDir = "FOREMAN_RUN_DIR"
	EnvRunID  = "FOREMAN_RUN_ID"
	EnvModel  = "FOREMAN_SCRIPTED_MODEL" // RunSpec.Model, echoed in the stream so tests can see which model a Run was given
)

// ExtraScript is the RunSpec.Extra key holding the script path.
const ExtraScript = "script"

// Config configures the adapter.
type Config struct {
	Exe    string // host binary to re-exec; default os.Args[0]
	Script string // default script path; RunSpec.Extra["script"] overrides
}

// Adapter implements api.Adapter.
type Adapter struct {
	lease *gate.Lease
	cfg   Config
}

// New constructs the adapter; MD-3 requires a Lease.
func New(lease *gate.Lease, cfg Config) (*Adapter, error) {
	if lease == nil {
		return nil, errors.New("scripted: lease required (MD-3)")
	}
	if cfg.Exe == "" {
		cfg.Exe = os.Args[0]
	}
	return &Adapter{lease: lease, cfg: cfg}, nil
}

// Name implements api.Adapter.
func (*Adapter) Name() string { return "scripted" }

// Capabilities: like every harness, it cannot stop mid-run (PP-5); it can
// report windows, cost and skill calls when the script emits them.
func (*Adapter) Capabilities() api.Capabilities {
	return api.Capabilities{ReportsWindows: true, ReportsSkillCalls: true, ReportsCost: true}
}

// ParseStream: the child prints claude's stream-json.
func (*Adapter) ParseStream(r io.Reader) iter.Seq[api.Event] { return claude.ParseStream(r) }

// BuildInvocation implements api.Adapter.
func (a *Adapter) BuildInvocation(s api.RunSpec) (api.Invocation, error) {
	if !s.Role.Valid() {
		return api.Invocation{}, errors.New("scripted: unknown role")
	}
	if err := api.CheckBudgetFallback(a.Capabilities(), s); err != nil {
		return api.Invocation{}, err
	}
	script := a.cfg.Script
	if v := s.Extra[ExtraScript]; v != "" {
		script = v
	}
	if script == "" {
		return api.Invocation{}, errors.New("scripted: no script")
	}
	return api.Invocation{
		Argv: []string{a.cfg.Exe, "-foreman-scripted-child"},
		Env: map[string]string{
			EnvChild: "1", EnvScript: script, EnvRunDir: s.RunDir, EnvRunID: s.RunID, EnvModel: s.Model,
		},
		Timeout: s.Timeout,
	}, nil
}
