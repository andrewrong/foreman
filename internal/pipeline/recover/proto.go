// Package recover holds what foremand needs to survive its own restart: the
// launch.lock / pid.json protocol that tells a session's real state from the
// files in its run directory (state-machine.yaml reconciler, GEM-6), the
// startup checks of D-047, and the rebuild of the Gate ledger from the Runs
// that hold reservations (resources.yaml RT-5). sched calls it; it never
// applies Transitions itself.
package recover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Protocol constants.
const (
	LockFileName = "launch.lock"
	// StartGrace is how long a launch.lock without pid.json may be left alone:
	// the launcher is probably still starting. Older than this it counts as a
	// failed start.
	StartGrace = 60 * time.Second
	// MaxStartAttempts bounds relaunches of one Run: the second failed start
	// is reported as infra instead of looping.
	MaxStartAttempts = 2
)

// ErrLockExists means another start of the same run directory is in flight or
// left its lock behind (GEM-6: one session per run directory).
var ErrLockExists = errors.New("recover: launch.lock exists")

// Phase is the state of a Run's session as the run directory shows it.
type Phase int

// Phases of a session.
const (
	NotStarted  Phase = iota // no launch.lock, no pid.json
	Starting                 // launch.lock younger than StartGrace, no pid.json
	StartFailed              // launch.lock older than StartGrace, no pid.json
	Alive                    // pid.json names a live process with the recorded start time
	Exited                   // pid.json names a process that is gone, a zombie, or a recycled pid
)

// PidFile is the content of pid.json: the process and when it started, so a
// recycled pid is told apart from the session (reconciler idempotency).
type PidFile struct {
	Pid       int
	StartTime string
}

// Procs is the process side of a run directory: the pid.json the launcher
// wrote, whether that process is still the session, and killing everything a
// run left behind. The composition root supplies it (from the sandbox
// launcher); this package never starts or signals a process itself.
type Procs interface {
	// ReadPid reads runDir/pid.json; ok is false when it does not exist.
	ReadPid(runDir string) (pf PidFile, ok bool, err error)
	// Alive reports whether the process of pf is still the running session:
	// it exists, is not a zombie and started when pid.json says it did.
	Alive(pf PidFile) bool
	// ForgetPid removes runDir/pid.json.
	ForgetPid(runDir string) error
	// KillRun kills every process of the run and returns their pids.
	KillRun(runDir string) ([]int, error)
}

// Probe is the result of Inspect.
type Probe struct {
	Phase   Phase
	Pid     PidFile   // valid for Alive and Exited
	Attempt int       // start attempt recorded in launch.lock (0 without a lock)
	Since   time.Time // when foremand took launch.lock (zero without a lock)
}

type lockBody struct {
	At      string `json:"at"`
	Attempt int    `json:"attempt"`
}

// Inspect classifies a run directory. Time comes from the caller's clock, so
// the 60 s rule runs under a fake clock.
func Inspect(procs Procs, runDir string, now time.Time) (Probe, error) {
	pf, ok, err := procs.ReadPid(runDir)
	if err != nil {
		return Probe{}, err
	}
	lock, hasLock, err := readLock(runDir)
	if err != nil {
		return Probe{}, err
	}
	var since time.Time
	if hasLock {
		if since, err = time.Parse(time.RFC3339Nano, lock.At); err != nil {
			return Probe{}, fmt.Errorf("recover: bad %s in %s: %w", LockFileName, runDir, err)
		}
	}
	if ok {
		p := Probe{Phase: Exited, Pid: pf, Attempt: lock.Attempt, Since: since}
		if procs.Alive(pf) {
			p.Phase = Alive
		}
		return p, nil
	}
	if !hasLock {
		return Probe{Phase: NotStarted}, nil
	}
	if now.Sub(since) < StartGrace {
		return Probe{Phase: Starting, Attempt: lock.Attempt, Since: since}, nil
	}
	return Probe{Phase: StartFailed, Attempt: lock.Attempt, Since: since}, nil
}

// TakeLock creates runDir/launch.lock with O_EXCL. It must happen before the
// launcher is forked; a second caller gets ErrLockExists.
func TakeLock(runDir string, now time.Time, attempt int) error {
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runDir, LockFileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrLockExists
	}
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(lockBody{At: now.UTC().Format(time.RFC3339Nano), Attempt: attempt})
	_, werr := f.Write(raw)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// DropLock removes launch.lock (and a pid.json that cannot belong to a live
// session) so the run can be started again.
func DropLock(procs Procs, runDir string) error {
	if err := os.Remove(filepath.Join(runDir, LockFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return procs.ForgetPid(runDir)
}

func readLock(runDir string) (lockBody, bool, error) {
	raw, err := os.ReadFile(filepath.Join(runDir, LockFileName))
	if errors.Is(err, os.ErrNotExist) {
		return lockBody{}, false, nil
	}
	if err != nil {
		return lockBody{}, false, err
	}
	var b lockBody
	if err := json.Unmarshal(raw, &b); err != nil {
		// A lock cut short by a crash between create and write: treat it as
		// created at the epoch so the 60 s rule reclaims it.
		return lockBody{At: time.Unix(0, 0).UTC().Format(time.RFC3339Nano), Attempt: 1}, true, nil
	}
	return b, true, nil
}
