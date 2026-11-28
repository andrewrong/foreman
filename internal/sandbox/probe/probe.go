// Package probe runs the startup/onboarding checks inside the real sandbox
// (access.yaml, C-08, D-056) and returns a structured report.
package probe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// Check names.
const (
	CheckGitCommit  = "git-commit-with-hook"
	CheckDeniedRead = "denied-read"
	CheckHostBlock  = "blocked-host"
	CheckEnv        = "env-no-credentials"
	CheckLocalBind  = "localhost-bind"
)

// Status of a single check.
type Status string

// Statuses.
const (
	Pass Status = "pass"
	Fail Status = "fail"
)

// Result is one check outcome.
type Result struct {
	Name   string
	Status Status
	Detail string
}

// Report is the structured probe outcome.
type Report struct{ Results []Result }

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, c := range r.Results {
		if c.Status != Pass {
			return false
		}
	}
	return len(r.Results) > 0
}

// Get returns the result of a named check.
func (r Report) Get(name string) (Result, bool) {
	for _, c := range r.Results {
		if c.Name == name {
			return c, true
		}
	}
	return Result{}, false
}

// Config is the probe input.
type Config struct {
	Launcher launch.Launcher
	Policy   policy.Policy
	// DeniedPath must exist and be listed in the policy's denyRead.
	DeniedPath string
	// BlockedHost must not be allowed by the policy (default api.github.com).
	BlockedHost string
	// CheckTimeout bounds each sandboxed check (default 30s).
	CheckTimeout time.Duration
}

// credLike matches variable names that look like credentials.
var credLike = regexp.MustCompile(`(?i)(token|key|secret|passw|credential|auth|cookie|session)`)

// srtInjected are credential-looking names srt itself adds for its local
// filtering proxy (observed 2026-11-28); they authenticate only to that proxy.
func srtInjected() map[string]bool {
	return map[string]bool{"CLOUDSDK_PROXY_PASSWORD": true, "CLOUDSDK_PROXY_USERNAME": true}
}

var envLine = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)=`)

// Fixed scripts; all variable data is passed as positional arguments.
const (
	gitScript = `set -e
d=$1
cd "$d"
mkdir -p hooks
printf '#!/bin/sh\necho ran > "$TMPDIR/hook-ran"\n' > hooks/pre-commit
chmod +x hooks/pre-commit
echo x > f
git add f
git -c core.hooksPath="$d/hooks" -c user.name=probe -c user.email=probe@example.invalid -c commit.gpgsign=false commit -q -m probe
git rev-parse --verify -q HEAD >/dev/null
test -f "$TMPDIR/hook-ran"`
	readScript = `ls -- "$1" >/dev/null 2>&1`
	hostScript = `code=$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "https://$1/" 2>/dev/null) || exit 0
echo "$code"`
	bindScript = `perl -MIO::Socket::INET -e 'IO::Socket::INET->new(Listen=>1,LocalAddr=>"127.0.0.1",LocalPort=>0) or exit 1'`
)

// Run executes all checks inside the sandbox.
func Run(ctx context.Context, cfg Config) Report {
	if cfg.CheckTimeout == 0 {
		cfg.CheckTimeout = 30 * time.Second
	}
	if cfg.BlockedHost == "" {
		cfg.BlockedHost = "api.github.com"
	}
	return Report{Results: []Result{
		checkGit(ctx, cfg),
		checkDeniedRead(ctx, cfg),
		checkBlockedHost(ctx, cfg),
		checkEnv(ctx, cfg),
		checkBind(ctx, cfg),
	}}
}

type outcome struct {
	exit   launch.Exit
	stdout string
	stderr string
	err    error
}

func sh(ctx context.Context, cfg Config, script string, args ...string) outcome {
	ctx, cancel := context.WithTimeout(ctx, cfg.CheckTimeout)
	defer cancel()
	var out, errb bytes.Buffer
	argv := append([]string{"/bin/sh", "-c", script, "sh"}, args...)
	ex, err := cfg.Launcher.Run(ctx, cfg.Policy, argv, launch.IO{Stdout: &out, Stderr: &errb})
	return outcome{exit: ex, stdout: out.String(), stderr: errb.String(), err: err}
}

func res(name string, ok bool, detail string) Result {
	if ok {
		return Result{Name: name, Status: Pass, Detail: detail}
	}
	return Result{Name: name, Status: Fail, Detail: detail}
}

func checkGit(ctx context.Context, cfg Config) Result {
	dir, err := os.MkdirTemp(cfg.Policy.SessionRepo(), ".probe-")
	if err != nil {
		return res(CheckGitCommit, false, "scratch repo: "+err.Error())
	}
	defer os.RemoveAll(dir)
	// srt makes .git/config and .git/hooks read-only, so the scratch repo is
	// created outside the sandbox, as foremand creates session repos.
	if err := initRepo(ctx, dir); err != nil {
		return res(CheckGitCommit, false, "git init: "+err.Error())
	}
	marker := cfg.Policy.RunTmp() + "/hook-ran"
	_ = os.Remove(marker)
	defer os.Remove(marker)
	o := sh(ctx, cfg, gitScript, dir)
	if o.err != nil || o.exit.Code != 0 {
		return res(CheckGitCommit, false, fmt.Sprintf("exit %d err=%v stderr=%s", o.exit.Code, o.err, trim(o.stderr)))
	}
	return res(CheckGitCommit, true, "commit succeeded and pre-commit hook ran")
}

func initRepo(ctx context.Context, dir string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, git, "init", "-q", "--template=", dir)
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, trim(string(out)))
	}
	return nil
}

func checkDeniedRead(ctx context.Context, cfg Config) Result {
	if cfg.DeniedPath == "" {
		return res(CheckDeniedRead, false, "no denied path configured")
	}
	denied := false
	for _, d := range cfg.Policy.DenyReadPaths() {
		if d == cfg.DeniedPath {
			denied = true
		}
	}
	if !denied {
		return res(CheckDeniedRead, false, "denied path is not in the policy denyRead list")
	}
	if _, err := os.Stat(cfg.DeniedPath); err != nil {
		return res(CheckDeniedRead, false, "denied path does not exist outside the sandbox")
	}
	o := sh(ctx, cfg, readScript, cfg.DeniedPath)
	if o.err != nil {
		return res(CheckDeniedRead, false, "launch: "+o.err.Error())
	}
	return res(CheckDeniedRead, o.exit.Code != 0, fmt.Sprintf("read exit code %d", o.exit.Code))
}

// blockedCodes are what curl reports when the sandbox proxy refuses a host.
func blockedCodes() map[string]bool {
	return map[string]bool{"": true, "000": true, "403": true, "407": true, "502": true}
}

func checkBlockedHost(ctx context.Context, cfg Config) Result {
	for _, h := range cfg.Policy.AllowedHosts() {
		if h == cfg.BlockedHost {
			return res(CheckHostBlock, false, "host is allowed by the policy")
		}
	}
	o := sh(ctx, cfg, hostScript, cfg.BlockedHost)
	if o.err != nil {
		return res(CheckHostBlock, false, "launch: "+o.err.Error())
	}
	code := strings.TrimSpace(o.stdout)
	return res(CheckHostBlock, blockedCodes()[code], "http code "+code)
}

func checkEnv(ctx context.Context, cfg Config) Result {
	o := sh(ctx, cfg, "env")
	if o.err != nil || o.exit.Code != 0 {
		return res(CheckEnv, false, fmt.Sprintf("env failed: exit %d err=%v", o.exit.Code, o.err))
	}
	allowed := map[string]bool{}
	for _, n := range cfg.Policy.AllowedNames() {
		allowed[n] = true
	}
	var bad []string
	for _, m := range envLine.FindAllStringSubmatch(o.stdout, -1) {
		if credLike.MatchString(m[1]) && !allowed[m[1]] && !srtInjected()[m[1]] {
			bad = append(bad, m[1])
		}
	}
	if len(bad) > 0 {
		return res(CheckEnv, false, "unexpected credential-like variables: "+strings.Join(bad, ","))
	}
	return res(CheckEnv, true, "no credential-like variables beyond the allowed ones")
}

func checkBind(ctx context.Context, cfg Config) Result {
	o := sh(ctx, cfg, bindScript)
	if o.err != nil {
		return res(CheckLocalBind, false, "launch: "+o.err.Error())
	}
	want := cfg.Policy.LocalBind()
	got := o.exit.Code == 0
	return res(CheckLocalBind, got == want, fmt.Sprintf("bind succeeded=%v allowed=%v", got, want))
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
