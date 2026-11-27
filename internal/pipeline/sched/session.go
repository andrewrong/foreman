package sched

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/spec/model"
	stagecode "github.com/andrewrong/foreman/internal/stage/code"
)

// driveSession is the reconciler protocol for running(code|review)
// (state-machine.yaml reconciler): it looks at the run directory, starts the
// session when there is none, leaves a live one alone and ingests a finished
// one. done is false while the session is still starting or running; the
// next Tick looks again. Nothing here blocks on the session.
func (s *Scheduler) driveSession(ctx context.Context, env stagecode.Env, r stagecode.Request) (stagecode.Result, bool, error) {
	now := s.d.Clock.Now()
	p, err := recover.Inspect(r.RunDir, now)
	if err != nil {
		return stagecode.Result{}, false, err
	}
	switch p.Phase {
	case recover.NotStarted:
		if _, err := os.Stat(filepath.Join(r.RunDir, "result.json")); err == nil {
			return env.Collect(r, nil, false), true, nil // ended before the lock protocol existed for it
		}
		return s.startSession(ctx, env, r, 1)
	case recover.Starting:
		return stagecode.Result{}, false, nil
	case recover.StartFailed:
		if p.Attempt >= recover.MaxStartAttempts {
			return stagecode.Result{Event: model.Event{Name: model.EventInfra, Detail: "session failed to start twice"}}, true, nil
		}
		if err := recover.DropLock(r.RunDir); err != nil {
			return stagecode.Result{}, false, err
		}
		return s.startSession(ctx, env, r, p.Attempt+1)
	case recover.Alive:
		if err := s.recordPid(ctx, r.RunID, p.Pid); err != nil {
			return stagecode.Result{}, false, err
		}
		timeout := env.Timeout
		if timeout == 0 {
			timeout = stagecode.DefaultTimeout
		}
		if p.Since.IsZero() || now.Sub(p.Since) < timeout {
			return stagecode.Result{}, false, nil
		}
		if _, err := launch.KillRun(r.RunDir); err != nil {
			return stagecode.Result{}, false, err
		}
		return env.Collect(r, nil, true), true, nil
	}
	// Exited: the process is gone (or its pid was recycled). Sweep strays that
	// escaped the session group, then ingest result.json or report infra.
	if err := s.recordPid(ctx, r.RunID, p.Pid); err != nil {
		return stagecode.Result{}, false, err
	}
	if _, err := launch.KillRun(r.RunDir); err != nil {
		return stagecode.Result{}, false, err
	}
	var exit *launch.Exit
	if h := s.takeProc(r.RunID); h != nil {
		<-h.Done() // already exited: waiting only lets the reaper finish
		e := h.Exit()
		exit = &e
	}
	return env.Collect(r, exit, false), true, nil
}

// startSession takes launch.lock (O_EXCL), starts the launcher in its own
// session and, once pid.json exists, records the pid in the Run.
func (s *Scheduler) startSession(ctx context.Context, env stagecode.Env, r stagecode.Request, attempt int) (stagecode.Result, bool, error) {
	prep, err := env.Prepare(r)
	if err != nil {
		return stagecode.Result{}, false, err
	}
	if err := recover.TakeLock(r.RunDir, s.d.Clock.Now(), attempt); err != nil {
		if errors.Is(err, recover.ErrLockExists) {
			return stagecode.Result{}, false, nil // another start is in flight
		}
		return stagecode.Result{}, false, err
	}
	h, err := env.Launcher.StartDetached(prep.Policy, prep.Inv.Argv, launch.Detached{
		RunDir: r.RunDir, Exe: s.d.SessionExe, Stdin: prep.Inv.Stdin, ExtraEnv: prep.Inv.Env,
		StdoutPath: filepath.Join(r.RunDir, "stdout.log"), StderrPath: filepath.Join(r.RunDir, "stderr.log"),
	})
	if err != nil {
		_ = recover.DropLock(r.RunDir)
		return stagecode.Result{}, false, err
	}
	s.mu.Lock()
	s.procs[r.RunID] = h
	s.mu.Unlock()
	pf, ok, err := launch.ReadPidFile(r.RunDir)
	if err != nil || !ok {
		return stagecode.Result{}, false, err
	}
	return stagecode.Result{}, false, s.recordPid(ctx, r.RunID, pf)
}

func (s *Scheduler) takeProc(runID string) *launch.Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.procs[runID]
	delete(s.procs, runID)
	return h
}

// WaitSessions blocks until every session this Scheduler started has exited.
// Tests use it to wait for a real process exit between Ticks without sleeping;
// a restarted daemon has no handle on sessions its predecessor started and
// relies on pid.json probing instead.
func (s *Scheduler) WaitSessions() {
	s.mu.Lock()
	hs := make([]*launch.Handle, 0, len(s.procs))
	for _, h := range s.procs {
		hs = append(hs, h)
	}
	s.mu.Unlock()
	for _, h := range hs {
		<-h.Done()
	}
}

// recordPid stores the session's pid and start time in the Run (again after a
// relaunch, a no-op while they are unchanged); the
// launcher wrote them to pid.json before exec (reconciler: only then does
// foremand put the pid in the Run).
func (s *Scheduler) recordPid(ctx context.Context, runID string, pf launch.PidFile) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET pid = ?, pid_start = ? WHERE id = ? AND (pid IS NOT ? OR pid_start IS NOT ?)`,
			pf.Pid, pf.StartTime, runID, pf.Pid, pf.StartTime)
		return err
	})
}

// holdRun keeps the lease of a step's current Run and persists its items in
// runs.reserved, from which a restarted foremand rebuilds the ledger (RT-5).
func (s *Scheduler) holdRun(ctx context.Context, stepID string, l *gate.Lease) error {
	s.hold(stepID, l)
	run, ok, err := s.currentRun(ctx, stepID)
	if err != nil || !ok {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET reserved = ? WHERE id = ?`, recover.ReservedJSON(l.Items()), run.ID)
		return err
	})
}

// AdoptLeases hands over the leases recover.Startup re-adopted, by step id.
func (s *Scheduler) AdoptLeases(leases map[string]*gate.Lease) {
	for id, l := range leases {
		s.hold(id, l)
	}
}
