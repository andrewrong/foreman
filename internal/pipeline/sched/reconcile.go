package sched

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// reconcile runs the reconciler action of every step that has one:
// running(code|check|review|merge) and waiting(ci).
func (s *Scheduler) reconcile(ctx context.Context) (bool, error) {
	var todo []stepRow
	running, err := s.stepsIn(ctx, "running")
	if err != nil {
		return false, err
	}
	todo = append(todo, running...)
	waiting, err := s.stepsIn(ctx, "waiting")
	if err != nil {
		return false, err
	}
	for _, st := range waiting {
		if st.Reason == "ci" && st.Type == "integrate" && st.Stage == "check" {
			todo = append(todo, st)
		}
	}
	progressed := false
	for _, st := range todo {
		did, err := s.reconcileStep(ctx, st)
		progressed = progressed || did
		if err != nil {
			return progressed, err
		}
	}
	return progressed, nil
}

func (s *Scheduler) reconcileStep(ctx context.Context, st stepRow) (bool, error) {
	run, ok, err := s.currentRun(ctx, st.ID)
	if err != nil || !ok {
		return false, err
	}
	req, err := s.loadReq(ctx, st.ReqID)
	if err != nil {
		return false, err
	}
	if err := s.markRunning(ctx, run.ID); err != nil {
		return false, err
	}
	ev, err := s.execute(ctx, st, req, run)
	if err != nil {
		return true, fmt.Errorf("sched: %s of %s: %w", stageRole(st.Type, st.Stage), st.ID, err)
	}
	if ev == nil { // L1 still running, or the Gate asked to wait: try again next Tick
		return true, nil
	}
	if err := s.recordFacts(ctx, st, *ev); err != nil {
		return true, err
	}
	all, err := s.stepsOf(ctx, st.ReqID)
	if err != nil {
		return true, err
	}
	out, err := s.apply(ctx, st.ID, s.stateEvent(*ev, singleStep(all)))
	if err != nil {
		return true, err
	}
	if staysPut(*ev, st, out) {
		// S37, S38, S45, S48: the deadline was extended and the step keeps its
		// state; the Run and the lease it holds (the merge slot) go on.
		return true, nil
	}
	if err := s.endRun(ctx, run.ID, *ev); err != nil {
		return true, err
	}
	if lease := s.takeLease(st.ID); lease != nil {
		if err := s.settle(ctx, lease); err != nil {
			return true, err
		}
	}
	// D-086: the results are committed (the Transition and endRun above), so
	// the run directory is garbage now. A failure here leaves a directory the
	// startup sweep removes.
	if err := recover.RemoveRunDir(s.d.Home, run.ID); err != nil {
		return true, fmt.Errorf("sched: remove run dir of %s: %w", run.ID, err)
	}
	return true, nil
}

// staysPut reports whether an event left the step where it was: the deadline
// extensions ci_progress and lease_wait below their cap.
func staysPut(ev model.Event, st stepRow, out state.Outcome) bool {
	if ev.Name != model.EventCIProgress && ev.Name != model.EventLeaseWait {
		return false
	}
	return out.To.State == st.State && out.To.Stage == st.Stage && out.To.Reason == st.Reason
}

// execute performs the stage action of a step. A nil event means "nothing to
// report yet".
func (s *Scheduler) execute(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	switch {
	case st.State == "waiting": // integrate, waiting(ci)
		return s.runIntegrate(ctx, st, req)
	case st.Stage == "merge":
		return s.runMerge(ctx, st, req)
	case st.Stage == "check":
		return s.runCheck(ctx, st, req, run)
	case st.Stage == "code":
		return s.runCode(ctx, st, req, run)
	case st.Stage == "review":
		return s.runReview(ctx, st, req, run)
	}
	return nil, fmt.Errorf("no reconciler action for running(%s) of type %s", st.Stage, st.Type)
}

func (s *Scheduler) runDir(runID string) string { return filepath.Join(s.d.Home, "runs", runID) }

// leaseFor returns the lease held since dispatch; after a restart none is
// held and a fresh one is requested (nil lease: the Gate says wait).
func (s *Scheduler) leaseFor(ctx context.Context, st stepRow, req reqRow) (*gate.Lease, error) {
	if l := s.peekLease(st.ID); l != nil {
		return l, nil
	}
	dec, err := s.d.Gate.Acquire(ctx, s.demand(st, req))
	if err != nil || !dec.Allowed() {
		return nil, err
	}
	if err := s.holdRun(ctx, st.ID, dec.Lease); err != nil {
		return nil, err
	}
	return dec.Lease, nil
}

// sessionJob builds the Run of a session stage.
func (s *Scheduler) sessionJob(ctx context.Context, st stepRow, req reqRow, run runRow, role, repo string, lease *gate.Lease) (SessionJob, error) {
	brief, err := s.contextInput(ctx, st, req, role)
	if err != nil {
		return SessionJob{}, err
	}
	return SessionJob{RunID: run.ID, StepID: st.ID, Role: role, Model: run.Model, RunDir: s.runDir(run.ID),
		SessionRepo: repo, Acceptance: st.Acceptance, Brief: brief, Lease: lease}, nil
}

func (s *Scheduler) runCode(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	lease, err := s.leaseFor(ctx, st, req)
	if err != nil || lease == nil {
		return nil, err
	}
	repo, err := s.ensureSessionRepo(ctx, req)
	if err != nil {
		return nil, err
	}
	job, err := s.sessionJob(ctx, st, req, run, RoleCoder, repo, lease)
	if err != nil {
		return nil, err
	}
	ev, done, err := s.driveSession(ctx, s.d.Code, job)
	if err != nil || !done {
		return nil, err // session starting or running: look again next Tick
	}
	return &ev, nil
}

func (s *Scheduler) runReview(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	lease, err := s.leaseFor(ctx, st, req)
	if err != nil || lease == nil {
		return nil, err
	}
	job, err := s.sessionJob(ctx, st, req, run, RoleReviewer, req.SessionRepo, lease)
	if err != nil {
		return nil, err
	}
	if st.Type != "integrate" {
		job.MergeBack = &MergeBack{ReqID: req.ID, StepID: st.ID, SessionRepo: req.SessionRepo,
			SessionBranch: req.Branch, Subject: firstLine(st.Goal)}
	}
	ev, done, err := s.driveSession(ctx, s.d.Review, job)
	if err != nil || !done {
		return nil, err
	}
	return &ev, nil
}

func (s *Scheduler) runCheck(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	if lease, err := s.leaseFor(ctx, st, req); err != nil || lease == nil {
		return nil, err // Gate wait: try again next Tick
	}
	ev, err := s.d.Check.Run(ctx, CheckJob{RunID: run.ID, StepID: st.ID, SessionRepo: req.SessionRepo, SessionBranch: req.Branch,
		RunDir: s.runDir(run.ID)})
	return &ev, err
}

func (s *Scheduler) runIntegrate(ctx context.Context, st stepRow, req reqRow) (*model.Event, error) {
	row, err := s.loadPRRow(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	res, err := s.d.Integrate.Run(ctx, IntegrateJob{ReqID: req.ID, Title: req.Spec.Title, Body: req.Spec.Goal, PR: row.Number,
		Call: s.opsCall(req, st), DeadlinePassed: s.deadlinePassed(st), Reruns: row.Reruns,
		MarkRerun: func(ctx context.Context, head string) error { return s.markRerun(ctx, req.ID, head) }})
	if err != nil {
		return nil, err
	}
	if res.PR > 0 {
		if err := s.savePR(ctx, req, res.PR, res.Head); err != nil {
			return nil, err
		}
	}
	return res.Event, nil
}

func (s *Scheduler) runMerge(ctx context.Context, st stepRow, req reqRow) (*model.Event, error) {
	if lease, err := s.leaseFor(ctx, st, req); err != nil || lease == nil {
		return nil, err // no merge slot / base health yet: never merge without them
	}
	row, err := s.loadPRRow(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if row.Number == 0 {
		return nil, fmt.Errorf("integrate step %s reached merge without a pull request", st.ID)
	}
	res, err := s.d.Merge.Run(ctx, MergeJob{ReqID: req.ID, PR: row.Number, Head: row.Head, Call: s.opsCall(req, st),
		LastReviewed: row.LastReviewed, GuardrailApproved: row.GuardrailApproved, Reruns: row.Reruns,
		MarkRerun:      func(ctx context.Context, head string) error { return s.markRerun(ctx, req.ID, head) },
		DeadlinePassed: s.deadlinePassed(st)})
	// The head moves under update-branch; the merge pins it and S43 reads it
	// back as last_reviewed_sha, so it is stored before any event is applied.
	if res.Head != "" && res.Head != row.Head {
		if serr := s.saveHead(ctx, req.ID, res.Head); serr != nil {
			return nil, serr
		}
	}
	return res.Event, err
}

// deadlinePassed: the deadline of the step's current state has passed.
func (s *Scheduler) deadlinePassed(st stepRow) bool {
	return !st.Deadline.IsZero() && !s.d.Clock.Now().Before(st.Deadline)
}

// stateEvent converts a stage event value into the Transition input.
func (s *Scheduler) stateEvent(ev model.Event, single string) state.Event {
	out := state.Event{Name: ev.Name, Head: ev.Head, Detail: ev.Detail, FailingTests: ev.FailingTests, LogTail: ev.LogTail}
	if ev.Name == model.EventGuardrailHit {
		out.Ask = &state.AskContent{HItem: "H6", What: "The pull request changes guardrail paths and needs your approval before it is merged",
			Changes: "Granting approves this head only; a new head asks again", Checked: ev.Detail}
	}
	if ev.Name == model.EventL1Pass {
		out.SingleStep = single
	}
	for _, f := range ev.Findings {
		out.Findings = append(out.Findings, state.FindingIn{Severity: f.Severity, Path: f.Path, Line: f.Line,
			Problem: f.Problem, CloseWhen: f.CloseWhen})
	}
	return out
}

// recordFacts stores what an event learned in rows Transition does not own:
// the pull request, the squashed commit of a step, the last L0 sha.
func (s *Scheduler) recordFacts(ctx context.Context, st stepRow, ev model.Event) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		switch ev.Name {
		case model.EventCheckPass:
			_, err := tx.ExecContext(ctx, `UPDATE requirements SET last_l0_sha = ? WHERE id = ?`, ev.Head, st.ReqID)
			return err
		case model.EventReviewApprove:
			if st.Type == "integrate" {
				return nil
			}
			_, err := tx.ExecContext(ctx, `UPDATE steps SET commit_sha = ? WHERE id = ?`, ev.Head, st.ID)
			return err
		case model.EventMerged:
			_, err := tx.ExecContext(ctx, `UPDATE pull_requests SET state = 'merged' WHERE req_id = ?`, st.ReqID)
			return err
		}
		return nil
	})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
