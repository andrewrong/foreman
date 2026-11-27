package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/spec/validate"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

func TestMain(m *testing.M) {
	scripted.MaybeRunChild() // the test binary doubles as the scripted model process
	os.Exit(m.Run())
}

const (
	reqID  = "R-m1"
	step1  = reqID + "/S1"
	step2  = reqID + "/S2"
	goMod  = "module example.test/sum\n\ngo 1.21\n"
	sumGo  = "package sum\n\nfunc Sum(a, b int) int { return a + b }\n"
	sumTst = "package sum\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif Sum(1, 2) != 3 {\n\t\tt.Fatal(\"bad Sum\")\n\t}\n}\n"
)

// fixture is a project: a bare local "remote" with a tiny Go module, a short
// FOREMAN_HOME and the scripted model scripts.
type fixture struct {
	t       *testing.T
	home    string
	remote  string
	git     *wsgit.Runner
	scripts map[api.Role]string
	goBin   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	// t.TempDir is too long on macOS for the 70-byte RunTmp limit.
	root, err := os.MkdirTemp("/tmp", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx := context.Background()
	gr, err := wsgit.New(wsgit.Config{Home: filepath.Join(root, "seedhome"),
		Ident: &wsgit.Identity{Name: "seed", Email: "seed@users.noreply.example"}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: filepath.Join(root, "home"), remote: filepath.Join(root, "remote.git"), git: gr,
		scripts: map[api.Role]string{}, goBin: goBin}
	work := filepath.Join(root, "work")
	for _, d := range []string{work, f.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.must(gr.Run(ctx, "", "init", "--bare", "-b", "main", f.remote))
	f.must(gr.Run(ctx, work, "init", "-b", "main"))
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	f.must(gr.Run(ctx, work, "add", "-A"))
	f.must(gr.Run(ctx, work, "commit", "-m", "seed"))
	f.must(gr.Run(ctx, work, "push", f.remote, "HEAD:refs/heads/main"))

	coder := scripted.Script{Actions: []scripted.Action{
		{Say: "implementing Sum"},
		{Write: &scripted.Write{Path: "sum.go", Content: sumGo}},
		{Write: &scripted.Write{Path: "sum_test.go", Content: sumTst}},
		{Git: []string{"add", "-A"}},
		{Git: []string{"commit", "-q", "-m", "implement Sum"}},
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],` +
			`"acceptance":[{"id":"A1","result":"pass","evidence":"go test"}]}`}},
		{Finish: &scripted.Finish{Turns: 3, Input: 1000, Output: 200, Text: "done"}},
	}}
	reviewer := scripted.Script{Actions: []scripted.Action{
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"reviewer",` +
			`"status":"ok","verdict":"approve","findings":[]}`}},
		{Finish: &scripted.Finish{Turns: 1, Input: 500, Output: 50, Text: "approved"}},
	}}
	for role, s := range map[api.Role]scripted.Script{api.RoleCoder: coder, api.RoleReviewer: reviewer} {
		raw, _ := json.Marshal(s)
		p := filepath.Join(root, string(role)+".json")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		f.scripts[role] = p
	}
	return f
}

func (f *fixture) must(_ string, err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remoteSHA(ref string) string {
	f.t.Helper()
	out, err := f.git.Line(context.Background(), f.remote, "rev-parse", ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) config(clk clock.Clock, ghURL string, on func(string, state.Event, state.Outcome)) Config {
	return Config{
		Home: f.home, Project: "p", Remote: f.remote, Repo: ops.Repo{Owner: "acme", Name: "widgets"},
		GitHubBase: ghURL, GitHubToken: "test-token", Clock: clk, Harness: "scripted",
		Models:  map[string]string{"coder": "scripted", "reviewer": "scripted"},
		Scripts: f.scripts, Path: filepath.Dir(f.goBin) + ":" + wsgit.DefaultPath,
		L0:     [][]string{{f.goBin, "test", "./..."}},
		Ledger: ledger.New(), OnTransition: on, Constitution: "C-01: only Transition writes state.",
	}
}

// seed inserts the fast-lane requirement: size s, one build step and its
// integrate step.
func seed(t *testing.T, app *App, clk clock.Clock) {
	t.Helper()
	ctx := context.Background()
	req := model.Requirement{ID: reqID, Kind: model.KindFeature, Source: model.SourceOwner, Priority: model.P2,
		SpecVersion: 1, Branch: "foreman/" + reqID, Hold: model.HoldNone, Status: model.StatusSubmitted, Size: model.SizeS,
		Spec: model.Spec{Title: "add Sum", Goal: "Sum adds two ints", Change: []string{"sum.go"},
			Outcome: []model.Acceptance{{ID: "A1", EARS: "When Sum(1,2) is called the system shall return 3",
				Verify: model.VerifyTest, VerifyDetail: "go test ./...", SandboxOK: true}}}}
	if err := state.InsertRequirement(ctx, app.Stores.Work, req); err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.Step{
		{ReqID: reqID, Type: model.StepBuild, Goal: "implement Sum", Acceptance: []string{"A1"}, Touches: []string{"root"}},
		{ReqID: reqID, Type: model.StepIntegrate, Goal: "integrate " + reqID, Acceptance: []string{"A1"}, Touches: []string{"root"},
			DependsOn: []string{step1}},
	} {
		if err := state.InsertStep(ctx, app.Stores.Work, clk, s); err != nil {
			t.Fatal(err)
		}
	}
}

func query(t *testing.T, app *App, q string, dst ...any) {
	t.Helper()
	err := app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q).Scan(dst...) })
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestM1FastLane runs one size-s requirement end to end: queued -> code (a
// scripted model makes a real commit) -> check (real `go test` in the
// integration checkout) -> review (scripted approve) -> merge-back with a
// Step-Id trailer -> push to a local bare remote -> PR on a fake GitHub ->
// L1 poll -> merge under the merge-slot lease -> done.
func TestM1FastLane(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	mainBefore := f.remoteSHA("refs/heads/main")

	var rows []string
	gh, ghURL := newFakeGitHub(t, func(branch string) string { return f.remoteSHA("refs/heads/" + branch) })
	cfg := f.config(clk, ghURL, func(stepID string, ev state.Event, out state.Outcome) {
		rows = append(rows, out.RowID)
		t.Logf("transition %-8s %-12s -> RowID %-4s state=%s stage=%s reason=%s", stepID, ev.Name, out.RowID, out.To.State, out.To.Stage, out.To.Reason)
	})
	app, err := Compose(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	seed(t, app, clk)

	done := false
	for tick := 1; tick <= 40 && !done; tick++ {
		clk.Advance(time.Second)
		if _, err := app.Sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		var open int
		query(t, app, `SELECT COUNT(*) FROM steps WHERE state <> 'done'`, &open)
		done = open == 0
	}
	t.Logf("RowIDs hit: %s", strings.Join(rows, " "))
	if !done {
		t.Fatalf("not done after 40 ticks; rows: %v", rows)
	}

	// Every expected row, in order (S02 and S10 belong to the integrate step
	// waiting for its dependency; other rows may be interleaved).
	want := []string{"S04", "S40", "S05", "S41", "S06", "S42", "S10", "S07", "S29", "S08", "S44"}
	pos := 0
	for _, r := range rows {
		if pos < len(want) && r == want[pos] {
			pos++
		}
	}
	if pos != len(want) {
		t.Errorf("rows %v do not contain %v in order", rows, want)
	}
	if !slices.Contains(rows, "S02") {
		t.Errorf("integrate step never waited for its dependency (S02): %v", rows)
	}

	// Requirement state: both steps done, PR merged. The derived phase is
	// "reporting": the feedback report (not part of M1) would make it "done".
	var prState string
	var prNumber int
	var prHead string
	query(t, app, `SELECT state, number, head_sha FROM pull_requests WHERE req_id = 'R-m1'`, &prState, &prNumber, &prHead)
	if prState != "merged" || prNumber != 7 {
		t.Errorf("pull request: state %s number %d", prState, prNumber)
	}
	phase := validate.DerivedPhase(validate.PhaseInput{Status: model.StatusSubmitted, HasPR: true, AllPRsMerged: true,
		HasSteps: true})
	if phase != model.PhaseReporting {
		t.Errorf("derived phase %s", phase)
	}

	// The work step: a squashed commit with the Step-Id trailer on the
	// requirement branch of the remote.
	branchSHA := f.remoteSHA("refs/heads/foreman/" + reqID)
	if prHead != branchSHA {
		t.Errorf("PR head %s, remote branch %s", prHead, branchSHA)
	}
	var commit string
	query(t, app, `SELECT commit_sha FROM steps WHERE id = 'R-m1/S1'`, &commit)
	if commit != branchSHA {
		t.Errorf("step commit_sha %s, branch tip %s", commit, branchSHA)
	}
	msg, _ := f.git.Line(ctx, f.remote, "log", "-1", "--format=%B", branchSHA)
	if !strings.HasSuffix(msg, "Step-Id: "+step1) {
		t.Errorf("tip message %q lacks the Step-Id trailer", msg)
	}
	if n, _ := f.git.Line(ctx, f.remote, "rev-list", "--count", "refs/heads/main..refs/heads/foreman/"+reqID); n != "1" {
		t.Errorf("requirement branch has %s commits beyond main, want the one squashed step commit", n)
	}
	var l0 string
	query(t, app, `SELECT last_l0_sha FROM requirements WHERE id = 'R-m1'`, &l0)
	if l0 == "" {
		t.Error("last_l0_sha not recorded by check_pass")
	}

	// D-050: foremand pushed only foreman/<req id>; main is untouched.
	if got := f.remoteSHA("refs/heads/main"); got != mainBefore {
		t.Errorf("default branch moved: %s -> %s", mainBefore, got)
	}
	refs, _ := f.git.Run(ctx, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads")
	if strings.Fields(refs)[0] != "refs/heads/foreman/"+reqID || len(strings.Fields(refs)) != 2 {
		t.Errorf("remote branches: %q", refs)
	}

	// The fake GitHub saw one create-PR and one merge pinned to the head.
	if len(gh.created) != 1 || gh.created[0].Head != "foreman/"+reqID || gh.created[0].Base != "main" {
		t.Errorf("created PRs: %+v", gh.created)
	}
	if len(gh.merges) != 1 || gh.merges[0]["sha"] != branchSHA {
		t.Errorf("merges: %+v, want head %s", gh.merges, branchSHA)
	}

	// Runs: one coder, one check, one reviewer, one ci_watch, one merge, all ended.
	var runs, ended int
	query(t, app, `SELECT COUNT(*), SUM(status = 'ended') FROM runs`, &runs, &ended)
	if runs != 5 || ended != 5 {
		t.Errorf("runs %d ended %d, want 5/5", runs, ended)
	}
	var usage string
	query(t, app, `SELECT usage FROM runs WHERE role = 'coder'`, &usage)
	if !strings.Contains(usage, `"output":200`) {
		t.Errorf("coder usage %s", usage)
	}
	// Every lease was settled or released: nothing left reserved.
	if open := cfg.Ledger.Open(); len(open) != 0 {
		t.Errorf("unsettled reservations: %+v", open)
	}
}

func TestComposeRefusesNilLedgerAndLongHome(t *testing.T) {
	if _, err := newGate(gate.Options{}); err == nil {
		t.Fatal("gate built without a ledger")
	}
	f := newFixture(t)
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	_, ghURL := newFakeGitHub(t, func(string) string { return "" })
	cfg := f.config(clk, ghURL, nil)
	cfg.Ledger = nil
	if _, err := Compose(context.Background(), cfg); err == nil {
		t.Fatal("Compose accepted a nil Ledger")
	}
	cfg = f.config(clk, ghURL, nil)
	cfg.Home = "/tmp/" + strings.Repeat("x", 60)
	if _, err := Compose(context.Background(), cfg); err == nil {
		t.Fatal("Compose accepted a FOREMAN_HOME too long for the srt socket path")
	}
}
