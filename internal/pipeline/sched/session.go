package sched

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// driveSession is the reconciler protocol for running(code|review)
// (state-machine.yaml reconciler): it looks at the run directory, starts the
// session when there is none, leaves a live one alone and ingests a finished
// one. done is false while the session is still starting or running; the
// next Tick looks again. Nothing here blocks on the session.
func (s *Scheduler) driveSession(ctx context.Context, stage SessionStage, job SessionJob) (ev model.Event, done bool, err error) {
	now := s.d.Clock.Now()
	p, err := recover.Inspect(s.d.Procs, job.RunDir, now)
	if err != nil {
		return model.Event{}, false, err
	}
	switch p.Phase {
	case recover.NotStarted:
		if _, err := os.Stat(filepath.Join(job.RunDir, "result.json")); err == nil {
			ev, err := stage.Collect(ctx, job, nil, false) // ended before the lock protocol existed for it
			return ev, err == nil, err
		}
		return s.startSession(ctx, stage, job, 1)
	case recover.Starting:
		return model.Event{}, false, nil
	case recover.StartFailed:
		if p.Attempt >= recover.MaxStartAttempts {
			return model.Event{Name: model.EventInfra, Detail: "session failed to start twice"}, true, nil
		}
		if err := recover.DropLock(s.d.Procs, job.RunDir); err != nil {
			return model.Event{}, false, err
		}
		return s.startSession(ctx, stage, job, p.Attempt+1)
	case recover.Alive:
		return s.watchSession(ctx, stage, job, p, now)
	}
	return s.collectExited(ctx, stage, job, p)
}

// watchSession leaves a live session alone until its timeout, then kills it
// and ingests what it left.
func (s *Scheduler) watchSession(ctx context.Context, stage SessionStage, job SessionJob, p recover.Probe, now time.Time) (model.Event, bool, error) {
	if err := s.recordPid(ctx, job.RunID, p.Pid); err != nil {
		return model.Event{}, false, err
	}
	if p.Since.IsZero() || now.Sub(p.Since) < stage.Timeout() {
		return model.Event{}, false, nil
	}
	if _, err := s.d.Procs.KillRun(job.RunDir); err != nil {
		return model.Event{}, false, err
	}
	ev, err := stage.Collect(ctx, job, nil, true)
	return ev, err == nil, err
}

// collectExited: the process is gone (or its pid was recycled). Sweep strays
// that escaped the session group, then ingest result.json or report infra.
func (s *Scheduler) collectExited(ctx context.Context, stage SessionStage, job SessionJob, p recover.Probe) (model.Event, bool, error) {
	if err := s.recordPid(ctx, job.RunID, p.Pid); err != nil {
		return model.Event{}, false, err
	}
	if _, err := s.d.Procs.KillRun(job.RunDir); err != nil {
		return model.Event{}, false, err
	}
	var exited Proc
	if h := s.takeProc(job.RunID); h != nil {
		<-h.Done() // already exited: waiting only lets the reaper finish
		exited = h
	}
	ev, err := stage.Collect(ctx, job, exited, false)
	return ev, err == nil, err
}

// startSession takes launch.lock (O_EXCL), starts the session through the
// stage and, once pid.json exists, records the pid in the Run.
func (s *Scheduler) startSession(ctx context.Context, stage SessionStage, job SessionJob, attempt int) (model.Event, bool, error) {
	locked := false
	h, err := stage.Start(ctx, job, func() error {
		if err := recover.TakeLock(job.RunDir, s.d.Clock.Now(), attempt); err != nil {
			return err
		}
		locked = true
		return nil
	})
	if err != nil {
		if locked {
			_ = recover.DropLock(s.d.Procs, job.RunDir) // the launch itself failed
		}
		if errors.Is(err, recover.ErrLockExists) {
			return model.Event{}, false, nil // another start is in flight
		}
		return model.Event{}, false, err
	}
	s.mu.Lock()
	s.procs[job.RunID] = h
	s.mu.Unlock()
	pf, ok, err := s.d.Procs.ReadPid(job.RunDir)
	if err != nil || !ok {
		return model.Event{}, false, err
	}
	return model.Event{}, false, s.recordPid(ctx, job.RunID, pf)
}

func (s *Scheduler) takeProc(runID string) Proc {
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
	hs := make([]Proc, 0, len(s.procs))
	for _, h := range s.procs {
		hs = append(hs, h)
	}
	s.mu.Unlock()
	for _, h := range hs {
		<-h.Done()
	}
}

// recordPid stores the session's pid and start time in the Run (again after a
// relaunch, a no-op while they are unchanged); the launcher wrote them to
// pid.json before exec (reconciler: only then does foremand put the pid in
// the Run).
func (s *Scheduler) recordPid(ctx context.Context, runID string, pf recover.PidFile) error {
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
