package sched

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	stagecheck "github.com/andrewrong/foreman/internal/stage/check"
	stagecode "github.com/andrewrong/foreman/internal/stage/code"
	stageintegrate "github.com/andrewrong/foreman/internal/stage/integrate"
	stagemerge "github.com/andrewrong/foreman/internal/stage/merge"
	stagereview "github.com/andrewrong/foreman/internal/stage/review"
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
	if _, err := s.apply(ctx, st.ID, s.stateEvent(*ev, singleStep(all))); err != nil {
		return true, err
	}
	if err := s.endRun(ctx, run.ID, *ev); err != nil {
		return true, err
	}
	if lease := s.takeLease(st.ID); lease != nil {
		if err := s.settle(ctx, lease); err != nil {
			return true, err
		}
	}
	return true, nil
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
	s.hold(st.ID, dec.Lease)
	return dec.Lease, nil
}

func (s *Scheduler) sessionEnv(role api.Role, lease *gate.Lease) (stagecode.Env, error) {
	ad, err := s.d.NewAdapter(role, lease)
	if err != nil {
		return stagecode.Env{}, err
	}
	return stagecode.Env{Launcher: s.d.Launcher, Adapter: ad, Policy: s.d.Policy}, nil
}

func (s *Scheduler) sessionRequest(ctx context.Context, st stepRow, req reqRow, run runRow, role api.Role, repo string) (stagecode.Request, error) {
	in, err := s.contextInput(ctx, st, req, string(role))
	if err != nil {
		return stagecode.Request{}, err
	}
	prompt := coderPrompt
	if role == api.RoleReviewer {
		prompt = reviewerPrompt
	}
	return stagecode.Request{RunID: run.ID, StepID: st.ID, Role: role, Model: run.Model, RunDir: s.runDir(run.ID),
		SessionRepo: repo, Context: renderContext(in), Prompt: prompt, AcceptanceIDs: st.Acceptance,
		Extra: s.extra(role, st.ID)}, nil
}

func (s *Scheduler) extra(role api.Role, stepID string) map[string]string {
	if s.d.RunExtra == nil {
		return nil
	}
	return s.d.RunExtra(role, stepID)
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
	env, err := s.sessionEnv(api.RoleCoder, lease)
	if err != nil {
		return nil, err
	}
	r, err := s.sessionRequest(ctx, st, req, run, api.RoleCoder, repo)
	if err != nil {
		return nil, err
	}
	ev, err := stagecode.Run(ctx, env, r)
	return &ev, err
}

func (s *Scheduler) runReview(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	lease, err := s.leaseFor(ctx, st, req)
	if err != nil || lease == nil {
		return nil, err
	}
	env, err := s.sessionEnv(api.RoleReviewer, lease)
	if err != nil {
		return nil, err
	}
	r, err := s.sessionRequest(ctx, st, req, run, api.RoleReviewer, req.SessionRepo)
	if err != nil {
		return nil, err
	}
	rr := stagereview.Request{Session: r}
	if st.Type != "integrate" {
		rr.MergeBack = &stagereview.MergeBack{ReqID: req.ID, StepID: st.ID, SessionRepo: req.SessionRepo,
			SessionBranch: req.Branch, Subject: firstLine(st.Goal)}
	}
	ev, err := stagereview.Run(ctx, stagereview.Env{Session: env, Integ: s.d.Integ, Mirror: s.d.Mirror}, rr)
	return &ev, err
}

func (s *Scheduler) runCheck(ctx context.Context, st stepRow, req reqRow, run runRow) (*model.Event, error) {
	if lease, err := s.leaseFor(ctx, st, req); err != nil || lease == nil {
		return nil, err // Gate wait: try again next Tick
	}
	ev, err := stagecheck.Run(ctx, stagecheck.Env{Integ: s.d.Integ, Launcher: s.d.Launcher, Policy: s.checkPolicy()},
		stagecheck.Request{RunID: run.ID, StepID: st.ID, SessionRepo: req.SessionRepo, SessionBranch: req.Branch,
			RunDir: s.runDir(run.ID), Commands: s.d.L0})
	return &ev, err
}

// checkPolicy is the sandbox policy of L0. The working directory is a
// throwaway checkout under the Run's directory, so the deny-write list is the
// session's own, integrate/ included: L0 runs AI-written code and must not be
// able to alter the directory foremand runs git in.
func (s *Scheduler) checkPolicy() policy.Inputs { return s.d.Policy }

func (s *Scheduler) runIntegrate(ctx context.Context, st stepRow, req reqRow) (*model.Event, error) {
	pr, _, err := s.loadPR(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	call := s.opsCall(req, st)
	res, err := stageintegrate.Run(ctx, stageintegrate.Env{Integ: s.d.Integ, Ops: s.d.Ops, Repo: s.d.Repo},
		stageintegrate.Request{ReqID: req.ID, Title: req.Spec.Title, Body: req.Spec.Goal, PR: pr, Priority: call.Priority,
			Caller: call.Caller, DeadlinePassed: !st.Deadline.IsZero() && !s.d.Clock.Now().Before(st.Deadline)})
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
	pr, head, err := s.loadPR(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if pr == 0 {
		return nil, fmt.Errorf("integrate step %s reached merge without a pull request", st.ID)
	}
	call := s.opsCall(req, st)
	return stagemerge.Run(ctx, stagemerge.Env{Ops: s.d.Ops, Repo: s.d.Repo},
		stagemerge.Request{PR: pr, Head: head, Priority: call.Priority, Caller: call.Caller})
}

// stateEvent converts a stage event value into the Transition input.
func (s *Scheduler) stateEvent(ev model.Event, single string) state.Event {
	out := state.Event{Name: ev.Name, Head: ev.Head, Detail: ev.Detail, FailingTests: ev.FailingTests, LogTail: ev.LogTail}
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
