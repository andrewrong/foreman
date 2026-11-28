package launch

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

const (
	fxMod  = "module example.test/sum\n\ngo 1.21\n"
	fxSum  = "package sum\n\nfunc Sum(a, b int) int { return a + b }\n"
	fxTest = "package sum\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif Sum(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"
)

type cacheEnv struct {
	root, master, repo, run string
	goBin                   string
	path                    string
}

func newCacheEnv(t *testing.T) *cacheEnv {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root, err := os.MkdirTemp("/tmp", "fmc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAllWritable(root) })
	e := &cacheEnv{root: root, master: filepath.Join(root, "cache"), repo: filepath.Join(root, "repo"),
		run: filepath.Join(root, "run"), goBin: goBin, path: filepath.Dir(goBin) + ":/usr/bin:/bin"}
	for _, d := range []string{e.master, e.repo, e.run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, e.repo, map[string]string{"go.mod": fxMod, "sum.go": fxSum, "sum_test.go": fxTest})
	return e
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *cacheEnv) policy(t *testing.T) policy.Policy {
	t.Helper()
	p, err := policy.New(policy.Inputs{
		SessionRepo: e.repo, RunTmp: e.run, ProjectCacheDir: e.master,
		DenyWrite: []string{e.master}, Path: e.path, GoFlags: policy.DefaultGoFlags,
		SessionGit: policy.SessionGit{Name: "n", Email: "n@users.noreply.example", DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *cacheEnv) warm(t *testing.T, src, commit string, environ func() []string) (WarmResult, error) {
	t.Helper()
	return WarmCache(context.Background(), Warm{Master: e.master, Src: src, Commit: commit, Path: e.path, Environ: environ})
}

func files(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out[rel] = true
		}
		return nil
	})
	return out
}

// TestSessionGoCacheCloned: after the project cache is warmed, an L0 style
// `go test` started through the launcher runs on a clone of it. The clone
// holds the standard library and the fixture's packages already, so the run
// writes only a handful of new cache files (a cold cache writes ~1000), and
// the master is not touched by the run.
func TestSessionGoCacheCloned(t *testing.T) {
	e := newCacheEnv(t)
	if res, err := e.warm(t, e.repo, "c1", nil); err != nil || !res.Warmed {
		t.Fatalf("warm = %+v, %v", res, err)
	}
	masterCache := filepath.Join(e.master, "warm", "gocache")
	before := files(t, masterCache)
	if len(before) < 100 {
		t.Fatalf("warm cache holds %d files, want the standard library compiled", len(before))
	}
	var out bytes.Buffer
	ex, err := Launcher{PassThrough: true}.Run(context.Background(), e.policy(t), []string{e.goBin, "test", "-count=1", "./..."}, IO{Stdout: &out, Stderr: &out})
	if err != nil || ex.Code != 0 {
		t.Fatalf("go test = %+v, %v\n%s", ex, err, out.String())
	}
	cloned := files(t, filepath.Join(e.run, "gocache"))
	fresh := 0
	for f := range cloned {
		if !before[f] {
			fresh++
		}
	}
	if len(cloned) < len(before) || fresh > 12 {
		t.Errorf("run cache has %d files (%d new), master %d: not a warm clone", len(cloned), fresh, len(before))
	}
	if after := files(t, masterCache); len(after) != len(before) {
		t.Errorf("master changed during the run: %d -> %d files", len(before), len(after))
	}
}

// TestSessionGoCacheEmptyWithoutWarm: no warm cache yet (or no master at all)
// means empty cache directories and a run that still succeeds.
func TestSessionGoCacheEmptyWithoutWarm(t *testing.T) {
	e := newCacheEnv(t)
	var out bytes.Buffer
	ex, err := Launcher{PassThrough: true}.Run(context.Background(), e.policy(t), []string{e.goBin, "test", "-count=1", "./..."}, IO{Stdout: &out, Stderr: &out})
	if err != nil || ex.Code != 0 {
		t.Fatalf("go test = %+v, %v\n%s", ex, err, out.String())
	}
	if fi, err := os.Stat(filepath.Join(e.run, "gomodcache")); err != nil || !fi.IsDir() {
		t.Errorf("gomodcache missing: %v", err)
	}
}

// TestWarmCacheRunsNoProjectCode: the warm-up compiles the fixture (library
// init, TestMain, test init, a go:generate line) but starts none of it.
func TestWarmCacheRunsNoProjectCode(t *testing.T) {
	e := newCacheEnv(t)
	marker := filepath.Join(e.root, "marker")
	write := "os.WriteFile(" + `"` + marker + `"` + ", []byte(\"ran\"), 0o600)"
	writeFiles(t, e.repo, map[string]string{
		"sum.go": "package sum\n\nimport \"os\"\n\n//go:generate sh -c \"echo ran > " + marker + "\"\n\nfunc init() { " + write + " }\n\nfunc Sum(a, b int) int { return a + b }\n",
		"sum_test.go": "package sum\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc init() { " + write + " }\n\n" +
			"func TestMain(m *testing.M) { " + write + "; os.Exit(m.Run()) }\n\nfunc TestSum(t *testing.T) {}\n",
	})
	res, err := e.warm(t, e.repo, "c1", nil)
	if err != nil || !res.Warmed {
		t.Fatalf("warm = %+v, %v\n%s", res, err, res.Log)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the warm-up ran project code")
	}
	// Control: the same fixture does create the marker when it is run.
	cmd := exec.Command(e.goBin, "test", "-count=1", "./...")
	cmd.Dir = e.repo
	cmd.Env = []string{"PATH=" + e.path, "HOME=" + e.root, "GOCACHE=" + filepath.Join(e.root, "ctl")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("control run: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the fixture does not create its marker when run, so the check above proves nothing")
	}
}

// TestWarmCacheFailureKeepsLiveCache: a failed warm-up (a module that cannot
// be downloaded) leaves the live cache and its recorded commit as they were
// and no staging directory behind; an unchanged head is a no-op.
func TestWarmCacheFailureKeepsLiveCache(t *testing.T) {
	e := newCacheEnv(t)
	if _, err := e.warm(t, e.repo, "c1", nil); err != nil {
		t.Fatal(err)
	}
	if res, err := e.warm(t, e.repo, "c1", nil); err != nil || res.Warmed {
		t.Fatalf("same commit: warmed=%v err=%v, want a no-op", res.Warmed, err)
	}
	writeFiles(t, e.repo, map[string]string{"go.mod": fxMod + "\nrequire example.invalid/missing v1.0.0\n"})
	offline := func() []string { return []string{"GOPROXY=off"} }
	if _, err := e.warm(t, e.repo, "c2", offline); err == nil {
		t.Fatal("warm of an undownloadable module succeeded")
	}
	if got := WarmedCommit(e.master); got != "c1" {
		t.Errorf("recorded commit = %q, want c1", got)
	}
	entries, _ := os.ReadDir(e.master)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".staging-") || strings.HasPrefix(en.Name(), ".old-") {
			t.Errorf("residue %s", en.Name())
		}
	}
}

// TestWarmCacheIsIncremental: a second warm-up starts from the live cache.
func TestWarmCacheIsIncremental(t *testing.T) {
	e := newCacheEnv(t)
	if _, err := e.warm(t, e.repo, "c1", nil); err != nil {
		t.Fatal(err)
	}
	first := files(t, filepath.Join(e.master, "warm", "gocache"))
	writeFiles(t, e.repo, map[string]string{"sum.go": fxSum + "\nfunc Twice(a int) int { return 2 * a }\n"})
	if _, err := e.warm(t, e.repo, "c2", nil); err != nil {
		t.Fatal(err)
	}
	second := files(t, filepath.Join(e.master, "warm", "gocache"))
	for f := range first {
		if !second[f] {
			t.Fatalf("%s of the first warm-up is missing after the second", f)
		}
	}
	if WarmedCommit(e.master) != "c2" {
		t.Errorf("commit = %q", WarmedCommit(e.master))
	}
}

// TestCloneTreeUsesClonefileOnMacOS: on macOS the clone must be a real APFS
// clone (clonefileat), not the plain-copy fallback, and modes are kept.
func TestCloneTreeUsesClonefileOnMacOS(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "d", "f"), []byte("x"), 0o440); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(filepath.Dir(src), "dst")
	if runtime.GOOS == "darwin" {
		if err := cloneDir(src, dst); err != nil {
			t.Fatalf("clonefile: %v", err)
		}
		if err := os.RemoveAll(dst); err != nil {
			t.Fatal(err)
		}
	}
	if err := CloneTree(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "d", "f"))
	if err != nil || fi.Mode().Perm() != 0o440 {
		t.Errorf("clone: %v %v", fi, err)
	}
	if err := CloneTree(src, dst); err == nil {
		t.Error("clone onto an existing path succeeded")
	}
}
