package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

// warmSeedEnv names the directory TestMain warmed once for the whole test
// process: a project cache master built by the real launch.WarmCache from the
// fixture module. Every fixture starts from a clone of it, so the many
// scenario tests do not each compile the standard library (that was ~95% of
// their time) while L0 still clones and uses a warm cache through the
// production path (launch.prepareCaches).
const warmSeedEnv = "FOREMAN_TEST_WARM_SEED"

// warmSeed builds the shared seed under /tmp (short paths) and returns its
// directory, or "" when go is not on PATH or the warm-up fails (fixtures then
// start with an empty cache and Compose warms it itself).
func warmSeed() string {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	root, err := os.MkdirTemp("/tmp", "fmw")
	if err != nil {
		return ""
	}
	src := filepath.Join(root, "src")
	files := map[string]string{"go.mod": goMod, "sum.go": sumGo, "sum_test.go": sumTst}
	if err := os.MkdirAll(src, 0o700); err != nil {
		return ""
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(src, n), []byte(c), 0o600); err != nil {
			return ""
		}
	}
	master := filepath.Join(root, "master")
	if err := os.Mkdir(master, 0o700); err != nil {
		return ""
	}
	_, err = launch.WarmCache(context.Background(), launch.Warm{Master: master, Src: src, Commit: "seed",
		Path: filepath.Dir(goBin) + ":" + wsgit.SystemPath()})
	if err != nil {
		os.RemoveAll(root)
		return ""
	}
	return root
}

// seedWarmCache gives home's project cache the shared warm seed, recorded as
// warmed from sha (the fixture's default-branch head).
func seedWarmCache(t *testing.T, home, project, sha string) {
	t.Helper()
	root := os.Getenv(warmSeedEnv)
	if root == "" {
		return
	}
	master := filepath.Join(home, "cache", project)
	if err := os.MkdirAll(master, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(master, "warm")); err == nil {
		return // seeded by an earlier config of the same home
	}
	if err := launch.CloneTree(filepath.Join(root, "master", "warm"), filepath.Join(master, "warm")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(master, "warm", "commit"), []byte(sha), 0o600); err != nil {
		t.Fatal(err)
	}
}
