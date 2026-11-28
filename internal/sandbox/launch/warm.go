package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// ErrWarming is returned when another warm-up of the same master is running.
var ErrWarming = errors.New("launch: the project cache is already being warmed")

// Warm describes one warm-up of the project cache master. It runs outside the
// sandbox, in foremand, so it must never execute project code: only commands
// that download modules and compile (see warmSteps).
type Warm struct {
	Master string // FOREMAN_HOME/cache/<p>
	Src    string // checkout of the default branch (its toolchain already resolved)
	Commit string // the commit Src is at; recorded as warm/commit
	// Path is the PATH for the go tool (the resolved toolchain first, D-085).
	Path string
	// Environ supplies the proxy and TLS settings forwarded to go (GOPROXY ...);
	// nil means the process environment. Nothing else is inherited.
	Environ func() []string
}

// WarmResult reports what a warm-up did.
type WarmResult struct {
	Warmed bool   // false: the master was already warm for Commit
	Log    string // output of the steps; a failed compile is noted here
}

// warmSteps are the go invocations that fill the cache. None of them runs a
// project binary: `go mod download` only fetches; `go build ./...` compiles
// (no go generate, no cgo program is executed, cgo only invokes the C
// compiler); `go test -run '^$' -exec true` compiles every test binary, and
// its -exec wrapper means a test binary is handed to /usr/bin/true as an
// argument, never started. A failing compile does not abort the warm-up (the
// cache is still useful); a failing download does.
func warmSteps(execTrue string) []struct {
	args  []string
	fatal bool
} {
	return []struct {
		args  []string
		fatal bool
	}{
		{[]string{"mod", "download"}, true},
		{[]string{"build", "./..."}, false},
		{[]string{"test", "-count=1", "-run", "^$", "-exec", execTrue, "./..."}, false},
	}
}

func forwardedEnv() []string {
	return []string{"GOPROXY", "GONOPROXY", "GONOSUMDB", "GOPRIVATE", "GOSUMDB", "GOINSECURE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}
}

// WarmCache fills master/warm for w.Commit unless it already holds it. The
// new cache is built in a staging directory beside warm/ and swapped in with
// two renames under the exclusive swap lock, which clones hold shared, so a
// run never clones a half-written cache.
func WarmCache(ctx context.Context, w Warm) (WarmResult, error) {
	if w.Master == "" || w.Src == "" || w.Commit == "" || w.Path == "" {
		return WarmResult{}, errors.New("launch: warm: Master, Src, Commit and Path are required")
	}
	unlock, err := flock(filepath.Join(w.Master, ".warm.lock"), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return WarmResult{}, ErrWarming
	}
	if err != nil {
		return WarmResult{}, err
	}
	defer unlock()
	if WarmedCommit(w.Master) == w.Commit {
		return WarmResult{}, nil
	}
	sweepResidue(w.Master)
	stage, err := os.MkdirTemp(w.Master, ".staging-")
	if err != nil {
		return WarmResult{}, err
	}
	defer removeAllWritable(stage)
	var dirs = map[string]string{}
	for _, n := range []string{goCacheName, goModCacheName, "home", "tmp"} {
		dirs[n] = filepath.Join(stage, n)
		// Incremental: start from the live cache when there is one. Only the
		// holder of the warm lock swaps warm/, so reading it needs no swap lock.
		if n == goCacheName || n == goModCacheName {
			if WarmedCommit(w.Master) != "" && CloneTree(filepath.Join(w.Master, warmDirName, n), dirs[n]) == nil {
				continue
			}
		}
		if err := os.Mkdir(dirs[n], 0o700); err != nil {
			return WarmResult{}, err
		}
	}
	env := warmEnv(w, dirs)
	var log bytes.Buffer
	if _, err := os.Stat(filepath.Join(w.Src, "go.mod")); err == nil {
		if err := runWarmSteps(ctx, w, env, &log); err != nil {
			return WarmResult{Log: log.String()}, err
		}
	}
	for _, n := range []string{"home", "tmp"} {
		if err := os.RemoveAll(dirs[n]); err != nil {
			return WarmResult{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, warmCommitName), []byte(w.Commit), 0o600); err != nil {
		return WarmResult{}, err
	}
	return WarmResult{Warmed: true, Log: log.String()}, swapIn(w.Master, stage)
}

// runWarmSteps runs warmSteps in w.Src with env, logging to log.
func runWarmSteps(ctx context.Context, w Warm, env []string, log *bytes.Buffer) error {
	goBin, err := lookPath("go", w.Path)
	if err != nil {
		return err
	}
	execTrue := "/usr/bin/true"
	if _, err := os.Stat(execTrue); err != nil {
		execTrue = "/bin/true"
	}
	for _, s := range warmSteps(execTrue) {
		fmt.Fprintf(log, "$ go %s\n", strings.Join(s.args, " "))
		cmd := exec.CommandContext(ctx, goBin, s.args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = w.Src, env, log, log
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil || s.fatal {
				return fmt.Errorf("launch: warm: go %s: %w", s.args[0], err)
			}
			fmt.Fprintf(log, "(continuing: %v)\n", err)
		}
	}
	return nil
}

// swapIn makes stage the live warm/ directory.
func swapIn(master, stage string) error {
	unlock, err := flock(filepath.Join(master, swapLockName), syscall.LOCK_EX)
	if err != nil {
		return err
	}
	live := filepath.Join(master, warmDirName)
	old := ""
	if _, err := os.Lstat(live); err == nil {
		old = filepath.Join(master, fmt.Sprintf(".old-%d", os.Getpid()))
		removeAllWritable(old)
		if err := os.Rename(live, old); err != nil {
			unlock()
			return err
		}
	}
	err = os.Rename(stage, live)
	unlock()
	if old != "" {
		removeAllWritable(old)
	}
	return err
}

// sweepResidue removes what a crashed warm-up left behind. The caller holds
// the warm lock, so nothing here is in use.
func sweepResidue(master string) {
	entries, err := os.ReadDir(master)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") || strings.HasPrefix(e.Name(), ".old-") {
			removeAllWritable(filepath.Join(master, e.Name()))
		}
	}
}

func warmEnv(w Warm, dirs map[string]string) []string {
	environ := os.Environ
	if w.Environ != nil {
		environ = w.Environ
	}
	env := []string{
		"PATH=" + w.Path, "HOME=" + dirs["home"], "TMPDIR=" + dirs["tmp"], "LANG=en_US.UTF-8",
		"GOCACHE=" + dirs[goCacheName], "GOMODCACHE=" + dirs[goModCacheName],
		// Same flags as the sessions (build cache keys), no VCS stamping (it would run git in the checkout),
		// and never fetch another Go toolchain: the resolved one is the one.
		"GOFLAGS=" + policy.DefaultGoFlags + " -buildvcs=false", "GOTOOLCHAIN=local",
	}
	for _, kv := range environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, f := range forwardedEnv() {
			if name == f {
				env = append(env, kv)
			}
		}
	}
	return env
}

// lookPath finds name in the colon-separated path.
func lookPath(name, path string) (string, error) {
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("launch: warm: %s not found on PATH %q", name, path)
}

// removeAllWritable is os.RemoveAll that first restores owner write on every
// directory below path (a Go module cache without -modcacherw is read-only).
// It never follows symbolic links.
func removeAllWritable(path string) {
	if os.RemoveAll(path) == nil {
		return
	}
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(path)
}
