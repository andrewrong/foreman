package integrate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

var (
	sessIdent  = git.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}
	integIdent = git.Identity{Name: "foreman", Email: "foreman@users.noreply.example"}
)

func must(out string, err error) string {
	if err != nil {
		panic(err)
	}
	return out
}

func mustErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// recorder wraps OSExec, records every Command and optionally runs after(cmd).
type recorder struct {
	mu    sync.Mutex
	cmds  []git.Command
	after func(git.Command)
}

func (rc *recorder) exec(ctx context.Context, c git.Command) (git.Result, error) {
	res, err := git.OSExec(ctx, c)
	rc.mu.Lock()
	rc.cmds = append(rc.cmds, c)
	hook := rc.after
	rc.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	return res, err
}

// subcommand returns the git args after the leading "-c key=value" pairs.
func subcommand(c git.Command, _ *git.Runner) []string {
	a := c.Args
	for len(a) >= 2 && a[0] == "-c" {
		a = a[2:]
	}
	return a
}

func newRunner(t *testing.T, ex git.ExecFunc) *git.Runner {
	t.Helper()
	r, err := git.New(git.Config{
		Home:  filepath.Join(t.TempDir(), "home"),
		Clock: clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Ident: &git.Identity{Name: "seed", Email: "seed@users.noreply.example"},
		Exec:  ex,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// seedRemote creates a bare remote with one commit on branch def.
func seedRemote(t *testing.T, r *git.Runner, def string) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mustErr(t, os.MkdirAll(work, 0o700))
	must(r.Run(ctx, "", "init", "--bare", "-b", def, remote))
	must(r.Run(ctx, work, "init", "-b", def))
	writeFiles(t, work, map[string]string{"a.txt": "a\n"})
	must(r.Run(ctx, work, "add", "-A"))
	must(r.Run(ctx, work, "commit", "-m", "seed"))
	must(r.Run(ctx, work, "push", remote, "HEAD:refs/heads/"+def))
	return remote
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		mustErr(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
}

// world is a remote, mirror, integration dir and one session repo for req r1.
type world struct {
	t      *testing.T
	r      *git.Runner
	rec    *recorder
	remote string
	mirror *session.Mirror
	integ  *Dir
	sess   *session.Repo
}

func newWorld(t *testing.T, def string) *world {
	t.Helper()
	ctx := context.Background()
	rec := &recorder{}
	r := newRunner(t, rec.exec)
	remote := seedRemote(t, r, def)
	m, err := session.CloneMirror(ctx, r, session.MirrorDir(r.Home(), "p"), remote)
	mustErr(t, err)
	d, err := Init(ctx, r, m, filepath.Join(t.TempDir(), "integ"), integIdent)
	mustErr(t, err)
	repo, err := session.Create(ctx, r, m, filepath.Join(t.TempDir(), "sess"),
		session.Spec{ReqID: "r1", BaseBranch: def, Identity: sessIdent})
	mustErr(t, err)
	rec.cmds = nil
	return &world{t: t, r: r, rec: rec, remote: remote, mirror: m, integ: d, sess: repo}
}

// commitOnBranch (test setup only) creates/resets branch at from inside the
// session repo and commits the files there.
func (w *world) commitOnBranch(branch, from string, files map[string]string, msg string) {
	w.t.Helper()
	ctx := context.Background()
	s := w.r.WithIdentity(sessIdent)
	must(s.Run(ctx, w.sess.Dir, "checkout", "--quiet", "-B", branch, from))
	writeFiles(w.t, w.sess.Dir, files)
	must(s.Run(ctx, w.sess.Dir, "add", "-A"))
	must(s.Run(ctx, w.sess.Dir, "commit", "--allow-empty", "-m", msg))
}

func (w *world) fetchStep(branch, id string) string {
	w.t.Helper()
	sha, err := w.integ.FetchFromSession(context.Background(), w.sess.Dir, branch, StepRefPrefix+"steps/"+id)
	mustErr(w.t, err)
	return sha
}

func (w *world) integLine(args ...string) string {
	w.t.Helper()
	return must(w.r.Line(context.Background(), w.integ.Path, args...))
}
