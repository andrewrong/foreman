// Package launch wraps an argv into an srt invocation. There is no shell
// interpolation: argv is passed to srt as separate arguments after "--".
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// Spec is a fully resolved process to start.
type Spec struct {
	Path   string
	Args   []string // excluding Path
	Env    []string // complete environment; nothing is inherited
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Exit describes how a process ended.
type Exit struct {
	Code     int // -1 when killed by a signal
	Signaled bool
}

// Process is a started process.
type Process interface {
	Pid() int
	Wait() (Exit, error)
	Kill() error
}

// Runner starts processes; injectable for tests.
type Runner interface {
	Start(ctx context.Context, s Spec) (Process, error)
}

// IO carries optional standard streams for the sandboxed process.
type IO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// ExtraEnv are the environment additions of a harness Invocation
	// (api.Invocation.Env). They are appended to the policy environment; a
	// name the policy environment already sets is refused, so an adapter
	// cannot override HOME, TMPDIR, PATH or a credential.
	ExtraEnv map[string]string
	// NoGoCache gives the process empty GOCACHE and GOMODCACHE instead of
	// clones of the project's warm cache: for processes that never run go
	// (the scripted model adapter), where a clone is pure cost.
	NoGoCache bool
}

// Launcher starts sandboxed processes.
type Launcher struct {
	SrtPath string
	// NodeDir is the directory of the real node binary (sandbox.node_dir). srt
	// is a node script ("#!/usr/bin/env node"); a mise shim found on PATH makes
	// it fail to start (probe P4). It goes first in the PATH srt starts with,
	// which the sandboxed process inherits.
	NodeDir string
	Runner  Runner // nil means the real os/exec runner
	// PassThrough lets an empty SrtPath run argv directly, with the same
	// from-scratch environment and working directory but no OS sandbox.
	// Only for adapters that need no sandbox (scripted) and for tests; the
	// caller must refuse sandbox-requiring adapters (claude) when Sandboxed
	// is false.
	PassThrough bool
}

// Sandboxed reports whether argv would run inside srt.
func (l Launcher) Sandboxed() bool { return l.SrtPath != "" }

// Command returns the srt argv (without the srt path itself) for argv.
func Command(settingsPath string, argv []string) []string {
	out := []string{"--settings", settingsPath, "--"}
	return append(out, argv...)
}

// Start writes the settings file (0600) into the run tmp and starts argv
// inside the sandbox with exactly the policy environment, in the session repo.
func (l Launcher) Start(ctx context.Context, pol policy.Policy, argv []string, io IO) (Process, error) {
	spec, err := l.spec(pol, argv, io)
	if err != nil {
		return nil, err
	}
	r := l.Runner
	if r == nil {
		r = ExecRunner{}
	}
	return r.Start(ctx, spec)
}

// spec resolves argv, policy and streams into the process to start.
func (l Launcher) spec(pol policy.Policy, argv []string, io IO) (Spec, error) {
	if l.SrtPath == "" && !l.PassThrough {
		return Spec{}, errors.New("launch: srt path not set")
	}
	if len(argv) == 0 || argv[0] == "" {
		return Spec{}, errors.New("launch: empty argv")
	}
	settings, err := pol.SettingsJSON()
	if err != nil {
		return Spec{}, err
	}
	if err := os.MkdirAll(pol.RunTmp(), 0o700); err != nil {
		return Spec{}, err
	}
	if err := os.MkdirAll(pol.Home(), 0o700); err != nil {
		return Spec{}, err
	}
	prepareCaches(pol, io.NoGoCache) // the run's own GOCACHE and GOMODCACHE, cloned from the warm cache
	if err := writePrivate(pol.SettingsPath(), settings); err != nil {
		return Spec{}, err
	}
	// D-082: the generated session gitconfig, rewritten on every start.
	if err := writePrivate(pol.GitConfigPath(), pol.GitConfig()); err != nil {
		return Spec{}, err
	}
	env, err := mergeEnv(pol.Env(), io.ExtraEnv)
	if err != nil {
		return Spec{}, err
	}
	if l.SrtPath != "" && l.NodeDir != "" {
		env = withNodeFirst(env, l.NodeDir)
	}
	path, args := l.SrtPath, Command(pol.SettingsPath(), argv)
	if path == "" {
		path, args = argv[0], argv[1:]
	}
	return Spec{Path: path, Args: args, Env: env, Dir: pol.SessionRepo(), Stdin: io.Stdin, Stdout: io.Stdout, Stderr: io.Stderr}, nil
}

// Run starts argv and waits for it to finish.
func (l Launcher) Run(ctx context.Context, pol policy.Policy, argv []string, io IO) (Exit, error) {
	p, err := l.Start(ctx, pol, argv, io)
	if err != nil {
		return Exit{Code: -1}, err
	}
	return p.Wait()
}

// withNodeFirst puts dir at the front of the PATH entry of env.
func withNodeFirst(env []string, dir string) []string {
	out := append([]string{}, env...)
	for i, kv := range out {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			out[i] = "PATH=" + dir + ":" + v
			return out
		}
	}
	return append(out, "PATH="+dir)
}

// Resolve returns a copy of l with SrtPath and NodeDir replaced by their real
// paths (EvalSymlinks); a missing one is an error naming it (D-081 ②, the
// startup self-check). A pass-through launcher (no SrtPath) is returned as is.
func (l Launcher) Resolve() (Launcher, error) {
	if l.SrtPath == "" {
		return l, nil
	}
	for name, p := range map[string]*string{"srt path": &l.SrtPath, "node dir": &l.NodeDir} {
		if *p == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(*p)
		if err != nil {
			return l, fmt.Errorf("launch: %s %s: %w", name, *p, err)
		}
		*p = real
	}
	return l, nil
}

// mergeEnv appends the sorted additions to base, refusing names base sets.
func mergeEnv(base []string, extra map[string]string) ([]string, error) {
	if len(extra) == 0 {
		return base, nil
	}
	have := map[string]bool{}
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		have[name] = true
	}
	names := make([]string, 0, len(extra))
	for n := range extra {
		if n == "" || strings.Contains(n, "=") || have[n] {
			return nil, fmt.Errorf("launch: environment addition %q is invalid or overrides the policy environment", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	out := append([]string{}, base...)
	for _, n := range names {
		out = append(out, n+"="+extra[n])
	}
	return out, nil
}

func writePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// ExecRunner runs processes with os/exec. cmd.Env is always set explicitly,
// so the parent environment is never inherited.
type ExecRunner struct{}

type execProc struct{ cmd *exec.Cmd }

// Start implements Runner.
func (ExecRunner) Start(ctx context.Context, s Spec) (Process, error) {
	cmd := exec.CommandContext(ctx, s.Path, s.Args...)
	cmd.Env = append([]string{}, s.Env...)
	if len(cmd.Env) == 0 {
		cmd.Env = []string{} // non-nil: empty, not inherited
	}
	cmd.Dir = s.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.Stdin, s.Stdout, s.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd}, nil
}

func (p *execProc) Pid() int { return p.cmd.Process.Pid }

func (p *execProc) Kill() error { return p.cmd.Process.Kill() }

func (p *execProc) Wait() (Exit, error) {
	err := p.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		return Exit{Code: code, Signaled: code < 0}, nil
	}
	if err != nil {
		return Exit{Code: -1}, err
	}
	return Exit{}, nil
}
