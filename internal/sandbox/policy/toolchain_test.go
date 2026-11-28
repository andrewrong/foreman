package policy

import (
	"slices"
	"strings"
	"testing"
)

// D-085: the toolchain directories lead PATH and join allowRead; the inputs
// they were applied to are not modified.
func TestPolicyWithToolchain(t *testing.T) {
	fx := newFixture(t)
	tool := fx.p("tc/go")
	orig := fx.in
	p := mustNew(t, orig.WithToolchain([]string{tool}, []string{tool}))
	// Only the lead is fixed: the rest is realpath'd (/bin is /usr/bin on some Linux systems).
	if !slices.ContainsFunc(p.Env(), func(kv string) bool { return strings.HasPrefix(kv, "PATH="+tool+":") }) {
		t.Errorf("PATH not led by the toolchain: %v", p.Env())
	}
	if !slices.Contains(p.AllowReadPaths(), tool) {
		t.Errorf("allowRead lacks the toolchain: %v", p.AllowReadPaths())
	}
	if orig.Path != "/usr/bin:/bin" || len(orig.ReadPaths) != 1 {
		t.Errorf("WithToolchain modified its receiver: %+v", orig)
	}
}

// The mirror's config and refs are not readable: only objects/ is allowRead.
func TestPolicyMirrorReadIsObjectsOnly(t *testing.T) {
	fx := newFixture(t)
	for _, p := range mustNew(t, fx.in).AllowReadPaths() {
		if p == fx.p("m/repo.git") {
			t.Errorf("the whole mirror is allowRead: %s", p)
		}
	}
}
