package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A refreshed session repo has the requirement branch's new head checked out,
// and nothing planted in the old repo (a hook, core.hooksPath, core.fsmonitor,
// a filter) runs: git is never executed on the old repo, which is only renamed
// and deleted (D-056).
func TestSessionRefreshDoesNotRunSessionConfig(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	m, err := CloneMirror(ctx, r, MirrorDir(r.Home(), "p"), seedRemote(t, r))
	mustErr(t, err)
	root := t.TempDir()
	dir := filepath.Join(root, "sess")
	_, err = Create(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "main", Identity: sessionIdent()})
	mustErr(t, err)

	// What a hostile session would leave behind: every way git runs a program.
	marker := filepath.Join(root, "executed")
	script := filepath.Join(root, "evil.sh")
	mustErr(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	hooks := filepath.Join(dir, ".git", "hooks")
	mustErr(t, os.MkdirAll(hooks, 0o700))
	for _, h := range []string{"post-checkout", "reference-transaction", "post-merge", "pre-commit"} {
		mustErr(t, os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	}
	cfg := filepath.Join(dir, ".git", "config")
	for k, v := range map[string]string{
		"core.hooksPath":            hooks,
		"core.fsmonitor":            script,
		"filter.x.smudge":           script,
		"filter.x.process":          script,
		"core.sshCommand":           script,
		"core.alternateRefsCommand": script,
	} {
		mustErr(t, r.SetConfig(ctx, cfg, k, v))
	}
	mustErr(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=x\n"), 0o600))
	mustErr(t, os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("uncommitted\n"), 0o600))

	// The requirement branch moves on in foremand's integration directory.
	integ := filepath.Join(root, "integ")
	must(r.Run(ctx, "", "clone", "--no-checkout", "--", m.Dir, integ))
	must(r.Run(ctx, integ, "checkout", "-q", "-b", "foreman/r1", "origin/main"))
	mustErr(t, os.WriteFile(filepath.Join(integ, "b.txt"), []byte("from step 1\n"), 0o600))
	must(r.Run(ctx, integ, "add", "-A"))
	must(r.Run(ctx, integ, "commit", "-q", "-m", "step 1"))
	tip := must(r.Line(ctx, integ, "rev-parse", "HEAD"))
	mustErr(t, m.ImportBranch(ctx, integ, "foreman/r1"))

	repo, err := Refresh(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "foreman/r1", Identity: sessionIdent()})
	mustErr(t, err)

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program planted in the session repo ran during the refresh")
	}
	if repo.Dir != dir || repo.Branch != "foreman/r1" {
		t.Errorf("repo %+v", repo)
	}
	if got := must(r.Line(ctx, "", "--git-dir", filepath.Join(dir, ".git"), "rev-parse", "HEAD")); got != tip {
		t.Errorf("session head %s, want the requirement head %s", got, tip)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "b.txt")); err != nil || string(got) != "from step 1\n" {
		t.Errorf("working tree lacks the merged file: %q %v", got, err)
	}
	for _, gone := range []string{"leftover.txt", ".gitattributes", filepath.Join(".git", "hooks", "post-checkout")} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s of the old session repo survived the refresh", gone)
		}
	}
	for _, sib := range []string{dir + ".new", dir + ".old"} {
		if _, err := os.Stat(sib); err == nil {
			t.Errorf("%s left behind", sib)
		}
	}
	// Idempotent: a repeat (merge-back is re-run after a crash) works.
	if _, err := Refresh(ctx, r, m, dir, Spec{ReqID: "r1", BaseBranch: "foreman/r1", Identity: sessionIdent()}); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
}
