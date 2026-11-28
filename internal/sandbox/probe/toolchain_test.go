package probe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// TestSessionToolchainFromMise (D-085): a session repo whose mise.toml
// declares the machine's Go version gets that toolchain, and only that one,
// inside srt: go version and go build work through the real policy and
// launcher, and gh (owner-global tool, not in the project's mise.toml) is not
// on the session PATH. Runs only with FOREMAN_TEST_SRT, FOREMAN_TEST_NODE_DIR
// and mise on PATH.
func TestSessionToolchainFromMise(t *testing.T) {
	srt := envOrSkip(t, "FOREMAN_TEST_SRT")
	nodeDir := envOrSkip(t, "FOREMAN_TEST_NODE_DIR")
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not on PATH")
	}
	out, err := exec.Command("mise", "current", "go").Output()
	goVersion := strings.TrimSpace(string(out))
	if err != nil || goVersion == "" {
		t.Skip("mise has no current go")
	}
	d, err := os.MkdirTemp("/tmp", "fmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	root, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "h")
	repo := filepath.Join(home, "projects/p/sessions/r1")
	run := filepath.Join(home, "runs/r1")
	mustMkdir(t, filepath.Join(home, "db"), filepath.Join(home, "githome"), filepath.Join(root, "owner"), run)
	mustWrite(t, filepath.Join(repo, "mise.toml"), "[tools]\ngo = \""+goVersion+"\"\n")
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/hello\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(repo, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tc, err := launch.Mise{EmptyConfig: filepath.Join(home, "githome/mise/config.toml")}.Toolchain(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Path) == 0 {
		t.Fatal("mise returned no tool directories for a repo declaring go")
	}
	for _, p := range tc.Path {
		if filepath.Base(p) == "shims" {
			t.Errorf("shims directory %s on the toolchain PATH", p)
		}
		if _, err := os.Stat(filepath.Join(p, "gh")); err == nil {
			t.Errorf("gh found in toolchain directory %s", p)
		}
	}
	in := policy.Inputs{
		SessionRepo: repo, RunTmp: run,
		DenyWrite: []string{filepath.Join(home, "db")},
		OwnerHome: filepath.Join(root, "owner"),
		// The tool's parent is a deny root, so reading the toolchain proves
		// that the install directory reached allowRead.
		DenyRoots:  []string{filepath.Dir(tc.Read[0])},
		SessionGit: policy.SessionGit{Name: "Session", Email: allowlistEmail, DefaultBranch: "main"},
		Families:   []policy.Family{policy.FamilyGo},
		Path:       "/usr/bin:/bin",
	}.WithToolchain(tc.Path, tc.Read)
	pol, err := policy.New(in)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := launch.Launcher{SrtPath: srt, NodeDir: nodeDir}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	sh := func(script string) shResult {
		var o, e bytes.Buffer
		ex, err := launcher.Run(ctx, pol, []string{"/bin/sh", "-c", script}, launch.IO{Stdout: &o, Stderr: &e})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		return shResult{code: ex.Code, stdout: strings.TrimSpace(o.String()), stderr: trim(e.String())}
	}
	if r := sh(`go version`); r.code != 0 || !strings.Contains(r.stdout, "go"+goVersion) {
		t.Errorf("go version inside srt = %+v, want go%s", r, goVersion)
	}
	if r := sh(`go build -o "$TMPDIR/hello" . && "$TMPDIR/hello"`); r.code != 0 || r.stdout != "hello" {
		t.Errorf("go build inside srt = %+v", r)
	}
	if r := sh(`command -v gh`); r.code == 0 {
		t.Errorf("gh is on the session PATH: %+v", r)
	}
}
