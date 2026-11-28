package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/spec/validate"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
)

const (
	live5Req  = "R-p-1"
	live5Step = live5Req + "/S1"
	// The session gets at most this many turns and this long; a session that
	// misbehaves is cut off by the CLI (max turns) and by the scheduler.
	live5MaxTurns    = 25
	live5SessionTime = 10 * time.Minute
	// live5Budget bounds the whole test (a ceiling, never waited for).
	live5Budget = 20 * time.Minute
)

// live5Env holds what the layer 5 test needs from the environment.
type live5Env struct {
	srt, nodeDir, repo string
	ghToken, claudeTok string
}

// live5Inputs reads the environment, or skips. Token files are read here and
// the values are never printed or logged.
func live5Inputs(t *testing.T) live5Env {
	t.Helper()
	var e live5Env
	e.srt, e.nodeDir, e.repo = os.Getenv("FOREMAN_TEST_SRT"), os.Getenv("FOREMAN_TEST_NODE_DIR"), os.Getenv("FOREMAN_TEST_GITHUB_REPO")
	ghFile, clFile := os.Getenv("FOREMAN_TEST_GITHUB_TOKEN_FILE"), os.Getenv("FOREMAN_TEST_CLAUDE_TOKEN_FILE")
	if e.srt == "" || e.nodeDir == "" || e.repo == "" || ghFile == "" || clFile == "" {
		t.Skip("FOREMAN_TEST_SRT, FOREMAN_TEST_NODE_DIR, FOREMAN_TEST_GITHUB_REPO, FOREMAN_TEST_GITHUB_TOKEN_FILE and FOREMAN_TEST_CLAUDE_TOKEN_FILE must all be set")
	}
	read := func(name, path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s token file: %v", name, err)
		}
		tok := strings.TrimSpace(string(raw))
		if tok == "" {
			t.Fatalf("%s token file is empty", name)
		}
		return tok
	}
	e.ghToken, e.claudeTok = read("GitHub", ghFile), read("claude", clFile)
	return e
}

// secrets lists every string that must appear nowhere: both tokens and the
// Basic payload git would derive from the GitHub one.
func (e live5Env) secrets() []string {
	return []string{e.ghToken, e.claudeTok, base64.StdEncoding.EncodeToString([]byte("x-access-token:" + e.ghToken))}
}

// redacted removes every secret from text before it is logged.
func (e live5Env) redacted(text string) string {
	for _, s := range e.secrets() {
		text = strings.ReplaceAll(text, s, "[secret]")
	}
	return text
}

// live5Spec is the requirement: small, fully specified, and valid by the
// program's own validator.
func live5Requirement() model.Requirement {
	return model.Requirement{ID: live5Req, Kind: model.KindFeature, Source: model.SourceOwner, Priority: model.P2,
		SpecVersion: 1, Branch: "foreman/" + live5Req, Hold: model.HoldNone, Status: model.StatusSubmitted, Size: model.SizeS,
		Spec: model.Spec{
			Title:   "add calc.Max",
			Goal:    "Package calc gains func Max(a, b int) int that returns the larger of its two arguments, covered by a table-driven test.",
			Current: []model.Ref{{Module: "calc", Path: "calc"}},
			Change:  []string{"calc/calc.go", "calc/calc_test.go"},
			Outcome: []model.Acceptance{{ID: "A1", EARS: "When Max(a, b) is called the system shall return the larger of a and b",
				Verify: model.VerifyTest, VerifyDetail: "go test ./calc/... runs TestMax with equal, ascending, descending and negative inputs", SandboxOK: true}},
			Boundaries: []string{"Touch only files under calc/", "Do not change go.mod, mise.toml or anything under .github/"},
		}}
}

// queryRows returns every row of every table of db as one string per row, to
// look for secrets in what the stores keep.
func queryRows(t *testing.T, db interface {
	Read(context.Context, func(*sql.Tx) error) error
}) []string {
	t.Helper()
	var rows []string
	err := db.Read(context.Background(), func(tx *sql.Tx) error {
		tables, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
		if err != nil {
			return err
		}
		var names []string
		for tables.Next() {
			var n string
			if err := tables.Scan(&n); err != nil {
				return err
			}
			names = append(names, n)
		}
		if err := tables.Err(); err != nil {
			return err
		}
		tables.Close()
		for _, n := range names {
			r, err := tx.Query(`SELECT * FROM "` + n + `"`)
			if err != nil {
				return err
			}
			cols, _ := r.Columns()
			for r.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := r.Scan(ptrs...); err != nil {
					r.Close()
					return err
				}
				rows = append(rows, n+": "+fmt.Sprint(vals...))
			}
			r.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// live5Wait blocks until every session the scheduler started has exited, or
// the test context ends (then the sessions are killed and the test fails).
func live5Wait(ctx context.Context, t *testing.T, app *App, home string) {
	t.Helper()
	done := make(chan struct{})
	go func() { app.Sched.WaitSessions(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		dirs, _ := filepath.Glob(filepath.Join(home, "runs", "*"))
		for _, d := range dirs {
			_, _ = launch.KillRun(d)
		}
		t.Fatalf("session did not end within the test budget: %v", ctx.Err())
	}
}

// live5Snapshot is what the code Run left, read before the scheduler collects
// it and removes the directory (D-086).
type live5Snapshot struct {
	runID, resultRaw string
	leaks            []string
	taken            bool
}

// takeSnapshot reads the coder Run directory once its session has exited.
func takeSnapshot(t *testing.T, app *App, e live5Env, home string) live5Snapshot {
	t.Helper()
	var s live5Snapshot
	var dir string
	if err := app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id, dir FROM runs WHERE role = 'coder' LIMIT 1`).Scan(&s.runID, &dir)
	}); err != nil {
		return s // no coder Run yet
	}
	if _, err := os.Stat(dir); err != nil {
		return s
	}
	raw, rerr := os.ReadFile(filepath.Join(dir, "result.json"))
	if _, serr := os.Stat(filepath.Join(dir, "stdout.log")); rerr != nil && serr != nil {
		return s // the session has not run (yet)
	}
	s.taken, s.resultRaw = true, string(raw)
	s.leaks = filesWith(t, []string{dir}, e.secrets()...)
	for _, name := range []string{"stdout.log", "stderr.log"} {
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			tail := string(raw)
			if len(tail) > 1500 {
				tail = tail[len(tail)-1500:]
			}
			t.Logf("%s (last 1500 bytes, secrets removed):\n%s", name, e.redacted(tail))
		}
	}
	if f, err := os.Open(filepath.Join(dir, "stdout.log")); err == nil {
		defer f.Close()
		sum := api.Collect(claude.ParseStream(f), nil)
		if r := sum.Result; r != nil {
			t.Logf("session cost: %.4f USD, %d turns (assistant messages %d), input %d output %d cache-read %d cache-write %d tokens, tools %v, terminal %q",
				r.CostUSD, r.Turns, sum.AssistantTurns, r.Usage.InputTokens, r.Usage.OutputTokens,
				r.Usage.CacheReadTokens, r.Usage.CacheCreationTokens, sum.ToolCounts, r.TerminalReason)
		} else {
			t.Log("session reported no result line (cost unknown)")
		}
	}
	return s
}

// TestLayer5CodeStageLive runs the code stage of one step for real (plan
// layer 5): a real srt sandbox, the real claude CLI launched only through
// sandbox/launch, a session repo cloned from the mirror of a real GitHub
// repository, and the L0 check (go vet, go test) in the sandbox. It stops
// after check_pass: nothing is pushed, no PR is opened, nothing is merged
// (only read calls reach GitHub: the rate-limit probe, the base-health poll
// and the mirror fetch).
//
// Inputs, all required (the test is skipped otherwise): FOREMAN_TEST_SRT,
// FOREMAN_TEST_NODE_DIR, FOREMAN_TEST_GITHUB_REPO (owner/name of a Go module
// with package calc, a mise.toml and CI), FOREMAN_TEST_GITHUB_TOKEN_FILE and
// FOREMAN_TEST_CLAUDE_TOKEN_FILE (files holding the tokens). mise and claude
// must be on PATH. It spends a little of the model quota (sonnet, at most
// live5MaxTurns turns).
func TestLayer5CodeStageLive(t *testing.T) {
	e := live5Inputs(t)
	owner, name, ok := strings.Cut(e.repo, "/")
	if !ok || owner == "" || name == "" {
		t.Fatalf("FOREMAN_TEST_GITHUB_REPO %q is not owner/name", e.repo)
	}
	ownerHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// A short, fresh FOREMAN_HOME (the srt socket path limit); not the owner's.
	tmp, err := os.MkdirTemp("/tmp", "fm5")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	home, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), live5Budget)
	defer cancel()

	// What happened, in order: "<step> <event>".
	var events []string
	checkPassed := false
	cfg := Config{
		Home: home, Project: "p", Remote: "https://github.com/" + e.repo + ".git", Repo: ops.Repo{Owner: owner, Name: name},
		GitHubToken: e.ghToken, ModelToken: e.claudeTok, Clock: clock.Real(),
		SrtPath: e.srt, NodeDir: e.nodeDir, OwnerHome: ownerHome,
		Toolchain:  launch.Mise{EmptyConfig: filepath.Join(home, "githome", "mise", "config.toml")},
		SessionGit: wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"},
		Harness:    "claude", Models: map[string]string{"coder": "sonnet", "reviewer": "sonnet"},
		SessionTimeout: live5SessionTime, SessionMaxTurns: live5MaxTurns,
		Path: wsgit.SystemPath(), Ledger: ledger.New(),
		// L0 of the test repository: vet and test, each inside the sandbox.
		L0:           [][]string{{"/bin/sh", "-c", "go vet ./..."}, {"/bin/sh", "-c", "go test -count=1 ./..."}},
		Constitution: "C-01: only the program changes state. C-08: no credentials in the repository or in logs.",
		OnTransition: func(stepID string, ev state.Event, out state.Outcome) {
			events = append(events, fmt.Sprintf("%s %s -> %s/%s %s", stepID, ev.Name, out.To.State, out.To.Stage, out.To.Reason))
			t.Logf("transition %s %s -> %s/%s %s", stepID, ev.Name, out.To.State, out.To.Stage, out.To.Reason)
			if stepID == live5Step && ev.Name == model.EventCheckPass {
				checkPassed = true
			}
		},
	}
	app, err := Compose(ctx, cfg)
	if err != nil {
		t.Fatalf("compose: %s", e.redacted(err.Error()))
	}
	t.Cleanup(func() { app.Close() })
	for _, c := range app.Startup {
		t.Log(e.redacted(fmt.Sprint(c)))
	}

	req := live5Requirement()
	if errs := validate.Requirement(req); len(errs) > 0 {
		t.Fatalf("the seeded requirement is not valid: %v", errs)
	}
	if err := state.InsertRequirement(ctx, app.Stores.Work, req); err != nil {
		t.Fatal(err)
	}
	if err := state.InsertStep(ctx, app.Stores.Work, cfg.Clock, model.Step{ReqID: live5Req, Type: model.StepBuild,
		Goal:       "add func Max(a, b int) int to package calc (calc/calc.go) with a table-driven TestMax in calc/calc_test.go",
		Acceptance: []string{"A1"}, Touches: []string{"calc"}}); err != nil {
		t.Fatal(err)
	}
	def, err := app.Mirror.DefaultBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base, err := app.Mirror.BranchSHA(ctx, def)
	if err != nil {
		t.Fatal(err)
	}

	// Tick until the check passed. After every Tick the sessions are waited
	// for (a real process exit); the coder Run directory is read before the
	// next Tick collects it and removes it.
	var snap live5Snapshot
	for tick := 1; tick <= 30 && !checkPassed; tick++ {
		if _, err := app.Sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %s\nevents: %v", tick, e.redacted(err.Error()), events)
		}
		live5Wait(ctx, t, app, home)
		if !snap.taken && !checkPassed {
			snap = takeSnapshot(t, app, e, home)
		}
		if ctx.Err() != nil {
			t.Fatalf("test budget exhausted; events: %v", events)
		}
	}
	if !checkPassed {
		t.Fatalf("check did not pass; events: %v", events)
	}
	assertLive5(t, app, e, home, base, snap, events)
}

// assertLive5 judges the outcome by program.
func assertLive5(t *testing.T, app *App, e live5Env, home, base string, snap live5Snapshot, events []string) {
	t.Helper()
	// The events: code ended ok, then L0 passed; nothing after that was started.
	if !containsInOrder(eventNames(events), []string{model.EventRunOK, model.EventCheckPass}) {
		t.Errorf("events %v lack run_ok then check_pass", events)
	}
	var coder, ended string
	query(t, app, `SELECT id, status FROM runs WHERE role = 'coder'`, &coder, &ended)
	if ended != "ended" {
		t.Errorf("coder Run %s is %s, want ended", coder, ended)
	}
	var checks int
	query(t, app, `SELECT COUNT(*) FROM runs WHERE role = 'check' AND status = 'ended'`, &checks)
	if checks != 1 {
		t.Errorf("%d ended check Runs, want 1 (L0 ran once, in the sandbox)", checks)
	}

	// result.json: schema-valid against PF-RESULT for this Run.
	if !snap.taken {
		t.Fatal("the coder Run directory was never seen; result.json cannot be judged")
	}
	res, err := api.ValidateResult([]byte(snap.resultRaw), api.ResultContext{RunID: snap.runID, Role: api.RoleCoder, Scope: "step", AcceptanceIDs: []string{"A1"}})
	if err != nil {
		t.Fatalf("result.json is not valid: %v", err)
	}
	if res.Status != "ok" || len(res.Commits) == 0 {
		t.Errorf("result.json status %q with %d commits, want ok with at least one", res.Status, len(res.Commits))
	}

	assertLive5Commits(t, app, base, res)
	assertLive5NoLeaks(t, app, e, home, snap)

	// D-086: the Run directories are gone once their results are committed.
	if left, _ := filepath.Glob(filepath.Join(home, "runs", "*")); len(left) != 0 {
		t.Errorf("run directories left behind: %v", left)
	}

	// The code stage's usage as stored (the cost line was logged from the stream).
	var usage string
	query(t, app, `SELECT usage FROM runs WHERE role = 'coder'`, &usage)
	t.Logf("stored coder usage: %s", usage)
}

// assertLive5Commits judges the session's commits, as the check stage fetched
// them into foremand's integration directory (never git with a session repo
// as cwd): new, on top of the mirror head, by the session identity, in calc/.
func assertLive5Commits(t *testing.T, app *App, base string, res *api.Result) {
	t.Helper()
	ctx := context.Background()
	ref := integrate.StepRefPrefix + "steps/" + live5Step
	git, dir := app.Git, app.Integ.Path
	head, err := git.Line(ctx, dir, "rev-parse", ref)
	if err != nil {
		t.Fatalf("step ref %s: %v", ref, err)
	}
	if len(res.Commits) > 0 && res.Commits[len(res.Commits)-1] != head {
		t.Errorf("result.json names head %s, the session branch is at %s", res.Commits[len(res.Commits)-1], head)
	}
	if _, err := git.Run(ctx, dir, "merge-base", "--is-ancestor", base, head); err != nil || head == base {
		t.Errorf("the session head %s is not a new commit on top of the mirror head %s (%v)", head, base, err)
	}
	authors, err := git.Run(ctx, dir, "log", "--format=%an <%ae> | %cn <%ce>", base+".."+head)
	if err != nil || strings.TrimSpace(authors) == "" {
		t.Fatalf("log of the session commits: %q (%v)", authors, err)
	}
	for _, l := range strings.Split(strings.TrimSpace(authors), "\n") {
		const want = "foreman-session <foreman-session@users.noreply.example> | foreman-session <foreman-session@users.noreply.example>"
		if l != want {
			t.Errorf("session commit identity %q, want %q", l, want)
		}
	}
	files, err := git.Run(ctx, dir, "diff", "--name-only", base+".."+head)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Fields(files)
	if len(changed) == 0 {
		t.Error("the session changed no file")
	}
	for _, f := range changed {
		if !strings.HasPrefix(f, "calc/") {
			t.Errorf("the diff touches %s outside calc/", f)
		}
	}
	t.Logf("session commits: %d, files: %v", strings.Count(authors, "\n")+1, changed)

}

// assertLive5NoLeaks looks for the tokens in the session repo, the Run
// directory logs, the git and config directories and every stored row.
func assertLive5NoLeaks(t *testing.T, app *App, e live5Env, home string, snap live5Snapshot) {
	t.Helper()
	if len(snap.leaks) > 0 {
		t.Errorf("a secret is in the coder Run directory: %v", snap.leaks)
	}
	var sessionRepo string
	query(t, app, `SELECT session_repo FROM requirements WHERE id = '`+live5Req+`'`, &sessionRepo)
	dirs := []string{sessionRepo, filepath.Join(home, "githome"), filepath.Join(home, "db"), filepath.Join(home, "config"),
		filepath.Join(home, "projects"), filepath.Join(home, "runs")}
	if bad := filesWith(t, dirs, e.secrets()...); len(bad) > 0 {
		t.Errorf("a secret is stored in %v", bad)
	}
	for _, db := range []struct {
		name string
		rows []string
	}{{"work", queryRows(t, app.Stores.Work)}, {"global", queryRows(t, app.Stores.Global)}} {
		for _, row := range db.rows {
			for _, s := range e.secrets() {
				if strings.Contains(row, s) {
					t.Errorf("a secret is in a %s database row: %.60s", db.name, e.redacted(row))
				}
			}
		}
	}
}

// eventNames returns the event name of each "<step> <event> -> ..." line.
func eventNames(events []string) []string {
	var out []string
	for _, ev := range events {
		if f := strings.Fields(ev); len(f) >= 2 {
			out = append(out, f[1])
		}
	}
	return out
}
