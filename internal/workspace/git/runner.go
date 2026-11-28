// Package git is the hardened git runner used by every workspace package
// (D-045, D-056). The environment is built from scratch and never inherited.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/andrewrong/foreman/internal/clock"
)

// DefaultPath is the fixed PATH given to git when Config.Path is empty.
const DefaultPath = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"

// HardenedArgs are prepended to every git invocation.
func HardenedArgs() []string {
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "protocol.file.allow=always",
		// No detached background work (auto gc / maintenance): foremand must not
		// leave git processes writing into its directories after a call returns.
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	}
}

// Identity is the commit identity injected with -c user.name/-c user.email.
type Identity struct{ Name, Email string }

// Command is one process invocation handed to an ExecFunc.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Result is the captured outcome of a Command. ExitCode is non-zero when the
// process ran and failed; an ExecFunc returns an error only if it could not
// run the process at all.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// ExecFunc runs a Command. It is injectable so tests can observe argv and env.
type ExecFunc func(ctx context.Context, c Command) (Result, error)

// OSExec runs the command with os/exec using exactly c.Env.
func OSExec(ctx context.Context, c Command) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// Error is a git invocation that ran and exited non-zero.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// Config configures a Runner.
type Config struct {
	Home   string // foremand-owned directory used as HOME and as default cwd
	Path   string // PATH for git; DefaultPath when empty
	GitBin string // git binary; resolved from PATH when empty
	Exec   ExecFunc
	Clock  clock.Clock // when set, GIT_AUTHOR_DATE/GIT_COMMITTER_DATE come from it
	Ident  *Identity
	// ExtraEnv entries (NAME=value) are appended to the environment of every
	// invocation; WithEnv sets it. It carries per-process credentials
	// (GIT_CONFIG_COUNT/KEY/VALUE), never argv and never a file.
	ExtraEnv []string
}

// Runner runs hardened git commands.
type Runner struct {
	cfg  Config
	exec ExecFunc
}

// New validates cfg and returns a Runner. Home is created if missing.
func New(cfg Config) (*Runner, error) {
	if cfg.Home == "" {
		return nil, errors.New("git: Config.Home is required")
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return nil, err
	}
	if cfg.Path == "" {
		cfg.Path = DefaultPath
	}
	if cfg.GitBin == "" {
		if p, err := exec.LookPath("git"); err == nil {
			cfg.GitBin = p
		} else {
			cfg.GitBin = "git"
		}
	}
	r := &Runner{cfg: cfg, exec: cfg.Exec}
	if r.exec == nil {
		r.exec = OSExec
	}
	return r, nil
}

// Home returns the foremand-owned HOME directory.
func (r *Runner) Home() string { return r.cfg.Home }

// WithIdentity returns a copy of r that injects id into every invocation.
func (r *Runner) WithIdentity(id Identity) *Runner {
	c := *r
	c.cfg.Ident = &id
	return &c
}

// WithEnv returns a copy of r whose invocations also get kv (NAME=value).
func (r *Runner) WithEnv(kv ...string) *Runner {
	c := *r
	c.cfg.ExtraEnv = append(append([]string(nil), r.cfg.ExtraEnv...), kv...)
	return &c
}

// Env returns the complete, from-scratch environment for git.
func (r *Runner) Env() []string {
	env := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"HOME=" + r.cfg.Home,
		"PATH=" + r.cfg.Path,
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
	}
	if r.cfg.Clock != nil {
		d := "@" + strconv.FormatInt(r.cfg.Clock.Now().Unix(), 10) + " +0000"
		env = append(env, "GIT_AUTHOR_DATE="+d, "GIT_COMMITTER_DATE="+d)
	}
	return append(env, r.cfg.ExtraEnv...)
}

// Argv returns the full argument vector (after the binary) for args.
func (r *Runner) Argv(args ...string) []string {
	argv := append([]string(nil), HardenedArgs()...)
	if id := r.cfg.Ident; id != nil {
		argv = append(argv, "-c", "user.name="+id.Name, "-c", "user.email="+id.Email)
	}
	return append(argv, args...)
}

// Run executes git args with cwd dir (Home when empty) and returns stdout.
// A non-zero exit yields *Error.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if dir == "" {
		dir = r.cfg.Home
	}
	res, err := r.exec(ctx, Command{Path: r.cfg.GitBin, Args: r.Argv(args...), Dir: dir, Env: r.Env()})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return string(res.Stdout), &Error{Args: args, ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
	}
	return string(res.Stdout), nil
}

// Line is Run with surrounding whitespace trimmed from stdout.
func (r *Runner) Line(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := r.Run(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// SetConfig writes key=value into the config file of a repo without running
// git inside that repo (git config --file), so a session repo is never cwd.
func (r *Runner) SetConfig(ctx context.Context, file, key, value string) error {
	_, err := r.Run(ctx, "", "config", "--file", file, key, value)
	return err
}
