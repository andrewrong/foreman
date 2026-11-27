package policy

import (
	"strings"
	"testing"
)

// access.yaml layout: denyWrite lists subdirectories of FOREMAN_HOME, and
// sessions/ and runs/ live in the same home outside them.
func TestPolicyDenyWriteSubdirsOfHome(t *testing.T) {
	in := base()
	in.ForemanHome = ""
	in.DenyWrite = []string{"/h/.foreman/db", "/h/.foreman/projects/p/repo.git"}
	in.SessionRepo = "/h/.foreman/projects/p/sessions/r1"
	in.RunTmp = "/h/.foreman/runs/r1"
	p := mustNew(t, in)
	got := strings.Join(p.DenyWritePaths(), ",")
	for _, want := range []string{"/h/.foreman/db", "/h/.foreman/projects/p/repo.git", "/h/.foreman/cache/p"} {
		if !strings.Contains(got, want) {
			t.Errorf("denyWrite %q lacks %s", got, want)
		}
	}
	in.SessionRepo = "/h/.foreman/db/x"
	if _, err := New(in); err == nil {
		t.Error("session repo inside a DenyWrite dir accepted")
	}
	in.SessionRepo = "/h/.foreman/projects/p/sessions/r1"
	in.DenyWrite = []string{"relative"}
	if _, err := New(in); err == nil {
		t.Error("relative DenyWrite accepted")
	}
	in.DenyWrite = nil
	if _, err := New(in); err == nil {
		t.Error("no deny-write at all accepted")
	}
}
