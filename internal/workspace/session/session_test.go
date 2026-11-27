package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRepoNoUpstream(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	remote := seedRemote(t, r)
	m, err := CloneMirror(ctx, r, MirrorDir(r.Home(), "p"), remote)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sess")
	repo, err := Create(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "main", Identity: sessionIdent})
	if err != nil {
		t.Fatal(err)
	}
	gd := filepath.Join(dir, ".git")
	cfg := filepath.Join(gd, "config")

	if out, err := r.Run(ctx, "", "config", "--file", cfg, "--get-regexp", `^(remote|branch)\.`); err == nil {
		t.Fatalf("session config still has remote/branch sections:\n%s", out)
	}
	if got := must(r.Line(ctx, "", "config", "--file", cfg, "push.default")); got != "nothing" {
		t.Fatalf("push.default = %q", got)
	}
	if got := must(r.Line(ctx, "", "--git-dir", gd, "for-each-ref", "refs/remotes")); got != "" {
		t.Fatalf("remote-tracking refs remain: %s", got)
	}
	if got := must(r.Line(ctx, "", "--git-dir", gd, "symbolic-ref", "HEAD")); got != "refs/heads/"+repo.Branch {
		t.Fatalf("HEAD = %s", got)
	}
	if _, err := os.Stat(filepath.Join(gd, "objects", "info", "alternates")); err != nil {
		t.Fatalf("not a --shared clone: %v", err)
	}
	// A push from the session cannot reach the mirror or the remote: there is no remote.
	if _, err := r.Run(ctx, dir, "push"); err == nil {
		t.Fatal("push from session repo unexpectedly succeeded")
	}
	if _, err := r.Run(ctx, dir, "push", "origin", "HEAD"); err == nil {
		t.Fatal("push to origin from session repo unexpectedly succeeded")
	}
}

func TestSessionGitIdentity(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	remote := seedRemote(t, r)
	m, err := CloneMirror(ctx, r, MirrorDir(r.Home(), "p"), remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, r, m, filepath.Join(t.TempDir(), "bad"), Spec{ReqID: "r1", BaseBranch: "main",
		Identity: gitIdent("Owner", "owner@example.com")}); !errors.Is(err, ErrIdentityNotNoreply) {
		t.Fatalf("non-noreply identity accepted: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "sess")
	if _, err := Create(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "main", Identity: sessionIdent}); err != nil {
		t.Fatal(err)
	}

	// A session-like process: owner identity in the user's HOME, the session
	// environment pins GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1.
	ownerHome := t.TempDir()
	owner := "[user]\n\tname = Owner\n\temail = owner@example.com\n"
	if err := os.WriteFile(filepath.Join(ownerHome, ".gitconfig"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + ownerHome, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LANG=C"}
	run := func(env []string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir, c.Env = dir, env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(env, "commit", "--allow-empty", "-m", "session work")
	got := run(env, "log", "-1", "--format=%an <%ae>|%cn <%ce>")
	want := sessionIdent.Name + " <" + sessionIdent.Email + ">"
	if got != want+"|"+want {
		t.Fatalf("session commit identity = %q, want %q", got, want)
	}
	// The mirror is unaffected by the session commit (sessions write only their own repo).
	if _, err := m.BranchSHA(ctx, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRemove(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	m, err := CloneMirror(ctx, r, MirrorDir(r.Home(), "p"), seedRemote(t, r))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sess")
	if _, err := Create(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "main", Identity: sessionIdent}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists: %v", err)
	}
	if err := Remove(t.TempDir()); err == nil {
		t.Fatal("Remove accepted a non-repo directory")
	}
}
