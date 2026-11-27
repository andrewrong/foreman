package integrate

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// readyToPush merges one step so the requirement branch has work to push.
func readyToPush(t *testing.T, w *world) {
	t.Helper()
	w.commitOnBranch("step/s1", w.sess.Branch, map[string]string{"one.txt": "1\n"}, "s1")
	w.fetchStep("step/s1", "s1")
	if _, err := w.merge("s1", StepRefPrefix+"steps/s1"); err != nil {
		t.Fatal(err)
	}
}

func remoteRef(w *world, ref string) string {
	return must(w.r.Line(context.Background(), w.remote, "rev-parse", "--verify", "--quiet", ref))
}

func TestPushRefspecOnly(t *testing.T) {
	w := newWorld(t, "main")
	readyToPush(t, w)
	mainBefore := remoteRef(w, "refs/heads/main")
	if err := w.integ.Push(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}

	want := []string{"push", "origin", "HEAD:refs/heads/foreman/r1"}
	var pushes int
	for _, c := range w.rec.cmds {
		sub := subcommand(c, w.r)
		if len(sub) == 0 || sub[0] != "push" {
			continue
		}
		if c.Dir != w.integ.Path {
			t.Fatalf("push ran in %s, not the integration dir", c.Dir)
		}
		if c.Dir == w.sess.Dir || !slices.Equal(sub, want) {
			t.Fatalf("push argv = %v, want exactly %v", sub, want)
		}
		pushes++
	}
	if pushes != 1 {
		t.Fatalf("expected exactly one push from the integration dir, saw %d", pushes)
	}
	if got, want := remoteRef(w, "refs/heads/foreman/r1"), w.integLine("rev-parse", "refs/heads/foreman/r1"); got != want {
		t.Fatalf("remote req branch %s != local %s", got, want)
	}
	if got := remoteRef(w, "refs/heads/main"); got != mainBefore {
		t.Fatalf("default branch changed: %s -> %s", mainBefore, got)
	}
}

func TestPushRefspecOnlyRefusesDefaultBranch(t *testing.T) {
	// The default branch is literally named like a requirement branch.
	w := newWorld(t, "foreman/r1")
	w.rec.cmds = nil
	err := w.integ.Push(context.Background(), "r1")
	if !errors.Is(err, ErrPushTargetIsDefault) {
		t.Fatalf("err = %v, want ErrPushTargetIsDefault", err)
	}
	for _, c := range w.rec.cmds {
		if sub := subcommand(c, w.r); len(sub) > 0 && sub[0] == "push" {
			t.Fatalf("a push was attempted: %v", sub)
		}
	}
	if err := w.integ.Push(context.Background(), "../main"); err == nil {
		t.Fatal("invalid req id accepted")
	}
}

func TestPushAssertsDefaultUnchanged(t *testing.T) {
	w := newWorld(t, "main")
	readyToPush(t, w)

	// Another party moves main on the remote right after our push completes,
	// i.e. between the pre and the post ls-remote check.
	other := w.r.WithIdentity(sessIdent)
	moved := false
	w.rec.after = func(c git.Command) {
		sub := subcommand(c, w.r)
		if moved || len(sub) == 0 || sub[0] != "push" {
			return
		}
		moved = true
		work := filepath.Join(t.TempDir(), "other")
		ctx := context.Background()
		must(other.Run(ctx, "", "clone", "--quiet", w.remote, work))
		must(other.Run(ctx, work, "commit", "--allow-empty", "-m", "owner moves main"))
		must(other.Run(ctx, work, "push", "origin", "HEAD:refs/heads/main"))
	}
	before := remoteRef(w, "refs/heads/main")
	err := w.integ.Push(context.Background(), "r1")
	var moved2 *DefaultBranchMovedError
	if !errors.As(err, &moved2) {
		t.Fatalf("err = %v, want *DefaultBranchMovedError", err)
	}
	if moved2.Before != before || moved2.After == before || moved2.After != remoteRef(w, "refs/heads/main") {
		t.Fatalf("unexpected shas: %+v (before %s)", moved2, before)
	}
	if !w.integ.Halted() {
		t.Fatal("pushes not halted after default branch moved")
	}
	w.rec.cmds = nil
	if err := w.integ.Push(context.Background(), "r1"); !errors.Is(err, ErrPushHalted) {
		t.Fatalf("second push err = %v, want ErrPushHalted", err)
	}
	if len(w.rec.cmds) != 0 {
		t.Fatalf("halted Push still ran git: %d commands", len(w.rec.cmds))
	}
}
