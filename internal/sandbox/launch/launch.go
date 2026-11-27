// Package launch wraps an argv into an srt invocation. There is no shell
// interpolation: argv is passed to srt as separate arguments after "--".
package launch

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

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
}

// Launcher starts sandboxed processes.
type Launcher struct {
	SrtPath string
	Runner  Runner // nil means the real os/exec runner
}

// Command returns the srt argv (without the srt path itself) for argv.
func Command(settingsPath string, argv []string) []string {
	out := []string{"--settings", settingsPath, "--"}
	return append(out, argv...)
}

// Start writes the settings file (0600) into the run tmp and starts argv
// inside the sandbox with exactly the policy environment, in the session repo.
func (l Launcher) Start(ctx context.Context, pol policy.Policy, argv []string, io IO) (Process, error) {
	if l.SrtPath == "" {
		return nil, errors.New("launch: srt path not set")
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("launch: empty argv")
	}
	settings, err := pol.SettingsJSON()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pol.RunTmp(), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pol.Home(), 0o700); err != nil {
		return nil, err
	}
	if err := writePrivate(pol.SettingsPath(), settings); err != nil {
		return nil, err
	}
	r := l.Runner
	if r == nil {
		r = ExecRunner{}
	}
	return r.Start(ctx, Spec{
		Path:   l.SrtPath,
		Args:   Command(pol.SettingsPath(), argv),
		Env:    pol.Env(),
		Dir:    pol.SessionRepo(),
		Stdin:  io.Stdin,
		Stdout: io.Stdout,
		Stderr: io.Stderr,
	})
}

// Run starts argv and waits for it to finish.
func (l Launcher) Run(ctx context.Context, pol policy.Policy, argv []string, io IO) (Exit, error) {
	p, err := l.Start(ctx, pol, argv, io)
	if err != nil {
		return Exit{Code: -1}, err
	}
	return p.Wait()
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
