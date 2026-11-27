package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/workspace/git"
)

var sessionIdent = git.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}

func gitIdent(n, e string) git.Identity { return git.Identity{Name: n, Email: e} }

func newRunner(t *testing.T) *git.Runner {
	t.Helper()
	r, err := git.New(git.Config{
		Home:  filepath.Join(t.TempDir(), "home"),
		Clock: clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Ident: &git.Identity{Name: "seed", Email: "seed@users.noreply.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

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

// seedRemote creates a bare "remote" with one commit on main and returns its path.
func seedRemote(t *testing.T, r *git.Runner) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mustErr(t, os.MkdirAll(work, 0o700))
	must(r.Run(ctx, "", "init", "--bare", "-b", "main", remote))
	must(r.Run(ctx, work, "init", "-b", "main"))
	mustErr(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o600))
	must(r.Run(ctx, work, "add", "-A"))
	must(r.Run(ctx, work, "commit", "-m", "seed"))
	must(r.Run(ctx, work, "push", remote, "HEAD:refs/heads/main"))
	return remote
}
