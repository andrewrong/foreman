package model

import (
	"slices"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", ".github", false},
		{".github/**", "src/.github/x", false},
		{"internal/workspace/**", "internal/workspace/git/runner.go", true},
		{"internal/workspace/**", "internal/workspaces/x.go", false},
		{"spec/guardrails.yaml", "spec/guardrails.yaml", true},
		{"prompts/reviewer*.md", "prompts/reviewer-v2.md", true},
		{"prompts/reviewer*.md", "prompts/coder.md", false},
		{".privguard*", ".privguard.yaml", true},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/b/b/c", true},
		{"a/**/c", "a/b/d", false},
		{"a/?.go", "a/x.go", true},
		{"a/?.go", "a/xy.go", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestHitsReportsFirstFilePerRule(t *testing.T) {
	rules := []Guardrail{{"G-05", ".github/**"}, {"G-09", "internal/workspace/**"}}
	got := Hits(rules, []string{"README.md", "internal/workspace/a.go", "internal/workspace/b.go"})
	if !slices.Equal(got, []string{"G-09 internal/workspace/a.go"}) {
		t.Errorf("Hits = %v", got)
	}
	if got := Hits(rules, []string{"README.md"}); len(got) != 0 {
		t.Errorf("Hits on a clean diff = %v", got)
	}
}
