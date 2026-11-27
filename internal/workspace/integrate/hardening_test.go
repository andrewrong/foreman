package integrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// plantMaliciousConfig makes the session repo run `touch marker` through the
// config-driven and hook-driven execution points git offers, as a hostile
// session could (it owns its own .git).
func plantMaliciousConfig(t *testing.T, w *world, marker string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "evil.sh")
	body := "#!/bin/sh\ntouch '" + marker + "'\n"
	mustErr(t, os.WriteFile(script, []byte(body), 0o700))

	hooksDir := filepath.Join(t.TempDir(), "hooks")
	mustErr(t, os.MkdirAll(hooksDir, 0o700))
	for _, h := range []string{"reference-transaction", "post-checkout", "post-merge", "pre-auto-gc", "post-rewrite"} {
		mustErr(t, os.WriteFile(filepath.Join(hooksDir, h), []byte(body), 0o700))
		mustErr(t, os.WriteFile(filepath.Join(w.sess.Dir, ".git", "hooks", h), []byte(body), 0o700))
	}
	cfg := filepath.Join(w.sess.Dir, ".git", "config")
	for k, v := range map[string]string{
		"core.fsmonitor":             script,
		"core.hooksPath":             hooksDir,
		"core.sshCommand":            script,
		"core.pager":                 script,
		"core.alternateRefsCommand":  script,
		"uploadpack.packObjectsHook": script,
		"gc.auto":                    "1",
	} {
		mustErr(t, w.r.SetConfig(context.Background(), cfg, k, v))
	}
}

func TestSessionGitConfigNotExecuted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	w.commitOnBranch("step/s1", w.sess.Branch, map[string]string{"one.txt": "1\n"}, "s1")

	marker := filepath.Join(t.TempDir(), "executed.marker")
	plantMaliciousConfig(t, w, marker)

	// Control: a plain, unhardened git in the poisoned repo does execute the
	// planted config (fsmonitor runs on status), so the marker check below can fail.
	ctl := exec.Command("git", "status", "--porcelain")
	ctl.Dir = w.sess.Dir
	ctl.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	_ = ctl.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control failed: planted config did not fire under plain git: %v", err)
	}
	mustErr(t, os.Remove(marker))

	// foremand's whole fetch / merge-back / push path must not fire it.
	sha := w.fetchStep("step/s1", "s1")
	if want := must(w.r.Line(ctx, "", "--git-dir", w.sess.Dir+"/.git", "rev-parse", "refs/heads/step/s1")); sha != want {
		t.Fatalf("fetched %s, session has %s", sha, want)
	}
	if _, err := w.merge("s1", StepRefPrefix+"steps/s1"); err != nil {
		t.Fatal(err)
	}
	mustErr(t, w.integ.Push(ctx, "r1"))
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("session config/hook was executed by foremand git (stat err = %v)", err)
	}
}

func TestRunnerEnvIsFromScratch(t *testing.T) {
	t.Setenv("FOREMAN_TEST_SECRET_TOKEN", "sentinel")
	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_AUTHOR_NAME", "leaked")
	rec := &recorder{}
	r := newRunner(t, rec.exec)
	must(r.Run(context.Background(), "", "--version"))
	env := strings.Join(rec.cmds[0].Env, "\n")
	for _, bad := range []string{"sentinel", "/nonexistent", "leaked"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment inherited %q:\n%s", bad, env)
		}
	}
	for _, need := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + r.Home(), "LANG=C"} {
		if !strings.Contains(env, need) {
			t.Fatalf("missing %q in env:\n%s", need, env)
		}
	}
	// The hardened prefix is present and no cwd outside Home leaks in.
	if got := strings.Join(rec.cmds[0].Args[:6], " "); got != strings.Join(git.HardenedArgs, " ") {
		t.Fatalf("argv prefix = %q", got)
	}
	if rec.cmds[0].Dir != r.Home() {
		t.Fatalf("default cwd = %q", rec.cmds[0].Dir)
	}
}

func TestHardenedGitIgnoresUserHookConfig(t *testing.T) {
	// A global hook config like the owner's (core.hooksPath in ~/.gitconfig)
	// must not apply to foremand's git: commits print nothing and run no hook.
	ctx := context.Background()
	home := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook.marker")
	hooks := filepath.Join(home, "hooks")
	mustErr(t, os.MkdirAll(hooks, 0o700))
	mustErr(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch '"+marker+"'\necho HOOK-RAN >&2\n"), 0o700))
	mustErr(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	rec := &recorder{}
	r := newRunner(t, rec.exec)
	work := t.TempDir()
	must(r.Run(ctx, work, "init", "-b", "main"))
	res, err := git.OSExec(ctx, git.Command{Path: "git", Dir: work, Env: r.Env(),
		Args: r.Argv("commit", "--allow-empty", "-m", "x")})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("commit failed: %v %s", err, res.Stderr)
	}
	if len(res.Stderr) != 0 || len(res.Stdout) > 0 && strings.Contains(string(res.Stdout), "HOOK") {
		t.Fatalf("hardened commit produced hook output: %q %q", res.Stdout, res.Stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("user hook ran (stat err = %v)", err)
	}
}
