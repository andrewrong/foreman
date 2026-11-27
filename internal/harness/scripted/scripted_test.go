package scripted

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// TestMain makes the test binary double as the scripted child process.
func TestMain(m *testing.M) {
	MaybeRunChild()
	os.Exit(m.Run())
}

var t0 = time.Unix(1_800_000_000, 0)

func testLease(t *testing.T) *gate.Lease {
	t.Helper()
	return &gate.Lease{} // see claude_test.go: ledger is off limits to harness
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=scripted", "GIT_AUTHOR_EMAIL=scripted@example.invalid",
		"GIT_COMMITTER_NAME=scripted", "GIT_COMMITTER_EMAIL=scripted@example.invalid",
	}
}

type session struct {
	repo, runDir string
	out, errOut  bytes.Buffer
	code         int
}

// run builds the invocation for script and executes it as a real process
// with a from-scratch environment plus the Invocation's additions.
func run(t *testing.T, role api.Role, script Script) *session {
	t.Helper()
	root := t.TempDir()
	s := &session{repo: filepath.Join(root, "repo"), runDir: filepath.Join(root, "run")}
	for _, d := range []string{s.repo, s.runDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, s.repo, "init", "-q", "-b", "main")
	git(t, s.repo, "commit", "-q", "--allow-empty", "-m", "base")
	raw, _ := json.Marshal(script)
	scriptPath := filepath.Join(root, "script.json")
	if err := os.WriteFile(scriptPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := New(testLease(t), Config{})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.BuildInvocation(api.RunSpec{
		RunID: "R-1/S1/r1", Role: role, MaxTurns: 5, Timeout: time.Minute,
		RunDir: s.runDir, WorkDir: s.repo, Extra: map[string]string{ExtraScript: scriptPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(inv.Argv[0], inv.Argv[1:]...)
	cmd.Dir = s.repo
	cmd.Env = gitEnv()
	for k, v := range inv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = &s.out, &s.errOut
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		s.code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return s
}

const coderResult = `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],` +
	`"acceptance":[{"id":"A1","result":"pass","evidence":"scripted"}]}`

func TestScriptedCoderEndToEnd(t *testing.T) {
	rl := map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.5, ResetsAt: t0.Unix() + 3600, Status: "allowed"}}
	s := run(t, api.RoleCoder, Script{Actions: []Action{
		{Say: "working"},
		{Write: &Write{Path: "hello.go", Content: "package hello\n"}},
		{Git: []string{"add", "hello.go"}},
		{Git: []string{"commit", "-q", "-m", "add hello"}},
		{RateLimit: rl},
		{Write: &Write{Path: "$RUN_DIR/todo.json", JSON: json.RawMessage(`{"schema_version":1}`)}},
		{Write: &Write{Path: "$RUN_DIR/result.json", Content: coderResult}},
		{Finish: &Finish{Turns: 2, CostUSD: 0.01, Input: 100, Output: 50, Text: "done"}},
	}})
	if s.code != 0 {
		t.Fatalf("exit %d: %s", s.code, s.errOut.String())
	}
	// Code edit and commit happened in the session repo.
	if got := git(t, s.repo, "log", "--format=%s", "-1"); got != "add hello" {
		t.Fatalf("last commit %q", got)
	}
	// result.json passes the real validator with the real commit sha.
	raw, err := os.ReadFile(filepath.Join(s.runDir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := api.ValidateResult(raw, api.ResultContext{RunID: "R-1/S1/r1", Role: api.RoleCoder, AcceptanceIDs: []string{"A1"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if head := git(t, s.repo, "rev-parse", "--short", "HEAD"); res.Commits[0] != head {
		t.Fatalf("commit %s, head %s", res.Commits[0], head)
	}
	// No stray files in the run dir, and no .tmp left behind (atomic write).
	if err := api.CheckOutputs(os.DirFS(s.runDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.runDir, "result.json.tmp")); err == nil {
		t.Fatal("tmp file left behind")
	}
	// The stream parses with the same parser as claude's.
	a, _ := New(testLease(t), Config{})
	store := meter.NewStore()
	sum := api.Collect(a.ParseStream(&s.out), api.MeterSink(store, "claude", "max-1", clock.NewFake(t0)))
	if sum.Init == nil || sum.AssistantTurns != 1 || sum.Result == nil || sum.Result.Usage.Total() != 150 || sum.Result.CostUSD != 0.01 || len(sum.Errors) != 0 {
		t.Fatalf("summary %+v", sum)
	}
	if r, ok := store.Get("model/claude/max-1/7d"); !ok || r.Used != 50 {
		t.Fatalf("meter %+v %v", r, ok)
	}
}

func TestScriptedReviewerAndStray(t *testing.T) {
	review := `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"reviewer","status":"ok","verdict":"approve","findings":[]}`
	s := run(t, api.RoleReviewer, Script{Actions: []Action{
		{Write: &Write{Path: "$RUN_DIR/result.json", Content: review}},
		{Write: &Write{Path: "$RUN_DIR/notes.md", Content: "oops"}},
	}})
	if s.code != 0 {
		t.Fatalf("exit %d %s", s.code, s.errOut.String())
	}
	raw, _ := os.ReadFile(filepath.Join(s.runDir, "result.json"))
	if _, err := api.ValidateResult(raw, api.ResultContext{RunID: "R-1/S1/r1", Role: api.RoleReviewer}); err != nil {
		t.Fatal(err)
	}
	// The session also wrote a file outside the protocol: rejected.
	if err := api.CheckOutputs(os.DirFS(s.runDir)); err == nil || !strings.Contains(err.Error(), "notes.md") {
		t.Fatalf("stray file not detected: %v", err)
	}
}

func TestScriptedFailureModes(t *testing.T) {
	// exit code surfaces; no result.json written -> PP-4 situation
	three := 3
	s := run(t, api.RoleCoder, Script{Actions: []Action{{Say: "x"}, {Exit: &three}}})
	if s.code != 3 {
		t.Fatalf("exit %d", s.code)
	}
	if _, err := os.Stat(filepath.Join(s.runDir, "result.json")); err == nil {
		t.Fatal("result.json must be absent")
	}
	sum := api.Collect(claude.ParseStream(&s.out), nil)
	if sum.Result != nil {
		t.Fatal("early exit must produce no result event")
	}
	// non-atomic (half-written) result is expressible and fails validation
	s = run(t, api.RoleCoder, Script{Actions: []Action{{Write: &Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,`, NonAtomic: true}}}})
	raw, _ := os.ReadFile(filepath.Join(s.runDir, "result.json"))
	if _, err := api.ValidateResult(raw, api.ResultContext{RunID: "R-1/S1/r1", Role: api.RoleCoder}); err == nil {
		t.Fatal("half-written result accepted")
	}
	// malformed action => exit 2
	s = run(t, api.RoleCoder, Script{Actions: []Action{{Say: "a", Git: []string{"status"}}}})
	if s.code != 2 {
		t.Fatalf("bad action: exit %d", s.code)
	}
	// failing git => exit 3
	s = run(t, api.RoleCoder, Script{Actions: []Action{{Git: []string{"no-such-subcommand"}}}})
	if s.code != 3 {
		t.Fatalf("failing git: exit %d", s.code)
	}
}

func TestScriptedAdapterContract(t *testing.T) {
	a, err := New(testLease(t), Config{Exe: "/bin/host", Script: "/tmp/s.json"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Capabilities().CanStopMidrun {
		t.Fatal("scripted must default to can_stop_midrun=false (PP-5)")
	}
	spec := api.RunSpec{RunID: "r", Role: api.RoleCoder, RunDir: "/run", MaxTurns: 1, Timeout: time.Second}
	inv, err := a.BuildInvocation(spec)
	if err != nil || inv.Argv[0] != "/bin/host" || inv.Env[EnvScript] != "/tmp/s.json" || inv.Env[EnvChild] != "1" || inv.Env[EnvRunDir] != "/run" {
		t.Fatalf("%+v %v", inv, err)
	}
	spec.MaxTurns = 0
	if _, err := a.BuildInvocation(spec); !errors.Is(err, api.ErrBudgetFallback) {
		t.Fatalf("want fallback error, got %v", err)
	}
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("lease required")
	}
	b, _ := New(testLease(t), Config{})
	if _, err := b.BuildInvocation(api.RunSpec{Role: api.RoleCoder, MaxTurns: 1, Timeout: time.Second}); err == nil {
		t.Fatal("script required")
	}
}
