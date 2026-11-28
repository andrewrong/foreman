package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/resource/gate"
)

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

func testLease(t *testing.T) *gate.Lease {
	t.Helper()
	// The adapters only require a non-nil Lease (MD-3); building a real one
	// needs resource/ledger, which harness must not import (MD-6).
	return &gate.Lease{}
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := m[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
}

func newAdapter(t *testing.T, fs map[string]string) *Adapter {
	t.Helper()
	useFakeClaude(t)
	a, err := New(testLease(t), Config{ReadFile: files(fs)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func promptFS() map[string]string {
	return map[string]string{
		"role.md": "ROLE PROMPT\n", "context.md": "CONTEXT\n", "tdd.md": "TDD BODY\n",
	}
}

func coderSpec() api.RunSpec {
	return api.RunSpec{
		RunID: "R-1/S1/r1", Role: api.RoleCoder, Model: "sonnet", AdvisorModel: "opus",
		MaxTurns: 40, Timeout: 30 * time.Minute, Effort: "high",
		PromptFiles: []string{"role.md", "context.md"},
		Skills:      []api.Skill{{Name: "tdd", Path: "tdd.md"}},
	}
}

func TestHarnessClaudeArgv(t *testing.T) {
	real := useFakeClaude(t)
	a, err := New(testLease(t), Config{ReadFile: files(promptFS())})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.BuildInvocation(coderSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		real, "-p", "--output-format", "stream-json", "--verbose",
		"--model", "sonnet", "--max-turns", "40",
		"--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
		"--tools", "Bash,Read,Edit,Write,Glob,Grep",
		"--permission-mode", "bypassPermissions",
		"--effort", "high",
		"--settings", `{"advisorModel":"opus"}`,
	}
	if !reflect.DeepEqual(inv.Argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", inv.Argv, want)
	}
	if inv.Timeout != 30*time.Minute {
		t.Fatalf("timeout %v", inv.Timeout)
	}
	for _, bad := range []string{"--bare", "--dangerously-skip-permissions"} {
		for _, x := range inv.Argv {
			if x == bad {
				t.Fatalf("argv contains %s", bad)
			}
		}
	}
	// advisor is passed via --settings only, never as a tool (D-066 ②)
	if strings.Contains(strings.Join(inv.Argv, " "), "advisor,") {
		t.Fatal("advisor listed in --tools")
	}
}

func TestArgvOptionalParts(t *testing.T) {
	a := newAdapter(t, promptFS())
	s := coderSpec()
	s.AdvisorModel, s.Effort, s.Role = "", "", api.RoleReviewer
	inv, err := a.BuildInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inv.Argv, " ")
	if strings.Contains(joined, "--settings") || strings.Contains(joined, "--effort") {
		t.Fatalf("no advisor/effort expected: %s", joined)
	}
	if !strings.Contains(joined, "--tools Bash,Read,Write,Glob,Grep") {
		t.Fatalf("reviewer tools: %s", joined)
	}
	s.Tools = []string{"Read"}
	inv, _ = a.BuildInvocation(s)
	if !strings.Contains(strings.Join(inv.Argv, " "), "--tools Read ") {
		t.Fatalf("tools override ignored: %q", inv.Argv)
	}
}

func TestEscalatedRunUsesOpusArgv(t *testing.T) {
	cat := loadCatalog(t)
	p, err := cat.Plan(api.RoleCoder, api.PlanInput{CheckFail: 2})
	if err != nil || !p.Upgraded {
		t.Fatalf("plan %+v %v", p, err)
	}
	s := coderSpec()
	s.Model, s.AdvisorModel = p.Model, p.AdvisorModel
	inv, err := newAdapter(t, promptFS()).BuildInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	i := indexOf(inv.Argv, "--model")
	if inv.Argv[i+1] != "opus" {
		t.Fatalf("model %q, want opus", inv.Argv[i+1])
	}
}

func loadCatalog(t *testing.T) *api.Catalog {
	t.Helper()
	data, err := os.ReadFile("../../../spec/design/skills.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := api.ParseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestHarnessSubagentDefaultOff(t *testing.T) {
	a := newAdapter(t, promptFS())
	for role := range DefaultTools() {
		s := coderSpec()
		s.Role = role
		inv, err := a.BuildInvocation(s)
		if err != nil {
			t.Fatal(err)
		}
		tools := strings.Split(inv.Argv[indexOf(inv.Argv, "--tools")+1], ",")
		for _, tl := range tools {
			if tl == SubagentTool || tl == "Agent" {
				t.Errorf("%s: sub-agent tool %s present by default", role, tl)
			}
		}
	}
	s := coderSpec()
	s.Subagents = true
	inv, _ := a.BuildInvocation(s)
	if tools := inv.Argv[indexOf(inv.Argv, "--tools")+1]; !strings.HasSuffix(tools, ","+SubagentTool) {
		t.Fatalf("Subagents=true should add the tool: %s", tools)
	}
}

func TestPromptAssembly(t *testing.T) {
	inv, err := newAdapter(t, promptFS()).BuildInvocation(coderSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := "ROLE PROMPT\n\n<skill name=\"tdd\">\nTDD BODY\n</skill>\n\nCONTEXT\n\n"
	if string(inv.Stdin) != want {
		t.Fatalf("stdin %q\nwant %q", inv.Stdin, want)
	}
	for _, x := range inv.Argv {
		if strings.Contains(x, "CONTEXT") {
			t.Fatal("prompt leaked into argv")
		}
	}
}

func TestBuildInvocationRejects(t *testing.T) {
	a := newAdapter(t, promptFS())
	mod := func(f func(*api.RunSpec)) error {
		s := coderSpec()
		f(&s)
		_, err := a.BuildInvocation(s)
		return err
	}
	for name, f := range map[string]func(*api.RunSpec){
		"role":       func(s *api.RunSpec) { s.Role = "x" },
		"model":      func(s *api.RunSpec) { s.Model = "" },
		"no prompt":  func(s *api.RunSpec) { s.PromptFiles = nil },
		"missing":    func(s *api.RunSpec) { s.PromptFiles = []string{"nope.md"} },
		"skill file": func(s *api.RunSpec) { s.Skills = []api.Skill{{Name: "x", Path: "nope"}} },
		"max turns":  func(s *api.RunSpec) { s.MaxTurns = 0 },
		"timeout":    func(s *api.RunSpec) { s.Timeout = 0 },
	} {
		if mod(f) == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := New(nil, Config{}); err == nil {
		t.Error("New without lease must fail (MD-3)")
	}
}

func TestBudgetFallbackDefault(t *testing.T) {
	a := newAdapter(t, promptFS())
	if a.Capabilities().CanStopMidrun {
		t.Fatal("claude must default to can_stop_midrun=false (PP-5)")
	}
	if !a.Capabilities().CanAdvise {
		t.Fatal("claude can advise")
	}
	s := coderSpec()
	s.MaxTurns, s.Timeout = 0, 0
	if _, err := a.BuildInvocation(s); err == nil {
		t.Fatal("without max_turns and timeout the invocation must be refused")
	}
}

// useFakeClaude puts an executable named claude, behind a symlink, on PATH
// for the test and returns its resolved path.
func useFakeClaude(t *testing.T) string {
	t.Helper()
	root := shortDir(t)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "claude-real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "bin", Command)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "bin"))
	return real
}

// shortDir is a temporary directory with a symlink-free path.
func shortDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
