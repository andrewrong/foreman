package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// Layout of the project cache master FOREMAN_HOME/cache/<p>/ (access.yaml R0):
//
//	warm/gocache, warm/gomodcache  the live warm cache (immutable once swapped in)
//	warm/commit                    the default-branch commit it was warmed from
//	.lock                          shared by clones, exclusive for the swap
//
// foremand is the only writer; the directory is denyWrite for sessions.
const (
	warmDirName    = "warm"
	warmCommitName = "commit"
	swapLockName   = ".lock"
	goCacheName    = "gocache"
	goModCacheName = "gomodcache"
)

// WarmedCommit returns the commit the warm cache in master was built from, or
// "" when there is none.
func WarmedCommit(master string) string {
	b, err := os.ReadFile(filepath.Join(master, warmDirName, warmCommitName))
	if err != nil {
		return ""
	}
	return string(b)
}

// flock takes an advisory lock on path (created when missing) and returns its release.
func flock(path string, how int) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// prepareCaches gives the run its own writable GOCACHE and GOMODCACHE: a
// clone of the project's warm cache, or empty directories when there is no
// warm cache or cloning fails (a run must never fail because of the cache).
// A cache directory that already exists is kept (second command of a Run).
func prepareCaches(pol policy.Policy, empty bool) {
	dsts := map[string]string{goCacheName: pol.GoCache(), goModCacheName: pol.GoModCache()}
	var missing []string
	for name, dst := range dsts {
		if _, err := os.Lstat(dst); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	if master := pol.ProjectCacheDir(); master != "" && !empty {
		_ = cloneFromMaster(master, missing, dsts)
	}
	for _, name := range missing {
		if _, err := os.Lstat(dsts[name]); err != nil {
			_ = os.MkdirAll(dsts[name], 0o700)
		}
	}
}

// cloneFromMaster clones warm/<name> to dsts[name] for every name, under the
// shared lock so a swap never renames the tree away mid-copy. On any error the
// partial copies are removed.
func cloneFromMaster(master string, names []string, dsts map[string]string) (err error) {
	unlock, err := flock(filepath.Join(master, swapLockName), syscall.LOCK_SH)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() {
		if err != nil {
			for _, n := range names {
				_ = os.RemoveAll(dsts[n])
			}
		}
	}()
	for _, n := range names {
		if err = CloneTree(filepath.Join(master, warmDirName, n), dsts[n]); err != nil {
			return err
		}
	}
	return nil
}

// CloneTree copies the directory src to the new path dst. On macOS (the
// production platform) it is an APFS clone (clonefile(2), the same copy-on-write
// as cp -c; same volume required: D-047 checks st_dev), falling back to a
// plain copy when the clone is refused. Elsewhere it is a plain recursive
// copy: production is macOS and CI caches are tiny. Modes are preserved.
func CloneTree(src, dst string) error {
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return fmt.Errorf("launch: clone: %s is not a directory", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("launch: clone: %s exists", dst)
	}
	if cloneDir(src, dst) == nil {
		return nil
	}
	_ = os.RemoveAll(dst)
	out, err := exec.Command("/bin/cp", "-R", src, dst).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dst)
		return errors.Join(fmt.Errorf("launch: clone %s: %w", src, err), errors.New(string(out)))
	}
	return nil
}
