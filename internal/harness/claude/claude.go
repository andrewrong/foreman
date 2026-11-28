// Package claude is the harness adapter for the claude CLI (D-066, probe P2).
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/resource/gate"
)

// Config configures the adapter.
type Config struct {
	Binary   string                       // default "claude"
	ReadFile func(string) ([]byte, error) // default os.ReadFile (prompt assembly)
	Tools    map[api.Role][]string        // per-role tool lists; default DefaultTools
}

// DefaultTools are the built-in tools per role (D-066 ②). Every role writes
// result.json, so Write is always present. Reviewer and planner get no Edit.
// The sub-agent tool ("Task") is added only when RunSpec.Subagents is set.
func DefaultTools() map[api.Role][]string {
	return map[api.Role][]string{
		api.RoleIntake:    {"Read", "Write", "Glob", "Grep"},
		api.RolePlanner:   {"Read", "Write", "Glob", "Grep", "Bash"},
		api.RoleCoder:     {"Bash", "Read", "Edit", "Write", "Glob", "Grep"},
		api.RoleReviewer:  {"Bash", "Read", "Write", "Glob", "Grep"},
		api.RoleRescuer:   {"Bash", "Read", "Write", "Glob", "Grep"},
		api.RoleDiagnoser: {"Bash", "Read", "Write", "Glob", "Grep"},
		api.RoleImprover:  {"Bash", "Read", "Edit", "Write", "Glob", "Grep"},
	}
}

// SubagentTool is the claude tool name that spawns sub-agents (D-041).
const SubagentTool = "Task"

// Adapter implements api.Adapter for claude.
type Adapter struct {
	lease *gate.Lease
	cfg   Config
}

// New constructs the adapter; MD-3 requires a Lease.
func New(lease *gate.Lease, cfg Config) (*Adapter, error) {
	if lease == nil {
		return nil, errors.New("claude: lease required (MD-3)")
	}
	if cfg.Binary == "" {
		cfg.Binary = "claude"
	}
	if cfg.ReadFile == nil {
		cfg.ReadFile = os.ReadFile
	}
	return &Adapter{lease: lease, cfg: cfg}, nil
}

// Lease returns the lease the adapter was constructed with.
func (a *Adapter) Lease() *gate.Lease { return a.lease }

// Name implements api.Adapter.
func (*Adapter) Name() string { return "claude" }

// Capabilities: can_stop_midrun stays false until a real run proves a
// mid-stream stop works (PP-5, ACR-40).
func (*Adapter) Capabilities() api.Capabilities {
	return api.Capabilities{
		CanAdvise: true, CanStopMidrun: false,
		ReportsWindows: true, ReportsSkillCalls: true, ReportsCost: true,
	}
}

// ParseStream implements api.Adapter.
func (*Adapter) ParseStream(r io.Reader) iter.Seq[api.Event] { return ParseStream(r) }

func (a *Adapter) tools(s api.RunSpec) []string {
	t := s.Tools
	if t == nil {
		t = a.cfg.Tools[s.Role]
	}
	if t == nil {
		t = DefaultTools()[s.Role]
	}
	out := append([]string(nil), t...)
	if s.Subagents {
		out = append(out, SubagentTool)
	}
	return out
}

// BuildInvocation implements api.Adapter. The prompt travels on stdin so it
// never lands in argv (process listings) and has no length limit.
func (a *Adapter) BuildInvocation(s api.RunSpec) (api.Invocation, error) {
	if !s.Role.Valid() {
		return api.Invocation{}, fmt.Errorf("claude: unknown role %q", s.Role)
	}
	if s.Model == "" {
		return api.Invocation{}, errors.New("claude: model required")
	}
	if err := api.CheckBudgetFallback(a.Capabilities(), s); err != nil {
		return api.Invocation{}, err
	}
	tools := a.tools(s)
	if len(tools) == 0 {
		return api.Invocation{}, fmt.Errorf("claude: no tools for role %s", s.Role)
	}
	argv := []string{
		a.cfg.Binary, "-p",
		"--output-format", "stream-json", "--verbose",
		"--model", s.Model,
		"--max-turns", strconv.Itoa(s.MaxTurns),
		"--strict-mcp-config", "--disable-slash-commands",
		"--setting-sources", "",
		"--tools", strings.Join(tools, ","),
		"--permission-mode", "bypassPermissions", // inside the OS sandbox (D-056)
	}
	if s.Effort != "" {
		argv = append(argv, "--effort", s.Effort)
	}
	if s.AdvisorModel != "" {
		settings, err := json.Marshal(map[string]string{"advisorModel": s.AdvisorModel})
		if err != nil {
			return api.Invocation{}, err
		}
		argv = append(argv, "--settings", string(settings))
	}
	prompt, err := a.assemblePrompt(s)
	if err != nil {
		return api.Invocation{}, err
	}
	return api.Invocation{Argv: argv, Stdin: prompt, Timeout: s.Timeout, ReadPaths: a.readPaths()}, nil
}

// readPaths declares what the sandbox must let the CLI read: the real binary
// (a symlink is followed: the link's directory is not the install directory)
// and its directory. A bare command name or a binary that cannot be resolved
// declares nothing; the owner then lists the install path in
// sandbox.read_paths, and policy.New refuses a path that does not exist.
func (a *Adapter) readPaths() []string {
	if !filepath.IsAbs(a.cfg.Binary) {
		return nil
	}
	real, err := filepath.EvalSymlinks(a.cfg.Binary)
	if err != nil {
		return nil
	}
	return []string{real, filepath.Dir(real)}
}

// assemblePrompt builds role prompt, then the role's skills, then the
// remaining files (context.md): stable content first for prompt caching
// (PF-CTX). Skills are injected as text because --disable-slash-commands
// turns off native skill loading (D-066).
func (a *Adapter) assemblePrompt(s api.RunSpec) ([]byte, error) {
	if len(s.PromptFiles) == 0 {
		return nil, errors.New("claude: at least one prompt file required")
	}
	var b strings.Builder
	part := func(path string) error {
		data, err := a.cfg.ReadFile(path)
		if err != nil {
			return fmt.Errorf("claude: read %s: %w", path, err)
		}
		b.WriteString(strings.TrimRight(string(data), "\n"))
		b.WriteString("\n\n")
		return nil
	}
	if err := part(s.PromptFiles[0]); err != nil {
		return nil, err
	}
	for _, sk := range s.Skills {
		data, err := a.cfg.ReadFile(sk.Path)
		if err != nil {
			return nil, fmt.Errorf("claude: read skill %s: %w", sk.Name, err)
		}
		fmt.Fprintf(&b, "<skill name=%q>\n%s\n</skill>\n\n", sk.Name, strings.TrimRight(string(data), "\n"))
	}
	for _, p := range s.PromptFiles[1:] {
		if err := part(p); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}
