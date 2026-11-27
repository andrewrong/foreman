// Package sched is the scheduler and reconciler driver. Tick does one round:
// wake waiting steps, dispatch queued steps (Gate lease first, then the
// dispatch Transition, RC-2 order), then run the reconciler action of every
// running step (state-machine.yaml reconciler) by calling the stage packages
// and handing the event value they return to state.Transition (MD-7, TX-1).
// There are no goroutines and no timers inside: callers drive Tick, tests
// with a fake clock.
package sched

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/store/sqlite"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// Deps is everything a Scheduler works with; the composition root builds it.
type Deps struct {
	DB      *sqlite.DB // work.db
	Engine  *state.Engine
	Clock   clock.Clock
	Gate    *gate.Gate
	Meters  *meter.Store
	Home    string // FOREMAN_HOME, absolute and short (RunTmp <= 70 bytes)
	Project string

	Repo   ops.Repo
	Ops    *ops.Ops
	Git    *wsgit.Runner
	Mirror *session.Mirror
	Integ  *integrate.Dir

	SessionIdent wsgit.Identity
	Launcher     launch.Launcher
	SessionExe   string        // foreman binary serving launch.FlagRunSession; empty: os.Args[0]
	Policy       policy.Inputs // run-independent sandbox inputs
	// NewAdapter builds the harness adapter of a Run with its lease (MD-3).
	NewAdapter func(role api.Role, lease *gate.Lease) (api.Adapter, error)
	// RunExtra gives adapter-specific settings of a Run (scripted: script path).
	RunExtra func(role api.Role, stepID string) map[string]string
	// L0 are the project's L0 commands (argv form).
	L0           [][]string
	Constitution string

	// OnTransition, if set, sees every applied Transition (tests log RowIDs).
	OnTransition func(stepID string, ev state.Event, out state.Outcome)
}

// Scheduler drives steps through the state machine.
type Scheduler struct {
	d Deps

	mu     sync.Mutex
	leases map[string]*gate.Lease    // step id -> lease held while the step runs
	procs  map[string]*launch.Handle // run id -> session started by this process
}

// New validates deps and returns a Scheduler.
func New(d Deps) (*Scheduler, error) {
	var missing []string
	for name, ok := range map[string]bool{
		"DB": d.DB != nil, "Engine": d.Engine != nil, "Clock": d.Clock != nil, "Gate": d.Gate != nil,
		"Meters": d.Meters != nil, "Home": d.Home != "", "Ops": d.Ops != nil, "Git": d.Git != nil,
		"Mirror": d.Mirror != nil, "Integ": d.Integ != nil, "NewAdapter": d.NewAdapter != nil,
	} {
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("sched: missing dependencies: %s", strings.Join(sortedStrings(missing), ", "))
	}
	return &Scheduler{d: d, leases: map[string]*gate.Lease{}, procs: map[string]*launch.Handle{}}, nil
}

// Tick does one scheduling round and reports whether any Transition was
// applied or any stage action ran.
func (s *Scheduler) Tick(ctx context.Context) (bool, error) {
	// FG-2: GitHub readings go stale after 10 minutes and the Gate then
	// answers "stale" to every non-P0 call; refresh them here (a no-op while
	// fresh). A failed probe is not fatal: the Gate decides from what it has.
	_ = s.d.Ops.EnsureFresh(ctx)
	progressed := false
	for _, phase := range []func(context.Context) (bool, error){s.wake, s.dispatch, s.reconcile} {
		p, err := phase(ctx)
		progressed = progressed || p
		if err != nil {
			return progressed, err
		}
	}
	return progressed, nil
}

// apply calls state.Transition and reports it to the observer.
func (s *Scheduler) apply(ctx context.Context, stepID string, ev state.Event) (state.Outcome, error) {
	out, err := s.d.Engine.Transition(ctx, s.d.DB, stepID, ev)
	if err != nil {
		return out, fmt.Errorf("sched: transition %s on %s: %w", ev.Name, stepID, err)
	}
	if s.d.OnTransition != nil {
		s.d.OnTransition(stepID, ev, out)
	}
	return out, nil
}

// wake sends the exit events of waiting steps whose condition holds:
// deps_done (S10) and deadline_passed for quota and infra waits (S13, S14).
func (s *Scheduler) wake(ctx context.Context) (bool, error) {
	steps, err := s.stepsIn(ctx, "waiting")
	if err != nil {
		return false, err
	}
	progressed := false
	now := s.d.Clock.Now()
	for _, st := range steps {
		var ev state.Event
		switch st.Reason {
		case "deps":
			all, err := s.stepsOf(ctx, st.ReqID)
			if err != nil {
				return progressed, err
			}
			if !depsDone(st, all) {
				continue
			}
			ev = state.Event{Name: "deps_done"}
		case "quota", "infra":
			if st.Deadline.IsZero() || now.Before(st.Deadline) {
				continue
			}
			ev = state.Event{Name: "deadline_passed"}
		default:
			continue // ci is handled by reconcile; the other reasons are out of M1 scope
		}
		out, err := s.apply(ctx, st.ID, ev)
		if err != nil {
			return progressed, err
		}
		progressed = progressed || !out.Ignored
	}
	return progressed, nil
}

// dispatch evaluates the guards of every queued step and sends the dispatch
// event. A lease is requested only when hold, budget and deps already allow
// the dispatch, and released again when the Transition does not start work
// that needs it (RC-2).
func (s *Scheduler) dispatch(ctx context.Context) (bool, error) {
	steps, err := s.stepsIn(ctx, "queued")
	if err != nil {
		return false, err
	}
	progressed := false
	for _, st := range steps {
		req, err := s.loadReq(ctx, st.ReqID)
		if err != nil {
			return progressed, err
		}
		all, err := s.stepsOf(ctx, st.ReqID)
		if err != nil {
			return progressed, err
		}
		over, err := s.overBudget(ctx, req)
		if err != nil {
			return progressed, err
		}
		ev := state.Event{Name: "dispatch", Deps: "pending", Budget: "ok"}
		if over {
			ev.Budget = "over"
		}
		if depsDone(st, all) {
			ev.Deps = "done"
		}
		var lease *gate.Lease
		if req.Hold == state.HoldNone && !over && ev.Deps == "done" {
			if st.Stage == "merge" {
				s.refreshBase(ctx, req, st)
			}
			dec, err := s.d.Gate.Acquire(ctx, s.demand(st, req))
			if err != nil {
				return progressed, err
			}
			if dec.Allowed() {
				ev.Lease, lease = "ok", dec.Lease
			} else {
				ev.Lease, ev.ResetsAt = "wait", dec.Wait.Until
			}
		}
		out, err := s.apply(ctx, st.ID, ev)
		if err != nil {
			s.release(lease)
			return progressed, err
		}
		progressed = progressed || !out.Ignored
		if lease != nil {
			if out.To.State == "running" {
				if err := s.holdRun(ctx, st.ID, lease); err != nil {
					return progressed, err
				}
			} else {
				s.release(lease) // S07 starts waiting(ci): forge ops take their own leases
			}
		}
	}
	return progressed, nil
}

// refreshBase polls the repository once so that the base-health reading the
// merge lease depends on is fresh. RES-BASE is not passive: a missing or
// stale reading makes the Gate answer "unmeasured" to every non-P0 merge, and
// the daemon's periodic batch poll (FG-7) does not exist yet. A failed poll
// is not fatal: the Gate then decides from what it has.
func (s *Scheduler) refreshBase(ctx context.Context, req reqRow, st stepRow) {
	_, _ = s.d.Ops.Poll(ctx, s.opsCall(req, st), ops.PollInput{Repo: s.d.Repo, BaseBranch: s.d.Integ.Default, Active: true})
}

func (s *Scheduler) hold(stepID string, l *gate.Lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[stepID] = l
}

// takeLease removes and returns the lease held for a step.
func (s *Scheduler) takeLease(stepID string) *gate.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[stepID]
	delete(s.leases, stepID)
	return l
}

func (s *Scheduler) peekLease(stepID string) *gate.Lease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leases[stepID]
}

func (s *Scheduler) release(l *gate.Lease) {
	if l != nil {
		_ = l.Release()
	}
}

// settle closes a lease whose Run ended; slots are not consumed amounts, so
// nothing is reported as actual use (the reservation goes back in full).
func (s *Scheduler) settle(ctx context.Context, l *gate.Lease) error {
	if l == nil {
		return nil
	}
	if _, err := l.Settle(ctx, nil); err != nil && !errors.Is(err, gate.ErrClosed) {
		return err
	}
	return nil
}

func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
