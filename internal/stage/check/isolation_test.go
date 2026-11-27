package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

// gitSpy runs git for real and records every Command; a command issued after
// the marker file exists (written by L0, i.e. after the sandbox started) is
// recorded as late.
type gitSpy struct {
	mu     sync.Mutex
	marker string
	cmds   []wsgit.Command
	late   []wsgit.Command
}

func (g *gitSpy) exec(ctx context.Context, c wsgit.Command) (wsgit.Result, error) {
	g.mu.Lock()
	g.cmds = append(g.cmds, c)
	if g.marker != "" {
		if _, err := os.Stat(g.marker); err == nil {
			g.late = append(g.late, c)
		}
	}
	g.mu.Unlock()
	return wsgit.OSExec(ctx, c)
}

// L0 is AI-written code: whatever it writes to ".git/config" relative to its
// working directory must land in its own throwaway checkout, never in the
// integration directory, and foremand must not run git again after L0 started
// (D-045, D-056, access.yaml layout).
func TestCheckStageL0IsolatedFromIntegrationDir(t *testing.T) {
	spy := &gitSpy{}
	w := newWorldExec(t, spy.exec)
	ctx := context.Background()
	w.commit(map[string]string{"sum.go": sumGo("a + b")})
	r := w.req()
	l0 := filepath.Join(r.RunDir, "l0")
	spy.marker = filepath.Join(w.root, "l0-started")
	cfgPath := filepath.Join(w.integ.Path, ".git", "config")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	evil := `printf '[url "file:///evil"]\n\tinsteadOf = origin\n[core]\n\tsshCommand = evil\n' >> .git/config; pwd -P > "$1"`
	r.Commands = [][]string{{"sh", "-c", evil, "sh", spy.marker}}

	ev, err := Run(ctx, w.env, r)
	if err != nil || ev.Name != model.EventCheckPass {
		t.Fatalf("event %+v err %v", ev, err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("integration .git/config changed (err %v):\n%s", err, after)
	}
	got, err := os.ReadFile(filepath.Join(l0, ".git", "config"))
	if err != nil || !strings.Contains(string(got), "file:///evil") {
		t.Fatalf("L0 did not write its own .git/config (err %v)", err)
	}
	cwd, err := os.ReadFile(spy.marker)
	if err != nil {
		t.Fatal(err)
	}
	wantCwd, _ := filepath.EvalSymlinks(l0)
	if strings.TrimSpace(string(cwd)) != wantCwd {
		t.Errorf("L0 cwd %q, want %q", cwd, wantCwd)
	}

	if len(spy.late) != 0 {
		t.Errorf("git ran after the sandbox started: %+v", spy.late)
	}
	inside, last := 0, spy.cmds[len(spy.cmds)-1]
	for _, c := range spy.cmds {
		if c.Dir == l0 || strings.HasPrefix(c.Dir, l0+"/") {
			inside++
		}
	}
	if inside != 1 || last.Dir != l0 {
		t.Errorf("want exactly one git command in the l0 checkout, and it last; got %d, last dir %q", inside, last.Dir)
	}
}

// A second check of the same Run replaces the previous (sandbox-written) checkout.
func TestCheckStageReplacesPreviousL0Checkout(t *testing.T) {
	w := newWorld(t)
	w.commit(map[string]string{"sum.go": sumGo("a + b")})
	r := w.req()
	r.Commands = [][]string{{"sh", "-c", "echo x > leftover"}}
	for i := 0; i < 2; i++ {
		if ev, err := Run(context.Background(), w.env, r); err != nil || ev.Name != model.EventCheckPass {
			t.Fatalf("run %d: %+v %v", i, ev, err)
		}
		r.Commands = [][]string{{"sh", "-c", "test ! -e leftover"}}
	}
}
