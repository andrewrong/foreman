package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

const goMod = "module example.test/sum\n\ngo 1.21\n"

func sumGo(body string) string {
	return "package sum\n\nfunc Sum(a, b int) int { return " + body + " }\n"
}

const sumTest = `package sum

import "testing"

func TestSum(t *testing.T) {
	if Sum(1, 2) != 3 {
		t.Fatal("Sum(1, 2) != 3")
	}
}
`

type world struct {
	t     *testing.T
	root  string
	git   *wsgit.Runner
	sess  *session.Repo
	integ *integrate.Dir
	env   Env
}

func newWorld(t *testing.T) *world { return newWorldExec(t, nil) }

func newWorldExec(t *testing.T, ex wsgit.ExecFunc) *world {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root, err := os.MkdirTemp("/tmp", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx := context.Background()
	gr, err := wsgit.New(wsgit.Config{Home: filepath.Join(root, "gh"), Clock: clock.NewFake(time.Unix(1_800_000_000, 0)),
		Ident: &wsgit.Identity{Name: "seed", Email: "seed@users.noreply.example"}, Exec: ex})
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
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte(goMod), 0o600); err != nil {
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
	return &world{t: t, root: root, git: gr, sess: sess, integ: integ, env: Env{Integ: integ,
		Launcher: launch.Launcher{PassThrough: true},
		// integrate/ stays denyWrite, as in the deployed policy.
		Policy: policy.Inputs{DenyWrite: []string{filepath.Join(root, "db"), integ.Path},
			Path: filepath.Dir(goBin) + ":" + wsgit.DefaultPath}}}
}

// commit stands in for the coder.
func (w *world) commit(files map[string]string) {
	w.t.Helper()
	ctx := context.Background()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(w.sess.Dir, name), []byte(body), 0o600); err != nil {
			w.t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "coder"}} {
		if _, err := w.git.Run(ctx, w.sess.Dir, args...); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) req() Request {
	return Request{RunID: "r1", StepID: "r1/S1", SessionRepo: w.sess.Dir, SessionBranch: w.sess.Branch,
		RunDir: filepath.Join(w.root, "runs", "r1"), Commands: [][]string{{"go", "test", "./..."}}}
}

// L0 runs a real command on the fetched head in the integration checkout:
// a passing head gives check_pass; a broken one gives check_fail with the
// failing test and a log tail (same RunDir twice keeps the Go cache warm).
func TestCheckStageRunsL0OnFetchedHead(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.commit(map[string]string{"sum.go": sumGo("a + b"), "sum_test.go": sumTest})
	good, _ := w.git.Line(ctx, w.sess.Dir, "rev-parse", "HEAD")
	ev, err := Run(ctx, w.env, w.req())
	if err != nil || ev.Name != model.EventCheckPass || ev.Head != good {
		t.Fatalf("event %+v err %v (want pass at %s)", ev, err, good)
	}
	// L0 ran on its own checkout at the fetched head, not in the integration directory.
	if got, _ := w.git.Line(ctx, filepath.Join(w.req().RunDir, "l0"), "rev-parse", "HEAD"); got != good {
		t.Errorf("l0 HEAD %s, want %s", got, good)
	}

	w.commit(map[string]string{"sum.go": sumGo("a - b")})
	ev, err = Run(ctx, w.env, w.req())
	if err != nil || ev.Name != model.EventCheckFail {
		t.Fatalf("event %+v err %v", ev, err)
	}
	if len(ev.FailingTests) != 1 || ev.FailingTests[0] != "TestSum" {
		t.Errorf("failing tests %q", ev.FailingTests)
	}
	if !strings.Contains(ev.LogTail, "Sum(1, 2) != 3") || strings.Count(ev.LogTail, "\n") >= MaxLogLines {
		t.Errorf("log tail %q", ev.LogTail)
	}
}

func TestCheckStageInfraWhenCommandCannotStart(t *testing.T) {
	w := newWorld(t)
	w.commit(map[string]string{"sum.go": sumGo("a + b")})
	r := w.req()
	r.Commands = [][]string{{filepath.Join(w.root, "no-such-binary")}}
	ev, err := Run(context.Background(), w.env, r)
	if err != nil || ev.Name != model.EventInfra {
		t.Fatalf("event %+v err %v", ev, err)
	}
}

func TestLogTailLimits(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString(strings.Repeat("x", 100) + "\n")
	}
	got := logTail(b.String())
	if len(got) > MaxLogBytes || strings.Count(got, "\n")+1 > MaxLogLines {
		t.Errorf("tail is %d bytes, %d lines", len(got), strings.Count(got, "\n")+1)
	}
}
