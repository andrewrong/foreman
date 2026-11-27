package integrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// otherClone is a second party's clone of the remote: it moves main and acts
// like GitHub's update-branch on the requirement branch.
func (w *world) otherClone() string {
	w.t.Helper()
	ctx := context.Background()
	dir := filepath.Join(w.t.TempDir(), "other")
	must(w.r.Run(ctx, "", "clone", "--quiet", w.remote, dir))
	return dir
}

func (w *world) moveMain(dir string, files map[string]string) {
	w.t.Helper()
	ctx := context.Background()
	must(w.r.Run(ctx, dir, "checkout", "--quiet", "main"))
	writeFiles(w.t, dir, files)
	must(w.r.Run(ctx, dir, "add", "-A"))
	must(w.r.Run(ctx, dir, "commit", "-q", "-m", "main moves"))
	must(w.r.Run(ctx, dir, "push", "-q", "origin", "HEAD:refs/heads/main"))
	mustErr(w.t, w.mirror.Update(ctx))
	mustErr(w.t, w.integ.SyncFromMirror(ctx))
}

func TestBaseDeltaAndPRFiles(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	readyToPush(t, w)
	head := w.integLine("rev-parse", "refs/heads/foreman/r1")
	if behind, err := w.integ.BehindBase(ctx, head); err != nil || behind {
		t.Fatalf("BehindBase before main moved = %v, %v", behind, err)
	}
	w.moveMain(w.otherClone(), map[string]string{"x.txt": "x\n"})
	if behind, err := w.integ.BehindBase(ctx, head); err != nil || !behind {
		t.Fatalf("BehindBase after main moved = %v, %v", behind, err)
	}
	base, err := w.integ.BaseFilesSince(ctx, head)
	if err != nil || !slices.Equal(base, []string{"x.txt"}) {
		t.Errorf("BaseFilesSince = %v, %v; want [x.txt]", base, err)
	}
	pr, err := w.integ.PRFiles(ctx, head)
	if err != nil || !slices.Equal(pr, []string{"one.txt"}) {
		t.Errorf("PRFiles = %v, %v; want [one.txt]", pr, err)
	}
}

func TestAdoptRemoteFastForwardsAfterUpdateBranch(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	readyToPush(t, w)
	mustErr(t, w.integ.Push(ctx, "r1"))
	oldHead := w.integLine("rev-parse", "refs/heads/foreman/r1")

	other := w.otherClone()
	w.moveMain(other, map[string]string{"x.txt": "x\n"})
	// What GitHub's update-branch does: merge main into the PR branch.
	must(w.r.Run(ctx, other, "checkout", "--quiet", "-B", "foreman/r1", "origin/foreman/r1"))
	must(w.r.Run(ctx, other, "merge", "--quiet", "--no-edit", "origin/main"))
	must(w.r.Run(ctx, other, "push", "-q", "origin", "HEAD:refs/heads/foreman/r1"))

	tip, err := w.integ.AdoptRemote(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if tip == oldHead || tip != w.integLine("rev-parse", "refs/heads/foreman/r1") {
		t.Fatalf("tip %s, local %s, old %s: local branch not fast-forwarded", tip, w.integLine("rev-parse", "refs/heads/foreman/r1"), oldHead)
	}
	if behind, err := w.integ.BehindBase(ctx, tip); err != nil || behind {
		t.Errorf("BehindBase after update-branch = %v, %v", behind, err)
	}
	if again, err := w.integ.AdoptRemote(ctx, "r1"); err != nil || again != tip {
		t.Errorf("second AdoptRemote = %s, %v", again, err)
	}
	if err := w.integ.Push(ctx, "r1"); err != nil {
		t.Errorf("push after adopt: %v", err)
	}
}

func TestAdoptRemoteRefusesDivergedBranch(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	readyToPush(t, w)
	mustErr(t, w.integ.Push(ctx, "r1"))
	other := w.otherClone()
	must(w.r.Run(ctx, other, "checkout", "--quiet", "-B", "foreman/r1", "origin/main"))
	if err := os.WriteFile(filepath.Join(other, "y.txt"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	must(w.r.Run(ctx, other, "add", "-A"))
	must(w.r.Run(ctx, other, "commit", "-q", "-m", "rewritten"))
	must(w.r.Run(ctx, other, "push", "-q", "--force", "origin", "HEAD:refs/heads/foreman/r1"))
	local := w.integLine("rev-parse", "refs/heads/foreman/r1")
	if _, err := w.integ.AdoptRemote(ctx, "r1"); err == nil {
		t.Fatal("AdoptRemote accepted a remote branch that does not contain the local tip")
	}
	if got := w.integLine("rev-parse", "refs/heads/foreman/r1"); got != local {
		t.Errorf("local branch moved to %s", got)
	}
}
