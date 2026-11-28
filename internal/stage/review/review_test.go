package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

func TestMain(m *testing.M) {
	scripted.MaybeRunChild()
	os.Exit(m.Run())
}

type world struct {
	t     *testing.T
	root  string
	git   *wsgit.Runner
	env   Env
	sess  *session.Repo
	integ *integrate.Dir
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	gr, err := wsgit.New(wsgit.Config{Home: filepath.Join(root, "gh"), Clock: clk,
		Ident: &wsgit.Identity{Name: "seed", Email: "seed@users.noreply.example"}})
	if err != nil {
		t.Fatal(err)
	}
	must := func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	must(gr.Run(ctx, "", "init", "--bare", "-b", "main", remote))
	must(gr.Run(ctx, work, "init", "-b", "main"))
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must(gr.Run(ctx, work, "add", "-A"))
	must(gr.Run(ctx, work, "commit", "-m", "seed"))
	must(gr.Run(ctx, work, "push", remote, "HEAD:refs/heads/main"))
	m, err := session.CloneMirror(ctx, gr, session.MirrorDir(gr.Home(), "p"), remote)
	if err != nil {
		t.Fatal(err)
	}
	integ, err := integrate.Init(ctx, gr, m, filepath.Join(root, "integ"), wsgit.Identity{Name: "foreman", Email: "foreman@users.noreply.example"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Create(ctx, gr, m, filepath.Join(root, "sess"), session.Spec{ReqID: "r1", BaseBranch: "main",
		Identity: wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}})
	if err != nil {
		t.Fatal(err)
	}
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meter.NewStore(), Ledger: ledger.New()})
	dec, err := g.Acquire(ctx, gate.Demand{Items: []gate.Item{{Resource: "local/slots/session", Amount: 1}}, Priority: gate.P2})
	if err != nil || !dec.Allowed() {
		t.Fatalf("lease %+v %v", dec, err)
	}
	ad, err := scripted.New(dec.Lease, scripted.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, root: root, git: gr, sess: sess, integ: integ, env: Env{
		Session: ctxmd.Env{Launcher: launch.Launcher{PassThrough: true}, Adapter: ad, Timeout: time.Minute,
			Policy: policy.Inputs{DenyWrite: []string{filepath.Join(root, "db")}, Path: wsgit.SystemPath(),
				SessionGit: policy.SessionGit{Name: "foreman-session", Email: "foreman-session@users.noreply.example", DefaultBranch: "main"}}},
		Integ: integ, Mirror: m}}
}

// commit stands in for the coder: one commit in the session repo.
func (w *world) commit(file, body string) {
	w.t.Helper()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(w.sess.Dir, file), []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "coder " + file}} {
		if _, err := w.git.Run(ctx, w.sess.Dir, args...); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) request(resultBody string, mb bool) Request {
	w.t.Helper()
	raw, _ := json.Marshal(scripted.Script{Actions: []scripted.Action{
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: resultBody}}}})
	script := filepath.Join(w.root, "script.json")
	if err := os.WriteFile(script, raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	r := Request{Session: ctxmd.Request{RunID: "r2", StepID: "r1/S1", Role: api.RoleReviewer, Model: "scripted",
		RunDir: filepath.Join(w.root, "runs", "r2"), SessionRepo: w.sess.Dir, Context: "# ctx\n", Prompt: "review\n",
		Extra: map[string]string{scripted.ExtraScript: script}}}
	if mb {
		r.MergeBack = &MergeBack{ReqID: "r1", StepID: "r1/S1", SessionRepo: w.sess.Dir, SessionBranch: w.sess.Branch, Subject: "implement sum",
			Identity: wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}}
	}
	return r
}

const approve = `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"reviewer","status":"ok","verdict":"approve","findings":[]}`

func TestReviewApproveMergesBackWithStepIDTrailer(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.commit("sum.go", "package sum\n")
	w.commit("sum2.go", "package sum\n")

	ev, err := Run(ctx, w.env, w.request(approve, true))
	if err != nil || ev.Name != model.EventReviewApprove {
		t.Fatalf("event %+v err %v", ev, err)
	}
	tip, err := w.integ.BranchSHA(ctx, "r1")
	if err != nil || tip != ev.Head {
		t.Fatalf("tip %q err %v, event head %s", tip, err, ev.Head)
	}
	msg, _ := w.git.Line(ctx, w.integ.Path, "log", "-1", "--format=%B", tip)
	if !strings.HasSuffix(msg, "Step-Id: r1/S1") {
		t.Fatalf("commit message %q", msg)
	}
	if n, _ := w.git.Line(ctx, w.integ.Path, "rev-list", "--count", "refs/remotes/mirror/main.."+tip); n != "1" {
		t.Fatalf("two coder commits must squash to one, got %s", n)
	}
	// The session repo was refreshed: its branch is the requirement branch tip
	// (the squash commit, not the coder's two) and its tree has the files.
	if head, _ := w.git.Line(ctx, "", "--git-dir", filepath.Join(w.sess.Dir, ".git"), "rev-parse", "HEAD"); head != tip {
		t.Errorf("session head %s, want the requirement tip %s", head, tip)
	}
	if _, err := os.Stat(filepath.Join(w.sess.Dir, "sum2.go")); err != nil {
		t.Errorf("refreshed session lacks the merged file: %v", err)
	}
	// Re-running after a crash is idempotent (the trailer is already there).
	ev2, err := Run(ctx, w.env, w.request(approve, true))
	if err != nil || ev2.Name != model.EventReviewApprove || ev2.Head != tip {
		t.Fatalf("repeat %+v err %v", ev2, err)
	}
}

func TestReviewChangesCarriesFindingsAndDoesNotMerge(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.commit("sum.go", "package sum\n")
	body := `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"reviewer","status":"ok","verdict":"changes","findings":[` +
		`{"scope":"r1/S1","severity":"blocking","location":{"path":"sum.go","line":1},"problem":"no doc","close_when":"doc added"}]}`
	ev, err := Run(ctx, w.env, w.request(body, true))
	if err != nil || ev.Name != model.EventReviewChanges || len(ev.Findings) != 1 || ev.Findings[0].Path != "sum.go" {
		t.Fatalf("event %+v err %v", ev, err)
	}
	if _, err := w.integ.BranchSHA(ctx, "r1"); err == nil {
		t.Fatal("requirement branch exists although the review asked for changes")
	}
	// S52 sets last_reviewed_sha=head: the event carries the head reviewed.
	if sessionHead, _ := w.git.Line(ctx, w.sess.Dir, "rev-parse", "HEAD"); ev.Head != sessionHead {
		t.Fatalf("review_changes head %q, session head %q", ev.Head, sessionHead)
	}
}

func TestReviewMergeBackConflictIsAnEvent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.commit("a.txt", "from step\n")
	// The requirement branch already changed a.txt differently.
	if err := w.integ.SyncFromMirror(ctx); err != nil {
		t.Fatal(err)
	}
	w.commitOnIntegration("a.txt", "from elsewhere\n")
	ev, err := Run(ctx, w.env, w.request(approve, true))
	if err != nil || ev.Name != model.EventMergeBackConfl || !strings.Contains(ev.Detail, "a.txt") {
		t.Fatalf("event %+v err %v", ev, err)
	}
}

// commitOnIntegration (test setup only) puts a commit on foreman/r1 in the
// integration directory, as an earlier step's merge-back would have.
func (w *world) commitOnIntegration(file, body string) {
	w.t.Helper()
	ctx := context.Background()
	for _, args := range [][]string{{"checkout", "-q", "-B", "foreman/r1", "refs/remotes/mirror/main"}} {
		if _, err := w.git.Run(ctx, w.integ.Path, args...); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.integ.Path, file), []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "earlier step"}} {
		if _, err := w.git.Run(ctx, w.integ.Path, args...); err != nil {
			w.t.Fatal(err)
		}
	}
}
