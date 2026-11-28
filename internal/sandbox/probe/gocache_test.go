package probe

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

// TestSessionGoCacheClonedInSrt: with the project cache warmed outside the
// sandbox, an L0 style `go test` inside srt runs on the run's clone of it
// (few new cache files, not a cold ~1000), and the sandboxed process cannot
// write the master. Runs only with FOREMAN_TEST_SRT, FOREMAN_TEST_NODE_DIR and
// FOREMAN_TEST_GO_ROOT.
func TestSessionGoCacheClonedInSrt(t *testing.T) {
	e := newAllowlistEnv(t)
	files := map[string]string{
		"go.mod":      "module example.test/sum\n\ngo 1.21\n",
		"sum.go":      "package sum\n\nfunc Sum(a, b int) int { return a + b }\n",
		"sum_test.go": "package sum\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif Sum(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	}
	src := filepath.Join(e.root, "warmsrc")
	for n, c := range files {
		mustWrite(t, filepath.Join(src, n), c)
		mustWrite(t, filepath.Join(e.session, "sub", n), c) // own module below the session repo (it has a main.go)
	}
	master := filepath.Join(e.home, "cache/p")
	res, err := launch.WarmCache(context.Background(), launch.Warm{Master: master, Src: src, Commit: "c1",
		Path: filepath.Join(e.goRoot, "bin") + ":/usr/bin:/bin"}) // the real GOROOT, as D-085 resolves it
	if err != nil || !res.Warmed {
		t.Fatalf("warm = %+v, %v\n%s", res, err, res.Log)
	}
	count := func(dir string) (n int) {
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	warm := count(filepath.Join(master, "warm", "gocache"))
	if r := e.sh(`cd sub && go test -count=1 ./...`); r.code != 0 || !strings.Contains(r.stdout, "ok") {
		t.Fatalf("go test in srt = %+v", r)
	}
	if got := count(filepath.Join(e.runDir, "gocache")); got < warm || got > warm+12 {
		t.Errorf("run cache has %d files, warm master %d: not a warm clone", got, warm)
	}
	if got := count(filepath.Join(master, "warm", "gocache")); got != warm {
		t.Errorf("master changed during the run: %d -> %d", warm, got)
	}
	if r := e.sh(`touch "$1"`, filepath.Join(master, "warm", "x")); r.code == 0 {
		t.Error("the sandboxed process could write the project cache master")
	}
	if r := e.sh(`touch "$1"`, filepath.Join(e.runDir, "gocache", "x")); r.code != 0 {
		t.Errorf("the run's own cache copy is not writable: %+v", r)
	}
}
