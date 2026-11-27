package code

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

func TestMain(m *testing.M) {
	scripted.MaybeRunChild()
	os.Exit(m.Run())
}

type world struct {
	t         *testing.T
	root      string
	repo, run string
	git       *wsgit.Runner
	lease     *gate.Lease
	env       Env
}

// newWorld builds a session repo with one commit, a short run dir (RunTmp is
// limited to 70 bytes) and a real gate lease.
func newWorld(t *testing.T, adapterName string) *world {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx := context.Background()
	ident := wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}
	gr, err := wsgit.New(wsgit.Config{Home: filepath.Join(root, "gh"), Ident: &ident})
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, root: root, repo: filepath.Join(root, "sess"), run: filepath.Join(root, "runs", "r1"), git: gr}
	if err := os.MkdirAll(w.repo, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", ident.Name},
		{"config", "user.email", ident.Email}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		if _, err := gr.Run(ctx, w.repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meter.NewStore(), Ledger: ledger.New()})
	dec, err := g.Acquire(ctx, gate.Demand{Items: []gate.Item{{Resource: "local/slots/session", Amount: 1}},
		Priority: gate.P2, Caller: gate.Caller{Role: "coder"}})
	if err != nil || !dec.Allowed() {
		t.Fatalf("lease: %+v %v", dec, err)
	}
	w.lease = dec.Lease
	var ad api.Adapter
	switch adapterName {
	case "scripted":
		ad, err = scripted.New(w.lease, scripted.Config{})
	case "claude":
		ad, err = claude.New(w.lease, claude.Config{})
	}
	if err != nil {
		t.Fatal(err)
	}
	w.env = Env{
		Launcher: launch.Launcher{PassThrough: true}, Adapter: ad, Timeout: time.Minute,
		Policy: policy.Inputs{DenyWrite: []string{filepath.Join(root, "db")}, Path: wsgit.DefaultPath},
	}
	return w
}

func (w *world) request(actions ...scripted.Action) Request {
	w.t.Helper()
	raw, _ := json.Marshal(scripted.Script{Actions: actions})
	script := filepath.Join(w.root, "script.json")
	if err := os.WriteFile(script, raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	return Request{RunID: "r1", StepID: "r1/S1", Role: api.RoleCoder, Model: "scripted", RunDir: w.run,
		SessionRepo: w.repo, Context: "# ctx\n", Prompt: "you are the coder\n", AcceptanceIDs: []string{"A1"},
		Extra: map[string]string{scripted.ExtraScript: script}}
}

const okResult = `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],` +
	`"acceptance":[{"id":"A1","result":"pass","evidence":"scripted"}]}`

func TestCodeStageScriptedCommitProducesRunOK(t *testing.T) {
	w := newWorld(t, "scripted")
	req := w.request(
		scripted.Action{Write: &scripted.Write{Path: "sum.go", Content: "package sum\n"}},
		scripted.Action{Git: []string{"add", "sum.go"}},
		scripted.Action{Git: []string{"commit", "-q", "-m", "add sum"}},
		scripted.Action{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: okResult}},
		scripted.Action{Finish: &scripted.Finish{Turns: 1, Input: 10, Output: 5, Text: "done"}},
	)
	ev, err := Run(context.Background(), w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := w.git.Line(context.Background(), w.repo, "rev-parse", "--short", "HEAD")
	if ev.Name != model.EventRunOK || ev.Head != head || ev.Usage.Output != 5 {
		t.Fatalf("event %+v, head %s", ev, head)
	}
	for _, f := range []string{"context.md", "prompt.md", "stdout.log", "result.json"} {
		if _, err := os.Stat(filepath.Join(w.run, f)); err != nil {
			t.Errorf("run dir lacks %s", f)
		}
	}
}

func TestCodeStageFailureModes(t *testing.T) {
	three := 3
	cases := []struct {
		name    string
		actions []scripted.Action
		want    string
	}{
		{"no result.json is bad_result (PP-4)", []scripted.Action{{Say: "hi"}}, model.EventBadResult},
		{"invalid result is bad_result", []scripted.Action{{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1}`}}}, model.EventBadResult},
		{"non-zero exit is infra", []scripted.Action{{Exit: &three}}, model.EventInfra},
		{"spec_conflict passes through", []scripted.Action{{Write: &scripted.Write{Path: "$RUN_DIR/result.json",
			Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"spec_conflict","note":"spec wrong"}`}}}, model.EventSpecConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, "scripted")
			ev, err := Run(context.Background(), w.env, w.request(c.actions...))
			if err != nil || ev.Name != c.want {
				t.Fatalf("event %+v err %v, want %s", ev, err, c.want)
			}
		})
	}
}

// The claude adapter passes bypassPermissions (D-056) and must only ever run
// inside srt: a pass-through launcher is refused before anything is written.
func TestClaudeSessionRefusedWithoutSandbox(t *testing.T) {
	w := newWorld(t, "claude")
	_, err := Run(context.Background(), w.env, w.request())
	if !errors.Is(err, ErrNeedsSandbox) {
		t.Fatalf("err = %v, want ErrNeedsSandbox", err)
	}
	if _, serr := os.Stat(w.run); !os.IsNotExist(serr) {
		t.Errorf("run dir created before the refusal: %v", serr)
	}
	// With srt configured the same request passes the guard (and fails later
	// only because no srt binary exists here).
	w.env.Launcher = launch.Launcher{SrtPath: filepath.Join(w.root, "no-such-srt")}
	if _, err := Run(context.Background(), w.env, w.request()); errors.Is(err, ErrNeedsSandbox) {
		t.Fatal("sandboxed launcher refused")
	}
}
