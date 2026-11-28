package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGolangciLint is check:golangci-lint (D-078): the pinned golangci-lint
// (go.mod tool directive) with the repository's .golangci.yml, which enables
// only gochecknoglobals (MD-13) and gocognit (MD-14).
func TestGolangciLint(t *testing.T) {
	cmd := exec.Command("go", "tool", "golangci-lint", "run", "./...")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("golangci-lint found problems (MD-13, MD-14): %v\n%s", err, out)
	}
}

// The repository's configuration rejects a mutable package variable and a
// function whose cognitive complexity exceeds 30, and accepts the allowed
// forms (an error value, a compiled regexp, a function returning the table).
func TestGolangciLintDetectsViolations(t *testing.T) {
	root := repoRoot(t)
	which, err := exec.Command("go", "tool", "-n", "golangci-lint").Output()
	if err != nil {
		t.Fatalf("go tool -n golangci-lint: %v", err)
	}
	bin := strings.TrimSpace(string(which))
	run := func(src string) (string, error) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"go.mod": "module example.com/lintfix\n\ngo 1.27\n", "p.go": src})
		cmd := exec.Command(bin, "run", "--config", filepath.Join(root, ".golangci.yml"), "./...")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	var deep strings.Builder
	deep.WriteString("package p\n\nfunc deep(n int) int {\n\tt := 0\n")
	for i := 0; i < 8; i++ {
		deep.WriteString("\tif n > 0 {\n\t\tfor j := 0; j < n; j++ {\n\t\t\tif j%2 == 0 {\n\t\t\t\tt++\n\t\t\t}\n\t\t}\n\t}\n")
	}
	deep.WriteString("\treturn t\n}\n")
	cases := []struct {
		name, src, want string // want empty: the file must pass
	}{
		{"mutable global", "package p\n\nvar table = map[string]bool{\"a\": true}\n\nfunc use() bool { return table[\"a\"] }\n", "gochecknoglobals"},
		{"complex function", deep.String(), "gocognit"},
		{"allowed forms", "package p\n\nimport (\n\t\"errors\"\n\t\"regexp\"\n)\n\nvar ErrX = errors.New(\"x\")\n\nvar re = regexp.MustCompile(`a+`)\n\nfunc table() map[string]bool { return map[string]bool{\"a\": true} }\n\nfunc use() (bool, error) { return re.MatchString(\"a\") && table()[\"a\"], ErrX }\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := run(c.src)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("clean source rejected: %v\n%s", err, out)
			case c.want != "" && (err == nil || !strings.Contains(out, c.want)):
				t.Errorf("want a %s finding, got err=%v\n%s", c.want, err, out)
			}
		})
	}
}
