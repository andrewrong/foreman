// Package claude is the harness adapter for the claude CLI (D-066, probe P2).
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/resource/gate"
)

// Config configures the adapter.
type Config struct {
	ReadFile func(string) ([]byte, error) // default os.ReadFile (prompt assembly)
	Tools    map[api.Role][]string        // per-role tool lists; default DefaultTools
}

// Command is the executable name looked up on foremand's own PATH (D-084).
const Command = "claude"

// ModelHost is the API host of the claude CLI (access.yaml R0).
const ModelHost = "api.anthropic.com"

// Locate finds claude on foremand's PATH and resolves every symlink on the
// way (D-084). The adapter does it again at every launch, so an upgrade that
// moves the link is picked up by the next session; the daemon calls it at
// startup to refuse to start when claude is missing.
func Locate() (string, error) {
	found, err := exec.LookPath(Command)
	if err != nil {
		return "", fmt.Errorf("claude: %q not found on foremand's PATH (D-084): %w", Command, err)
	}
	if !filepath.IsAbs(found) {
		if found, err = filepath.Abs(found); err != nil {
			return "", fmt.Errorf("claude: %w", err)
		}
	}
	real, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", fmt.Errorf("claude: resolve %s: %w", found, err)
	}
	return real, nil
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
	binary, err := Locate()
	if err != nil {
		return api.Invocation{}, err
	}
	argv := []string{
		binary, "-p",
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
	return api.Invocation{Argv: argv, Stdin: prompt, Timeout: s.Timeout, ReadPaths: []string{binary, filepath.Dir(binary)}, ModelHost: ModelHost}, nil
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
