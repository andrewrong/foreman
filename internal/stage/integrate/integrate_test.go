package integrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/spec/model"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	wsintegrate "github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// fakeGitHub is a minimal stateful GitHub: one PR, scriptable check runs.
type fakeGitHub struct {
	mu       sync.Mutex
	headOf   func(branch string) string
	pr       *ops.PR
	checks   []ops.CheckRun
	requests []string
	// runs answers OP-08 for a head_sha filter; logs answers OP-10 by job id.
	runs []ops.Run
	logs map[int64]string
	// baseRun, if set, is the push run of the default branch; baseChecks are
	// the check runs of its head.
	baseRun    *ops.Run
	baseChecks []ops.CheckRun
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	out := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.Method + " " + r.URL.Path {
	case "GET /rate_limit":
		out(map[string]any{"resources": map[string]any{"core": map[string]any{"limit": 5000, "remaining": 5000, "reset": 1_800_003_600}}})
	case "GET /repos/acme/widgets/pulls":
		list := []ops.PR{}
		if f.pr != nil {
			list = append(list, *f.pr)
		}
		out(list)
	case "POST /repos/acme/widgets/pulls":
		var in ops.NewPR
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.pr = &ops.PR{Number: 7, State: "open", Title: in.Title, Head: ops.Ref{Ref: in.Head, SHA: f.headOf(in.Head)}, Base: ops.Ref{Ref: in.Base}}
		w.WriteHeader(http.StatusCreated)
		out(f.pr)
	case "GET /repos/acme/widgets/pulls/7":
		out(f.pr)
	case "GET /repos/acme/widgets/actions/runs":
		if r.URL.Query().Get("head_sha") != "" {
			out(map[string]any{"workflow_runs": f.runs})
			return
		}
		base := ops.Run{ID: 1, Status: "completed", Conclusion: "success", Event: "push", HeadBranch: "main"}
		if f.baseRun != nil {
			base = *f.baseRun
		}
		out(map[string]any{"workflow_runs": []ops.Run{base}})
	default:
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			if f.baseRun != nil && strings.Contains(r.URL.Path, f.baseRun.HeadSHA) {
				out(map[string]any{"check_runs": f.baseChecks})
				return
			}
			out(map[string]any{"check_runs": f.checks})
			return
		}
		if strings.Contains(r.URL.Path, "/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs") {
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/actions/jobs/"), "%d", &id)
			if _, ok := f.logs[id]; ok { // like GitHub: 302 to blob storage
				http.Redirect(w, r, fmt.Sprintf("/blob-logs/%d", id), http.StatusFound)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/blob-logs/") {
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/blob-logs/"), "%d", &id)
			if text, ok := f.logs[id]; ok {
				_, _ = w.Write([]byte(text))
				return
			}
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/runs/") && strings.HasSuffix(r.URL.Path, "/rerun-failed-jobs") {
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.NotFound(w, r)
	}
}

type world struct {
	t       *testing.T
	root    string
	remote  string
	git     *wsgit.Runner
	integ   *wsintegrate.Dir
	gh      *fakeGitHub
	env     Env
	clk     *clock.Fake
	headSHA string
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
	integ, err := wsintegrate.Init(ctx, gr, m, filepath.Join(root, "integ"), wsgit.Identity{Name: "foreman", Email: "foreman@users.noreply.example"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.Create(ctx, gr, m, filepath.Join(root, "sess"), session.Spec{ReqID: "r1", BaseBranch: "main",
		Identity: wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}})
	if err != nil {
		t.Fatal(err)
	}
	// A merged-back step so that foreman/r1 exists in the integration dir.
	if err := os.WriteFile(filepath.Join(sess.Dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must(gr.Run(ctx, sess.Dir, "add", "-A"))
	must(gr.Run(ctx, sess.Dir, "commit", "-q", "-m", "step"))
	if err := integ.SyncFromMirror(ctx); err != nil {
		t.Fatal(err)
	}
	ref := wsintegrate.StepRefPrefix + "steps/r1/S1"
	if _, err := integ.FetchFromSession(ctx, sess.Dir, sess.Branch, ref); err != nil {
		t.Fatal(err)
	}
	res, err := integ.SquashMergeBack(ctx, wsintegrate.MergeSpec{ReqID: "r1", StepID: "r1/S1", StepRef: ref, Subject: "step"})
	if err != nil {
		t.Fatal(err)
	}

	gh := &fakeGitHub{headOf: func(branch string) string {
		sha, _ := gr.Line(ctx, remote, "rev-parse", "refs/heads/"+branch)
		return sha
	}}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	meters := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meters, Ledger: ledger.New()})
	c := client.New(client.Config{BaseURL: srv.URL, Token: "tok", Meters: meters, Clock: clk})
	o := ops.New(ops.Config{Client: c, Gate: g, Meters: meters, Clock: clk})
	if err := o.EnsureFresh(ctx); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, root: root, remote: remote, git: gr, integ: integ, gh: gh, clk: clk, headSHA: res.SHA,
		env: Env{Integ: integ, Ops: o, Repo: ops.Repo{Owner: "acme", Name: "widgets"}}}
}

func (w *world) req(deadlinePassed bool, pr int) Request {
	return Request{ReqID: "r1", Title: "r1: add b", PR: pr, Priority: gate.P2, DeadlinePassed: deadlinePassed}
}

func TestIntegrateStagePushesCreatesPRAndPollsL1(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	defaultBefore, _ := w.git.Line(ctx, w.remote, "rev-parse", "refs/heads/main")

	// Round 1: push and create the PR; L1 has not started.
	res, err := Run(ctx, w.env, w.req(false, 0))
	if err != nil || res.Event != nil || res.PR != 7 || res.Head != w.headSHA {
		t.Fatalf("round 1: %+v err %v", res, err)
	}
	if got, _ := w.git.Line(ctx, w.remote, "rev-parse", "refs/heads/foreman/r1"); got != w.headSHA {
		t.Fatalf("remote foreman/r1 = %s, want %s", got, w.headSHA)
	}
	if got, _ := w.git.Line(ctx, w.remote, "rev-parse", "refs/heads/main"); got != defaultBefore {
		t.Fatal("the default branch moved (D-050)")
	}

	// Round 2: PR found by head branch, not created again; no check runs yet
	// is not a pass, and ci_progress appears only once the deadline passed.
	res, err = Run(ctx, w.env, w.req(false, 7))
	if err != nil || res.Event != nil {
		t.Fatalf("round 2: %+v err %v", res, err)
	}
	// No run found at the deadline is deadline_passed (S33, ACR-24); a run in
	// progress is ci_progress (S38).
	res, err = Run(ctx, w.env, w.req(true, 7))
	if err != nil || res.Event == nil || res.Event.Name != model.EventDeadlinePassed {
		t.Fatalf("round 2 at deadline: %+v err %v", res, err)
	}
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "in_progress", HeadSHA: w.headSHA}}
	res, err = Run(ctx, w.env, w.req(true, 7))
	if err != nil || res.Event == nil || res.Event.Name != model.EventCIProgress {
		t.Fatalf("in progress at deadline: %+v err %v", res, err)
	}
	w.gh.checks = nil
	creates := 0
	for _, r := range w.gh.requests {
		if r == "POST /repos/acme/widgets/pulls" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("PR created %d times", creates)
	}

	// In progress: still pending. Success: l1_pass with PR and head.
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "in_progress", HeadSHA: w.headSHA}}
	if res, err = Run(ctx, w.env, w.req(false, 7)); err != nil || res.Event != nil {
		t.Fatalf("in progress: %+v err %v", res, err)
	}
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: "success", HeadSHA: w.headSHA}}
	res, err = Run(ctx, w.env, w.req(false, 7))
	if err != nil || res.Event == nil || res.Event.Name != model.EventL1Pass || res.Event.PR != 7 || res.Event.Head != w.headSHA {
		t.Fatalf("success: %+v err %v", res, err)
	}
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "unit", Status: "completed", Conclusion: "failure", HeadSHA: w.headSHA}}
	res, err = Run(ctx, w.env, w.req(false, 7))
	if err != nil || res.Event == nil || res.Event.Name != model.EventL1Fail || fmt.Sprint(res.Event.FailingTests) != "[unit]" {
		t.Fatalf("failure: %+v err %v", res, err)
	}
}
