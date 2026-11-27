package merge

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

// gitHub is a stateful fake for one PR whose head is the remote branch
// foreman/r1: update-branch merges main into it for real.
type gitHub struct {
	mu       sync.Mutex
	w        *world
	checks   []ops.CheckRun // answered for the PR head; nil = one green run
	merged   bool
	requests []string
	merges   []map[string]string
	bad      string // conclusion of the base's push run
}

func (g *gitHub) count(req string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, r := range g.requests {
		if r == req {
			n++
		}
	}
	return n
}

func (g *gitHub) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r.Method+" "+r.URL.Path)
	out := func(v any) { _ = json.NewEncoder(rw).Encode(v) }
	const base = "/repos/acme/widgets"
	head := g.w.remoteSHA("refs/heads/foreman/r1")
	switch r.Method + " " + r.URL.Path {
	case "GET /rate_limit":
		out(map[string]any{"resources": map[string]any{"core": map[string]any{"limit": 5000, "remaining": 5000, "reset": 1_800_003_600}}})
	case "GET " + base + "/pulls":
		out([]ops.PR{{Number: 7, State: "open", Head: ops.Ref{Ref: "foreman/r1", SHA: head}}})
	case "GET " + base + "/pulls/7":
		out(ops.PR{Number: 7, State: "open", Merged: g.merged, Head: ops.Ref{Ref: "foreman/r1", SHA: head}})
	case "GET " + base + "/actions/runs":
		out(map[string]any{"workflow_runs": []ops.Run{{ID: 1, Status: "completed", Conclusion: g.bad, Event: "push", HeadBranch: "main"}}})
	case "PUT " + base + "/pulls/7/update-branch":
		g.w.mergeMain()
		rw.WriteHeader(http.StatusAccepted)
		out(map[string]string{"message": "Updating pull request branch."})
	case "PUT " + base + "/pulls/7/merge":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.merges = append(g.merges, body)
		g.merged = true
		out(ops.MergeResult{SHA: "m3rge", Merged: true})
	default:
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			checks := g.checks
			if checks == nil {
				checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: "success"}}
			}
			out(map[string]any{"check_runs": checks})
			return
		}
		http.NotFound(rw, r)
	}
}

type world struct {
	t      *testing.T
	git    *wsgit.Runner
	remote string
	work   string
	integ  *wsintegrate.Dir
	env    Env
	gh     *gitHub
	head   string // the PR head after setup
}

func (w *world) remoteSHA(ref string) string {
	out, err := w.git.Line(context.Background(), w.remote, "rev-parse", ref)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) run(args ...string) {
	w.t.Helper()
	if _, err := w.git.Run(context.Background(), w.work, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) write(files map[string]string) {
	for n, body := range files {
		p := filepath.Join(w.work, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			w.t.Fatal(err)
		}
	}
}

// moveMain commits files to main on the remote.
func (w *world) moveMain(files map[string]string) {
	w.t.Helper()
	w.run("checkout", "-q", "main")
	w.write(files)
	w.run("add", "-A")
	w.run("commit", "-q", "-m", "main moves")
	w.run("push", "-q", w.remote, "HEAD:refs/heads/main")
}

// mergeMain is update-branch: main merged into foreman/r1 on the remote.
func (w *world) mergeMain() {
	w.run("fetch", "-q", w.remote, "+refs/heads/foreman/r1:refs/remotes/r/pr", "+refs/heads/main:refs/remotes/r/main")
	w.run("checkout", "-q", "-B", "pr", "refs/remotes/r/pr")
	w.run("merge", "-q", "--no-edit", "refs/remotes/r/main")
	w.run("push", "-q", w.remote, "HEAD:refs/heads/foreman/r1")
}

// newWorld builds a remote with a.txt and b.txt, a PR branch foreman/r1 that
// changes pr (path -> body), the mirror, the integration dir and the fake
// GitHub. The PR head is recorded as last reviewed by the tests that need it.
func newWorld(t *testing.T, pr map[string]string) *world {
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
	w := &world{t: t, git: gr, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	if err := os.MkdirAll(w.work, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := gr.Run(ctx, "", "init", "--bare", "-b", "main", w.remote); err != nil {
		t.Fatal(err)
	}
	w.run("init", "-q", "-b", "main")
	w.write(map[string]string{"a.txt": lines("a1", "a10"), "b.txt": "b\n"})
	w.run("add", "-A")
	w.run("commit", "-q", "-m", "seed")
	w.run("push", "-q", w.remote, "HEAD:refs/heads/main")

	m, err := session.CloneMirror(ctx, gr, session.MirrorDir(gr.Home(), "p"), w.remote)
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
	for n, body := range pr {
		p := filepath.Join(sess.Dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "step"}} {
		if _, err := gr.Run(ctx, sess.Dir, a...); err != nil {
			t.Fatal(err)
		}
	}
	if err := integ.SyncFromMirror(ctx); err != nil {
		t.Fatal(err)
	}
	ref := wsintegrate.StepRefPrefix + "steps/r1/S1"
	if _, err := integ.FetchFromSession(ctx, sess.Dir, sess.Branch, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := integ.SquashMergeBack(ctx, wsintegrate.MergeSpec{ReqID: "r1", StepID: "r1/S1", StepRef: ref, Subject: "step"}); err != nil {
		t.Fatal(err)
	}
	if err := integ.Push(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	w.integ, w.head = integ, w.remoteSHA("refs/heads/foreman/r1")

	w.gh = &gitHub{w: w, bad: "success"}
	srv := httptest.NewServer(w.gh)
	t.Cleanup(srv.Close)
	meters := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meters, Ledger: ledger.New()})
	o := ops.New(ops.Config{Client: client.New(client.Config{BaseURL: srv.URL, Token: "tok", Meters: meters, Clock: clk}), Gate: g, Meters: meters, Clock: clk})
	if err := o.EnsureFresh(ctx); err != nil {
		t.Fatal(err)
	}
	w.env = Env{Ops: o, Repo: ops.Repo{Owner: "acme", Name: "widgets"}, Integ: integ, Mirror: m,
		Guardrails: []model.Guardrail{{ID: "G-05", Glob: ".github/**"}, {ID: "G-09", Glob: "internal/workspace/**"}}}
	return w
}

func (w *world) req() Request {
	return Request{ReqID: "r1", PR: 7, Head: w.head, Priority: gate.P2, LastReviewed: w.head,
		MarkRerun: func(context.Context, string) error { return nil }}
}

func eventName(ev *model.Event) string {
	if ev == nil {
		return ""
	}
	return ev.Name
}

// test:guardrail-diff-needs-h6 (GR-1): only the real diff decides. A diff
// touching a guardrail path is guardrail_hit with the rule and file, before any
// merge call; the owner's approval of exactly this head lets it through; any
// other head asks again; a clean diff just merges.
func TestGuardrailDiffNeedsH6(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, map[string]string{"internal/workspace/x.go": "package workspace\n"})
	res, err := Run(ctx, w.env, w.req())
	if err != nil || eventName(res.Event) != model.EventGuardrailHit || res.Event.Head != w.head ||
		!strings.Contains(res.Event.Detail, "G-09 internal/workspace/x.go") {
		t.Fatalf("hit: %+v err %v", res.Event, err)
	}
	if len(w.gh.merges) != 0 {
		t.Fatal("merged before the owner decided")
	}

	r := w.req()
	r.GuardrailApproved = "0000000000000000000000000000000000000000"
	if res, err = Run(ctx, w.env, r); err != nil || eventName(res.Event) != model.EventGuardrailHit {
		t.Fatalf("approval of another head must not count: %+v err %v", res.Event, err)
	}

	r.GuardrailApproved = w.head
	res, err = Run(ctx, w.env, r)
	if err != nil || eventName(res.Event) != model.EventMerged || len(w.gh.merges) != 1 || w.gh.merges[0]["sha"] != w.head {
		t.Fatalf("approved head: %+v err %v merges %+v", res.Event, err, w.gh.merges)
	}

	clean := newWorld(t, map[string]string{"c.txt": "c\n"})
	if res, err = Run(ctx, clean.env, clean.req()); err != nil || eventName(res.Event) != model.EventMerged {
		t.Fatalf("clean diff: %+v err %v", res.Event, err)
	}
}

// The base moved: update-branch is requested (once, nothing else happens that
// round); next round the new head is reported, and base changes that do not
// touch the PR's files only need L1 (merged here, L1 is green).
func TestRunUpdatesBranchThenMergesWhenFilesAreDisjoint(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, map[string]string{"c.txt": "c\n"})
	w.moveMain(map[string]string{"b.txt": "b changed\n"})

	res, err := Run(ctx, w.env, w.req())
	if err != nil || res.Event != nil || res.Head != w.head {
		t.Fatalf("round 1: %+v head %s err %v", res.Event, res.Head, err)
	}
	if n := w.gh.count("PUT /repos/acme/widgets/pulls/7/update-branch"); n != 1 || len(w.gh.merges) != 0 {
		t.Fatalf("update-branch %d times, merges %+v", n, w.gh.merges)
	}
	res, err = Run(ctx, w.env, w.req())
	newHead := w.remoteSHA("refs/heads/foreman/r1")
	if err != nil || eventName(res.Event) != model.EventMerged || res.Head != newHead || newHead == w.head {
		t.Fatalf("round 2: %+v head %s (remote %s, old %s) err %v", res.Event, res.Head, newHead, w.head, err)
	}
	if w.gh.merges[0]["sha"] != newHead {
		t.Errorf("merge pinned %s, want the new head %s", w.gh.merges[0]["sha"], newHead)
	}
}

// Base changes to a file the PR also changed (different hunks, so GitHub's
// merge is clean): after update-branch the head moved since the last review
// and the files intersect, so needs_rereview names the file. Pinned to the new
// head nothing is merged yet.
func TestRunNeedsRereviewWhenBaseChangedPRFiles(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, map[string]string{"a.txt": lines("a by the PR", "a10")})
	w.moveMain(map[string]string{"a.txt": lines("a1", "a10 by main")})
	if res, err := Run(ctx, w.env, w.req()); err != nil || res.Event != nil {
		t.Fatalf("update round: %+v err %v", res.Event, err)
	}
	res, err := Run(ctx, w.env, w.req())
	if err != nil || eventName(res.Event) != model.EventNeedsRereview || res.Event.Head != w.remoteSHA("refs/heads/foreman/r1") ||
		!strings.Contains(res.Event.Detail, "a.txt") {
		t.Fatalf("event %+v err %v", res.Event, err)
	}
	if len(w.gh.merges) != 0 {
		t.Error("merged without the re-review")
	}
	// Once the new head is the reviewed one (S43 records it) the merge goes on.
	r := w.req()
	r.LastReviewed = res.Event.Head
	if res, err = Run(ctx, w.env, r); err != nil || eventName(res.Event) != model.EventMerged {
		t.Fatalf("after the re-review: %+v err %v", res.Event, err)
	}
}

// L1 must be green on the head being merged: pending is silent until the
// deadline, then ci_progress (S48); a transient failure is rerun without an
// event; a real failure is l1_fail (S60).
func TestRunWaitsForL1OnTheHead(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, map[string]string{"c.txt": "c\n"})
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "in_progress"}}
	r := w.req()
	if res, err := Run(ctx, w.env, r); err != nil || res.Event != nil {
		t.Fatalf("pending: %+v err %v", res.Event, err)
	}
	r.DeadlinePassed = true
	if res, err := Run(ctx, w.env, r); err != nil || eventName(res.Event) != model.EventCIProgress {
		t.Fatalf("pending at the deadline: %+v err %v", res.Event, err)
	}
	w.gh.checks = []ops.CheckRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: "cancelled"}}
	if res, err := Run(ctx, w.env, w.req()); err != nil || res.Event != nil {
		t.Fatalf("cancelled job: %+v err %v (want a silent rerun)", res.Event, err)
	}
	r = w.req()
	r.Reruns = map[string]int{w.head: 1}
	res, err := Run(ctx, w.env, r)
	if err != nil || eventName(res.Event) != model.EventL1Fail || res.Event.Head != w.head {
		t.Fatalf("failure after the rerun: %+v err %v", res.Event, err)
	}
	if len(w.gh.merges) != 0 {
		t.Error("merged on a red head")
	}
}

func lines(first, last string) string {
	out := []string{first}
	for i := 2; i <= 9; i++ {
		out = append(out, fmt.Sprintf("a%d", i))
	}
	return strings.Join(append(out, last), "\n") + "\n"
}
