package repocheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	agentsMaxBytes     = 6000 // C-11
	agentsMaxLines     = 80
	agentsMaxLineBytes = 200
)

// agentsViolations implements C-11 on root/AGENTS.md.
func agentsViolations(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return nil, err
	}
	var out []string
	if len(data) > agentsMaxBytes {
		out = append(out, fmt.Sprintf("AGENTS.md: %d bytes > %d", len(data), agentsMaxBytes))
	}
	if n := countLines(data); n > agentsMaxLines {
		out = append(out, fmt.Sprintf("AGENTS.md: %d lines > %d", n, agentsMaxLines))
	}
	for i, line := range strings.Split(string(data), "\n") {
		if len(line) > agentsMaxLineBytes {
			out = append(out, fmt.Sprintf("AGENTS.md:%d: %d bytes > %d", i+1, len(line), agentsMaxLineBytes))
		}
	}
	return out, nil
}

func TestAgentsMdSize(t *testing.T) {
	v, err := agentsViolations(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		t.Error(s)
	}
}

func TestAgentsMdSizeDetectsViolations(t *testing.T) {
	cases := map[string]struct {
		content string
		want    string
	}{
		"bytes": {strings.Repeat(strings.Repeat("x", 99)+"\n", 70), "bytes > 6000"},
		"lines": {strings.Repeat("x\n", 81), "lines > 80"},
		"line":  {strings.Repeat("x", 201) + "\n", ":1: 201 bytes"},
		"ok":    {strings.Repeat("x\n", 80), ""},
	}
	for name, c := range cases {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"AGENTS.md": c.content})
		v, err := agentsViolations(root)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(v, "\n")
		if c.want == "" && len(v) != 0 || c.want != "" && !strings.Contains(joined, c.want) {
			t.Errorf("%s: violations %q, want %q", name, joined, c.want)
		}
	}
}
