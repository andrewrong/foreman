package lint

import (
	"os"
	"path/filepath"
	"testing"
)

const realSpec = "../../../spec"

// TestSpecLint is check:spec-lint: the real spec has no findings.
func TestSpecLint(t *testing.T) {
	for _, f := range Lint(realSpec) {
		t.Errorf("%s", f)
	}
}

// TestSpecPhaseTags is check:spec-phase-tags: F10 holds on the real spec.
func TestSpecPhaseTags(t *testing.T) {
	for _, f := range Lint(realSpec) {
		if f.Rule == "F10" {
			t.Errorf("%s", f)
		}
	}
}

// writeSpec writes name->content files under a fresh dir and returns it.
func writeSpec(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hasFinding(fs []Finding, rule, id string) bool {
	for _, f := range fs {
		if f.Rule == rule && (id == "" || f.ID == id) {
			return true
		}
	}
	return false
}

const cleanDecisions = `schema: x
decisions:
  - {id: D-001, enforced_by: "test:a-b；check:c-d", status: active}
  - {id: D-002, enforced_by: none, status: active}
`

func TestLintCleanFixture(t *testing.T) {
	dir := writeSpec(t, map[string]string{
		"decisions.yaml":    cleanDecisions,
		"constitution.yaml": "articles:\n  - {id: C-01, enforced_by: test:x}\n",
	})
	if fs := Lint(dir); len(fs) != 0 {
		t.Fatalf("unexpected findings: %v", fs)
	}
}

func TestLintNegative(t *testing.T) {
	dec := func(body string) map[string]string {
		return map[string]string{"decisions.yaml": "decisions:\n" + body}
	}
	cases := []struct {
		name  string
		files map[string]string
		rule  string
		id    string
	}{
		{"F1 duplicate id", dec("  - {id: D-001, enforced_by: none, status: active}\n  - {id: D-001, enforced_by: none, status: active}\n"), "F1", "D-001"},
		{"F1 dangling ref", dec("  - {id: D-001, statement: see D-099, enforced_by: none, status: active}\n"), "F1", "D-099"},
		{"F1 unknown prefix", dec("  - {id: D-001, statement: see ZZ-3, enforced_by: none, status: active}\n"), "F1", "ZZ-3"},
		{"F1 external prefix ok-control", dec("  - {id: D-001, statement: see ACR-3, enforced_by: none, status: active}\n"), "", ""},
		{"F3 missing", dec("  - {id: D-001, status: active}\n"), "F3", "D-001"},
		{"F3 bad form", dec("  - {id: D-001, enforced_by: manual, status: active}\n"), "F3", "D-001"},
		{"F3 constitution missing", map[string]string{"constitution.yaml": "articles:\n  - {id: C-01}\n"}, "F3", "C-01"},
		{"F4 program only", dec("  - {id: D-001, enforced_by: \"program:store\", status: active}\n"), "F4", "D-001"},
		{"F4 constitution none limit", map[string]string{"constitution.yaml": "articles:\n  - {id: C-01, enforced_by: none}\n  - {id: C-02, enforced_by: none}\n  - {id: C-03, enforced_by: program:x}\n  - {id: C-04, enforced_by: none}\n"}, "F4", "constitution"},
		{"F5 banned word", dec("  - {id: D-001, statement: 适当处理, enforced_by: none, status: active}\n"), "F5", "D-001"},
		{"F7 unknown link field", dec("  - {id: D-001, enforced_by: none, status: active, replaces: D-002}\n"), "F7", "D-001"},
		{"F7 missing target", dec("  - {id: D-001, enforced_by: none, status: active, amends: D-009}\n"), "F7", "D-001"},
		{"F7 superseded untargeted", dec("  - {id: D-001, enforced_by: none, status: superseded}\n"), "F7", "D-001"},
		{"F7 target not marked superseded", dec("  - {id: D-001, enforced_by: none, status: active}\n  - {id: D-002, enforced_by: none, status: active, supersedes: D-001}\n"), "F7", "D-002"},
		{"F7 resolves missing question", dec("  - {id: D-001, enforced_by: none, status: active, resolves: Q-1}\n"), "F7", "D-001"},
		{"F8 double amends", dec("  - {id: D-001, enforced_by: none, status: active}\n  - {id: D-002, enforced_by: none, status: active, amends: D-001}\n  - {id: D-003, enforced_by: none, status: active, amends: D-001}\n"), "F8", "D-001"},
		{"F8 amends an amender", dec("  - {id: D-001, enforced_by: none, status: active}\n  - {id: D-002, enforced_by: none, status: active, amends: D-001}\n  - {id: D-003, enforced_by: none, status: active, amends: D-002}\n"), "F8", "D-003"},
		{"F10 bad phase", map[string]string{"design/x.yaml": "items:\n  - {id: X-1, phase: v3}\n"}, "F10", "X-1"},
		{"F10 v2 test in v1 list", map[string]string{
			"design/x.yaml": "items:\n  - {id: E-ONE, phase: v2}\n",
			"tests.yaml":    "tests:\n  - {name: test:a, covers: [\"x:E-ONE\"]}\n",
		}, "F10", "test:a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := Lint(writeSpec(t, c.files))
			if c.rule == "" {
				if len(fs) != 0 {
					t.Fatalf("expected no findings, got %v", fs)
				}
				return
			}
			if !hasFinding(fs, c.rule, c.id) {
				t.Fatalf("want %s on %s, got %v", c.rule, c.id, fs)
			}
		})
	}
}

func TestLintExemptions(t *testing.T) {
	files := map[string]string{
		"decisions.yaml": "decisions:\n" +
			"  - {id: D-001, statement: \"用『适当』一词\", enforced_by: none, status: active}\n" +
			"  - {id: D-002, enforced_by: \"program:store\", status: active}\n" +
			"  - {id: D-003, enforced_by: none, status: active, coverage: {D-002: \"test:s-t\"}}\n" +
			"  - {id: D-004, enforced_by: none, status: superseded}\n" +
			"  - {id: D-005, enforced_by: none, status: active, supersedes: D-004}\n" +
			"  - {id: D-006, enforced_by: none, status: active, amends: D-004}\n",
		"design/y.yaml": "phase: v2\nitems:\n  - {id: E-ONE}\n",
		"tests.yaml":    "tests:\n  - {name: test:a, covers: [\"y:E-ONE\", D-001]}\n",
	}
	if fs := Lint(writeSpec(t, files)); len(fs) != 0 {
		t.Fatalf("unexpected findings: %v", fs)
	}
}
