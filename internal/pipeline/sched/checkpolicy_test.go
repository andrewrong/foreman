package sched

import (
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
)

// L0 runs AI-written code, so the check policy keeps integrate/ denyWrite
// (access.yaml layout); the l0 checkout under runs/<id>/ is still accepted.
func TestCheckPolicyKeepsIntegrateDenyWrite(t *testing.T) {
	integ := &integrate.Dir{Path: "/h/projects/p/integrate"}
	deny := []string{"/h/db", "/h/config", "/h/projects/p/repo.git", integ.Path}
	s := &Scheduler{d: Deps{Integ: integ, Policy: policy.Inputs{DenyWrite: deny, Path: "/usr/bin"}}}
	in := s.checkPolicy()
	if len(in.DenyWrite) != len(deny) {
		t.Fatalf("DenyWrite %v, want %v", in.DenyWrite, deny)
	}
	found := false
	for _, d := range in.DenyWrite {
		found = found || d == integ.Path
	}
	if !found {
		t.Fatalf("integrate/ missing from DenyWrite %v", in.DenyWrite)
	}
	in.SessionRepo, in.RunTmp = "/h/runs/r1/l0", "/h/runs/r1"
	if _, err := policy.New(in); err != nil {
		t.Fatalf("policy refuses the l0 working directory: %v", err)
	}
	in.SessionRepo = integ.Path
	if _, err := policy.New(in); err == nil {
		t.Fatal("policy accepts the integration directory as working directory")
	}
}
