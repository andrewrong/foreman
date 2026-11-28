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

// allowlistEnv is the real-sandbox fixture of TestSandboxReadAllowlist: a
// fake owner home, a data volume root holding foreman's home, and a policy
// built by policy.New from them. Nothing here touches the real home.
type allowlistEnv struct {
	t        *testing.T
	root     string
	owner    string // fake owner home
	data     string // deny root (data volume)
	home     string // fake FOREMAN_HOME inside data
	mirror   string
	session  string
	runDir   string
	outside  string
	harness  string
	goRoot   string
	launcher launch.Launcher
	pol      policy.Policy
}

const allowlistEmail = "session@users.noreply.example"

func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skip(name + " not set")
	}
	return v
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hostGit runs git outside the sandbox the way foremand prepares repos: no
// owner or system config, no hooks.
func hostGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always",
		"-c", "user.name=seed", "-c", "user.email=seed@users.noreply.example"}, args...)
	cmd := exec.Command(bin, argv...)
	cmd.Dir = dir
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "PATH=/usr/bin:/bin", "HOME=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, trim(string(out)))
	}
}

func newAllowlistEnv(t *testing.T) *allowlistEnv {
	t.Helper()
	srt := envOrSkip(t, "FOREMAN_TEST_SRT")
	nodeDir := envOrSkip(t, "FOREMAN_TEST_NODE_DIR")
	goRoot := envOrSkip(t, "FOREMAN_TEST_GO_ROOT")
	// srt's unix socket lives in the run dir: keep every path short, and
	// resolve /tmp (a symlink on macOS) so the test sees what srt sees.
	d, err := os.MkdirTemp("/tmp", "fma")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	root, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	goReal, err := filepath.EvalSymlinks(goRoot)
	if err != nil {
		t.Fatal(err)
	}
	e := &allowlistEnv{t: t, root: root, goRoot: goReal}
	e.owner = filepath.Join(root, "owner")
	e.data = filepath.Join(root, "data")
	e.home = filepath.Join(e.data, "h")
	e.mirror = filepath.Join(e.home, "projects/p/repo.git")
	e.session = filepath.Join(e.home, "projects/p/sessions/r1")
	e.runDir = filepath.Join(e.home, "runs/r1")
	e.outside = filepath.Join(root, "outside")
	e.harness = filepath.Join(e.data, "harness")

	// Fake owner home: the items a real home holds that must stay unreadable.
	mustWrite(t, filepath.Join(e.owner, ".claude.json"), "owner-secret")
	mustWrite(t, filepath.Join(e.owner, ".gitconfig"), "[user]\n\tname = owner\n\temail = owner@example.invalid\n")
	mustWrite(t, filepath.Join(e.owner, ".zsh_history"), "owner-secret")
	mustWrite(t, filepath.Join(e.owner, ".ssh/id_ed25519"), "owner-secret")
	mustWrite(t, filepath.Join(e.owner, "Library/Keychains/login.keychain-db"), "owner-secret")
	// A sibling of foreman's home on the deny root, foreman's own db, and
	// the harness install dir (declared by the adapter).
	mustWrite(t, filepath.Join(e.data, "sibling/secret.txt"), "sibling-secret")
	mustWrite(t, filepath.Join(e.home, "db/global.db"), "db-secret")
	mustWrite(t, filepath.Join(e.harness, "bin/tool"), "harness-binary")
	mustMkdir(t, e.outside, filepath.Join(e.home, "config"), filepath.Join(e.home, "cache/p"), e.runDir)

	// Mirror and a --shared session clone: the session's objects live in the mirror.
	work := filepath.Join(root, "work")
	mustMkdir(t, work)
	hostGit(t, work, "init", "-q", "-b", "main", ".")
	mustWrite(t, filepath.Join(work, "README"), "seed\n")
	mustWrite(t, filepath.Join(work, "go.mod"), "module example.invalid/hello\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(work, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n")
	hostGit(t, work, "add", "-A")
	hostGit(t, work, "commit", "-q", "-m", "seed")
	mustMkdir(t, filepath.Dir(e.mirror), filepath.Dir(e.session))
	hostGit(t, work, "clone", "-q", "--bare", "--", work, e.mirror)
	hostGit(t, work, "clone", "-q", "--shared", "--", e.mirror, e.session)

	// The toolchain is reached through a symlink, as with a mise "lts" link:
	// the policy must resolve it.
	lts := filepath.Join(root, "lts")
	if err := os.Symlink(goReal, lts); err != nil {
		t.Fatal(err)
	}
	var launcher launch.Launcher
	launcher, err = launch.Launcher{SrtPath: srt, NodeDir: nodeDir}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	e.launcher = launcher
	e.pol, err = policy.New(policy.Inputs{
		SessionRepo:     e.session,
		RunTmp:          e.runDir,
		DenyWrite:       []string{filepath.Join(e.home, "db"), filepath.Join(e.home, "config"), e.mirror},
		ProjectCacheDir: filepath.Join(e.home, "cache/p"),
		OwnerHome:       e.owner,
		// The Go toolchain's parent is a deny root too, so reading the
		// toolchain proves that read_paths re-allows it.
		DenyRoots:    []string{e.data, filepath.Dir(goReal)},
		MirrorRepo:   e.mirror,
		HarnessPaths: []string{e.harness},
		ReadPaths:    []string{lts},
		SessionGit:   policy.SessionGit{Name: "Session", Email: allowlistEmail, DefaultBranch: "main"},
		Families:     []policy.Family{policy.FamilyGo},
		Path:         filepath.Join(lts, "bin") + ":/usr/bin:/bin",
		GoFlags:      policy.DefaultGoFlags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type shResult struct {
	code   int
	stdout string
	stderr string
}

// sh runs a fixed /bin/sh script through the real launcher in srt; variable
// data is passed as positional arguments. No model is involved.
func (e *allowlistEnv) sh(script string, args ...string) shResult {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	argv := append([]string{"/bin/sh", "-c", script, "sh"}, args...)
	ex, err := e.launcher.Run(ctx, e.pol, argv, launch.IO{Stdout: &out, Stderr: &errb})
	if err != nil {
		e.t.Fatalf("launch: %v", err)
	}
	return shResult{code: ex.Code, stdout: strings.TrimSpace(out.String()), stderr: trim(errb.String())}
}

const (
	readFile = `cat -- "$1" >/dev/null`
	listDir  = `ls -- "$1" >/dev/null`
)

func TestSandboxReadAllowlist(t *testing.T) {
	e := newAllowlistEnv(t)
	// Positive control: the same operations work where they are allowed, so
	// a refusal in the checks below is the allowlist and not a broken launch.
	if r := e.sh(readFile, filepath.Join(e.session, "README")); r.code != 0 {
		t.Fatalf("session repo not readable: %+v", r)
	}
	if r := e.sh(readFile, filepath.Join(e.harness, "bin/tool")); r.code != 0 {
		t.Errorf("harness path declared by the adapter not readable: %+v", r)
	}
	if r := e.sh(listDir, filepath.Join(e.mirror, "objects")); r.code != 0 {
		t.Errorf("mirror objects/ (alternates target) not readable: %+v", r)
	}
	t.Run("denied reads", e.deniedReads)
	t.Run("denied writes", e.deniedWrites)
	t.Run("allowed read and write", e.allowedReadWrite)
	t.Run("git log and commit with the mirror read-only", e.gitLogAndCommit)
	t.Run("session git config is the generated one", e.generatedGitconfig)
	t.Run("go build with the toolchain from read_paths", e.goBuild)
}

func (e *allowlistEnv) deniedReads(t *testing.T) {
	files := map[string]string{
		"owner .claude.json":  filepath.Join(e.owner, ".claude.json"),
		"owner .gitconfig":    filepath.Join(e.owner, ".gitconfig"),
		"owner .zsh_history":  filepath.Join(e.owner, ".zsh_history"),
		"owner .ssh key":      filepath.Join(e.owner, ".ssh/id_ed25519"),
		"owner Library":       filepath.Join(e.owner, "Library/Keychains/login.keychain-db"),
		"sibling on the root": filepath.Join(e.data, "sibling/secret.txt"),
		"foreman db":          filepath.Join(e.home, "db/global.db"),
		"mirror config":       filepath.Join(e.mirror, "config"),
		"mirror HEAD":         filepath.Join(e.mirror, "HEAD"),
	}
	for name, p := range files {
		if r := e.sh(readFile, p); r.code == 0 {
			t.Errorf("%s: readable, want denied", name)
		}
	}
	dirs := map[string]string{"owner home": e.owner, ".ssh": filepath.Join(e.owner, ".ssh"),
		"Library": filepath.Join(e.owner, "Library"), "data root": e.data, "config dir": filepath.Join(e.home, "config"),
		"mirror refs": filepath.Join(e.mirror, "refs"), "mirror root": e.mirror}
	for name, p := range dirs {
		if r := e.sh(listDir, p); r.code == 0 {
			t.Errorf("%s: listable, want denied", name)
		}
	}
}

func (e *allowlistEnv) deniedWrites(t *testing.T) {
	targets := map[string]string{
		"outside any session dir": filepath.Join(e.outside, "x"),
		"owner home":              filepath.Join(e.owner, "x"),
		"foreman db":              filepath.Join(e.home, "db/x"),
		"mirror (read-only)":      filepath.Join(e.mirror, "x"),
		"project cache master":    filepath.Join(e.home, "cache/p/x"),
		"data root":               filepath.Join(e.data, "x"),
	}
	for name, p := range targets {
		if r := e.sh(`echo x > "$1"`, p); r.code == 0 {
			t.Errorf("%s: write succeeded, want denied", name)
		}
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s: %s exists after the sandboxed write", name, p)
		}
	}
}

func (e *allowlistEnv) allowedReadWrite(t *testing.T) {
	if r := e.sh(`echo mine > "$1/new.txt" && cat "$1/new.txt"`, e.session); r.code != 0 || r.stdout != "mine" {
		t.Errorf("session repo write+read: %+v", r)
	}
	if r := e.sh(`echo mine > "$TMPDIR/new.txt" && cat "$TMPDIR/new.txt"`); r.code != 0 || r.stdout != "mine" {
		t.Errorf("run dir write+read: %+v", r)
	}
}

func (e *allowlistEnv) gitLogAndCommit(t *testing.T) {
	if r := e.sh(`cd "$1" && git log --oneline`, e.session); r.code != 0 || !strings.Contains(r.stdout, "seed") {
		t.Fatalf("git log in a --shared clone of the mirror: %+v", r)
	}
	r := e.sh(`cd "$1" && echo change >> README && git add README && git commit -q -m session-change && git log -1 --format=%ae`, e.session)
	if r.code != 0 || r.stdout != allowlistEmail {
		t.Errorf("git commit with the generated identity: %+v", r)
	}
	// The mirror stays read-only: a session cannot write refs into it.
	if r := e.sh(`cd "$1" && git push "$2" HEAD:refs/heads/x`, e.session, e.mirror); r.code == 0 {
		t.Error("a session can write refs into the mirror")
	}
}

func (e *allowlistEnv) generatedGitconfig(t *testing.T) {
	if r := e.sh(`cd "$1" && git config user.email`, e.session); r.code != 0 || r.stdout != allowlistEmail {
		t.Errorf("git config user.email = %+v, want %s", r, allowlistEmail)
	}
	r := e.sh(`cd "$1" && git config --show-origin user.name`, e.session)
	if want := "file:" + e.pol.GitConfigPath(); r.code != 0 || !strings.HasPrefix(r.stdout, want) {
		t.Errorf("user.name origin = %+v, want %s", r, want)
	}
	if r := e.sh(`cd "$1" && git config --list --show-origin | grep -c owner@example.invalid`, e.session); r.stdout != "0" {
		t.Errorf("owner gitconfig leaked into the session: %+v", r)
	}
}

func (e *allowlistEnv) goBuild(t *testing.T) {
	r := e.sh(`cd "$1" && go build -o "$TMPDIR/hello" . && "$TMPDIR/hello"`, e.session)
	if r.code != 0 || r.stdout != "hello" {
		t.Errorf("go build in the session: %+v", r)
	}
}
