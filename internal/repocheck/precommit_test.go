package repocheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type preCommitHook struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Entry    string `yaml:"entry"`
	Language string `yaml:"language"`
}

type preCommitFile struct {
	Repos []struct {
		Repo  string          `yaml:"repo"`
		Hooks []preCommitHook `yaml:"hooks"`
	} `yaml:"repos"`
}

// preCommitProblems checks that data is a well-formed configuration for the
// pre-commit framework: a top-level repos list, every hook with id, name,
// entry and language, and every `language: system` hook's entry starting with
// an executable that is found (found reports it) or is go or bash. It asserts
// nothing about which hooks exist (C-07).
func preCommitProblems(data []byte, found func(string) bool) []string {
	var f preCommitFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return []string{"not valid YAML: " + err.Error()}
	}
	if len(f.Repos) == 0 {
		return []string{"top-level `repos` list is missing or empty"}
	}
	var out []string
	for _, r := range f.Repos {
		if r.Repo == "" {
			out = append(out, "a repos entry has no `repo`")
		}
		for i, h := range r.Hooks {
			label := h.ID
			if label == "" {
				label = fmt.Sprintf("hook #%d", i+1)
			}
			for field, v := range map[string]string{"id": h.ID, "name": h.Name, "entry": h.Entry, "language": h.Language} {
				if strings.TrimSpace(v) == "" {
					out = append(out, label+": missing "+field)
				}
			}
			if h.Language != "system" || strings.TrimSpace(h.Entry) == "" {
				continue
			}
			first := strings.Fields(h.Entry)[0]
			if first != "go" && first != "bash" && !found(first) {
				out = append(out, label+": entry command "+first+" is not on PATH")
			}
		}
	}
	return out
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// TestPreCommitConfig is check:pre-commit-config (D-083).
func TestPreCommitConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range preCommitProblems(data, onPath) {
		t.Error(p)
	}
}

// The check rejects a malformed file and a system hook whose command does
// not exist, and accepts go and bash without a lookup.
func TestPreCommitConfigRejectsMalformed(t *testing.T) {
	never := func(string) bool { return false }
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"not yaml":      {"repos: [", "not valid YAML"},
		"no repos":      {"hooks: []\n", "repos"},
		"missing field": {"repos:\n- repo: local\n  hooks:\n  - id: a\n    name: a\n    language: system\n", "missing entry"},
		"unknown tool":  {"repos:\n- repo: local\n  hooks:\n  - {id: a, name: a, language: system, entry: nosuchtool --x}\n", "not on PATH"},
	} {
		got := strings.Join(preCommitProblems([]byte(tc.src), never), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: problems %q lack %q", name, got, tc.want)
		}
	}
	ok := "repos:\n- repo: local\n  hooks:\n  - {id: a, name: a, language: system, entry: go vet ./...}\n" +
		"  - {id: b, name: b, language: system, entry: \"bash -c true\"}\n  - {id: c, name: c, language: python, entry: whatever}\n"
	if p := preCommitProblems([]byte(ok), never); len(p) != 0 {
		t.Errorf("well-formed file rejected: %v", p)
	}
}
