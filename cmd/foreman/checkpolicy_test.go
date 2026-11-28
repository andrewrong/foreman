package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// L0 runs AI-written code, so the check policy keeps integrate/ denyWrite
// (access.yaml layout); the l0 checkout under runs/<id>/ is still accepted.
func TestCheckPolicyKeepsIntegrateDenyWrite(t *testing.T) {
	t.Parallel()
	d, err := os.MkdirTemp("/tmp", "fmc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	home, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	integ := filepath.Join(home, "projects/p/integrate")
	for _, sub := range []string{"db", "config", "projects/p/repo.git/objects", "projects/p/integrate", "cache/p", "runs/r1/l0"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mirror := filepath.Join(home, "projects/p/repo.git")
	cfg := Config{Home: home, Project: "p", Path: "/usr/bin"}
	cfg.SessionGit.Name, cfg.SessionGit.Email = "s", "s@users.noreply.example"
	in := sessionPolicy(cfg, integ, mirror, "main")
	deny := []string{filepath.Join(home, "db"), filepath.Join(home, "config"), mirror, integ}
	if len(in.DenyWrite) != len(deny) {
		t.Fatalf("DenyWrite %v, want %v", in.DenyWrite, deny)
	}
	found := false
	for _, d := range in.DenyWrite {
		found = found || d == integ
	}
	if !found {
		t.Fatalf("integrate/ missing from DenyWrite %v", in.DenyWrite)
	}
	if in.MirrorRepo != mirror || in.SessionGit.DefaultBranch != "main" {
		t.Fatalf("mirror or default branch not wired: %+v", in)
	}
	in.SessionRepo, in.RunTmp = filepath.Join(home, "runs/r1/l0"), filepath.Join(home, "runs/r1")
	if _, err := policy.New(in); err != nil {
		t.Fatalf("policy refuses the l0 working directory: %v", err)
	}
	in.SessionRepo = integ
	if _, err := policy.New(in); err == nil {
		t.Fatal("policy accepts the integration directory as working directory")
	}
}
