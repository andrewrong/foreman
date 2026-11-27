package privguard

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// repoConfig has no owner-specific value. FOREMAN_PRIVGUARD_EXTRA (comma
// separated literals) lets the owner run the strict version locally.
func repoConfig(t *testing.T, root string) Config {
	t.Helper()
	cfg := Config{}
	data, err := os.ReadFile(filepath.Join(root, ".privguard", "allow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowReasons, err = ParseAllowReasons(data); err != nil {
		t.Fatal(err)
	}
	for _, lit := range strings.Split(os.Getenv("FOREMAN_PRIVGUARD_EXTRA"), ",") {
		if lit = strings.TrimSpace(lit); lit != "" {
			cfg.Literals = append(cfg.Literals, lit)
		}
	}
	return cfg
}

func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	cmd.Env = ScrubbedGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files
}

// TestPrivguardTrackedFiles is check:privguard: every tracked file passes the
// P1-P3 rules, without directory or path exemptions (C-09).
func TestPrivguardTrackedFiles(t *testing.T) {
	root := repoRoot(t)
	sc, err := New(repoConfig(t, root))
	if err != nil {
		t.Fatal(err)
	}
	files := trackedFiles(t, root)
	if len(files) == 0 {
		t.Fatal("git ls-files returned nothing")
	}
	for _, name := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			continue // deleted in the work tree, or a symlink
		}
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range sc.Scan(name, content) {
			t.Errorf("%s", f)
		}
	}
}
