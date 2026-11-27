package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// FlagRunSession selects the launcher mode of the foreman binary: foremand
// starts "<exe> -foreman-run-session <run dir> -- <argv...>" in a new session
// (setsid); the launcher records pid.json and then execs argv, so the session
// process keeps the launcher's pid (state-machine.yaml reconciler, GEM-6).
const FlagRunSession = "-foreman-run-session"

// PidFileName is written by the launcher into the run directory.
const PidFileName = "pid.json"

// handshakeFD is the descriptor the launcher closes once pid.json is on disk;
// foremand blocks on its read end instead of polling for the file.
const handshakeFD = 3

// PidFile is the content of pid.json: the process and when it started, so a
// recycled pid is told apart from the session (reconciler idempotency).
type PidFile struct {
	Pid       int    `json:"pid"`
	StartTime string `json:"start_time"`
}

// ReadPidFile reads runDir/pid.json; ok is false when it does not exist.
func ReadPidFile(runDir string) (p PidFile, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(runDir, PidFileName))
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Pid <= 0 {
		return PidFile{}, false, fmt.Errorf("launch: bad %s in %s", PidFileName, runDir)
	}
	return p, true, nil
}

// Alive reports whether the process of p is still the running session: it
// exists, is not a zombie and started when pid.json says it did.
func (p PidFile) Alive() bool {
	start, zombie, ok := ProcInfo(p.Pid)
	return ok && !zombie && start == p.StartTime
}

// psPath finds ps without trusting PATH: a launcher runs with the session's
// minimal PATH, which on macOS does not contain /bin.
func psPath() string {
	for _, p := range []string{"/bin/ps", "/usr/bin/ps"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "ps"
}

// ProcInfo reads a process's start time and state with ps (works on macOS and
// Linux; there is no /proc on macOS).
func ProcInfo(pid int) (start string, zombie, ok bool) {
	cmd := exec.Command(psPath(), "-o", "stat=,lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/bin:/usr/bin"}
	out, err := cmd.Output()
	if err != nil {
		return "", false, false
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return "", false, false
	}
	return strings.Join(f[1:], " "), strings.HasPrefix(f[0], "Z"), true
}

// Handle is a session started by StartDetached. Done closes when the process
// exited and was reaped; it exists so tests can wait for a real exit without
// sleeping, and so a daemon never leaves a zombie.
type Handle struct {
	Pid  int
	done chan struct{}
	exit Exit
}

// Done is closed when the process has exited.
func (h *Handle) Done() <-chan struct{} { return h.done }

// Exit is valid after Done is closed.
func (h *Handle) Exit() Exit { return h.exit }

// Detached carries what a detached session needs besides the policy.
type Detached struct {
	RunDir string
	// Exe is the foreman binary that serves FlagRunSession; default os.Args[0].
	Exe        string
	Stdin      []byte
	ExtraEnv   map[string]string
	StdoutPath string
	StderrPath string
}

// StartDetached starts argv through a launcher in its own session so that it
// outlives foremand. It returns after the launcher wrote pid.json (a pipe
// handshake, not polling). The caller creates launch.lock before and records
// the pid in the Run after.
func (l Launcher) StartDetached(pol policy.Policy, argv []string, d Detached) (*Handle, error) {
	spec, err := l.spec(pol, argv, IO{ExtraEnv: d.ExtraEnv})
	if err != nil {
		return nil, err
	}
	exe := d.Exe
	if exe == "" {
		exe = os.Args[0]
	}
	stdin, err := os.CreateTemp(d.RunDir, "stdin-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(stdin.Name())
	defer stdin.Close()
	if _, err := io.Copy(stdin, bytes.NewReader(d.Stdin)); err != nil {
		return nil, err
	}
	if _, err := stdin.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out, err := os.Create(d.StdoutPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	errf, err := os.Create(d.StderrPath)
	if err != nil {
		return nil, err
	}
	defer errf.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()

	cmd := exec.Command(exe, append([]string{FlagRunSession, d.RunDir, "--", spec.Path}, spec.Args...)...)
	cmd.Env = append([]string{}, spec.Env...)
	cmd.Dir = spec.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, out, errf
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return nil, err
	}
	_ = pw.Close()
	h := &Handle{Pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		h.exit = exitOf(cmd.Wait())
		close(h.done)
	}()
	_, _ = io.Copy(io.Discard, pr) // EOF: the launcher closed the handshake or died
	pf, ok, err := ReadPidFile(d.RunDir)
	if err != nil || !ok || pf.Pid != h.Pid {
		return nil, fmt.Errorf("launch: launcher did not record pid.json (err %v)", err)
	}
	return h, nil
}

func exitOf(err error) Exit {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		return Exit{Code: code, Signaled: code < 0}
	}
	if err != nil {
		return Exit{Code: -1}
	}
	return Exit{}
}

// MaybeRunSession serves FlagRunSession and never returns in that mode;
// otherwise it returns immediately. Call it first in main and in TestMain.
func MaybeRunSession() {
	if len(os.Args) < 5 || os.Args[1] != FlagRunSession || os.Args[3] != "--" {
		return
	}
	fmt.Fprintln(os.Stderr, "launcher:", runSession(os.Args[2], os.Args[4:]))
	os.Exit(125)
}

// runSession writes pid.json, releases the handshake and execs argv. It
// returns only on failure.
func runSession(runDir string, argv []string) error {
	pid := os.Getpid()
	start, _, ok := ProcInfo(pid)
	if !ok {
		return fmt.Errorf("cannot read own start time of %d", pid)
	}
	raw, _ := json.Marshal(PidFile{Pid: pid, StartTime: start})
	tmp := filepath.Join(runDir, PidFileName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(runDir, PidFileName)); err != nil {
		return err
	}
	_ = syscall.Close(handshakeFD) // the session must not inherit it
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
