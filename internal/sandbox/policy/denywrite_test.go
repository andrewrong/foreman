package policy

import (
	"os"
	"strings"
	"testing"
)

// access.yaml layout: denyWrite lists subdirectories of FOREMAN_HOME, and
// sessions/ and runs/ live in the same home outside them.
func TestPolicyDenyWriteSubdirsOfHome(t *testing.T) {
	fx := newFixture(t)
	for _, sub := range []string{"h/.foreman/db", "h/.foreman/projects/p/repo.git", "h/.foreman/projects/p/sessions/r1", "h/.foreman/runs/r1"} {
		if err := os.MkdirAll(fx.p(sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	in := fx.in
	in.ForemanHome = ""
	in.DenyWrite = []string{fx.p("h/.foreman/db"), fx.p("h/.foreman/projects/p/repo.git")}
	in.SessionRepo = fx.p("h/.foreman/projects/p/sessions/r1")
	in.RunTmp = fx.p("h/.foreman/runs/r1")
	p := mustNew(t, in)
	got := strings.Join(p.DenyWritePaths(), ",")
	for _, want := range []string{fx.p("h/.foreman/db"), fx.p("h/.foreman/projects/p/repo.git"), fx.p("h/.foreman/cache/p")} {
		if !strings.Contains(got, want) {
			t.Errorf("denyWrite %q lacks %s", got, want)
		}
	}
	in.SessionRepo = fx.p("h/.foreman/db")
	if _, err := New(in); err == nil {
		t.Error("session repo inside a DenyWrite dir accepted")
	}
	in.SessionRepo = fx.p("h/.foreman/projects/p/sessions/r1")
	in.DenyWrite = []string{"relative"}
	if _, err := New(in); err == nil {
		t.Error("relative DenyWrite accepted")
	}
	in.DenyWrite = nil
	if _, err := New(in); err == nil {
		t.Error("no deny-write at all accepted")
	}
}
