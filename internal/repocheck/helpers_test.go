package repocheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const modulesPath = "spec/design/modules.yaml"

type moduleDef struct {
	Module   string   `yaml:"module"`
	Layer    int      `yaml:"layer"`
	Uses     []string `yaml:"uses"`
	Packages []string `yaml:"packages"`
}

type limits struct {
	NonTest int `yaml:"non_test_lines"`
	Test    int `yaml:"test_lines"`
	File    int `yaml:"file_lines"`
}

type modulesFile struct {
	Modules       []moduleDef `yaml:"modules"`
	PackageLimits limits      `yaml:"package_limits"`
}

// repoRoot walks up from the working directory to the directory with go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadModules(root string) (*modulesFile, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(modulesPath)))
	if err != nil {
		return nil, err
	}
	var m modulesFile
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", modulesPath, err)
	}
	return &m, nil
}

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "module" {
			return f[1], nil
		}
	}
	return "", fmt.Errorf("go.mod: no module line")
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata"
}

// goFiles returns slash-separated relative paths of all .go files under root.
func goFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func countLines(data []byte) int {
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func isGenerated(data []byte) bool {
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.Contains(first, "Code generated") && strings.Contains(first, "DO NOT EDIT")
}
