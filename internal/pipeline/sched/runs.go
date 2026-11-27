package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"

	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// markRunning moves a pending Run to running (the reconciler handshake of
// E-RUN.status). The pid and start time follow once the launcher wrote
// pid.json (recordPid).
func (s *Scheduler) markRunning(ctx context.Context, runID string) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'running' WHERE id = ? AND status = 'pending'`, runID)
		return err
	})
}

// outcomeOf maps an event to E-RUN.outcome.
func outcomeOf(name string) string {
	switch name {
	case model.EventCheckFail, model.EventL1Fail:
		return "check_failed"
	case model.EventReviewChanges:
		return "review_changes"
	case model.EventSpecConflict:
		return "spec_conflict"
	case model.EventInfra:
		return "infra"
	case model.EventBadResult:
		return "bad_result"
	}
	return "ok"
}

// endRun closes a Run once its event was applied (PP-3: a Run is ended once;
// the status filter makes a repeat a no-op).
func (s *Scheduler) endRun(ctx context.Context, runID string, ev model.Event) error {
	usage, err := json.Marshal(map[string]int64{"input": ev.Usage.Input, "output": ev.Usage.Output})
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'ended', ended_at = ?, outcome = ?, usage = ?, reserved = '[]'
			WHERE id = ? AND status IN ('pending', 'running')`,
			s.d.Clock.Now().UTC().Format(timeLayout), outcomeOf(ev.Name), string(usage), runID)
		return err
	})
}

// ensureSessionRepo creates the requirement's session repo from the mirror on
// first use (D-045) and records it.
func (s *Scheduler) ensureSessionRepo(ctx context.Context, req reqRow) (string, error) {
	if req.SessionRepo != "" {
		return req.SessionRepo, nil
	}
	if err := s.d.Mirror.Update(ctx); err != nil {
		return "", err
	}
	dir := filepath.Join(s.d.Home, "projects", s.d.Project, "sessions", req.ID)
	repo, err := session.Create(ctx, s.d.Git, s.d.Mirror, dir, session.Spec{ReqID: req.ID, BaseBranch: s.d.Integ.Default,
		Identity: s.d.SessionIdent})
	if err != nil {
		return "", err
	}
	err = s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE requirements SET session_repo = ?, session_repo_state = 'active' WHERE id = ?`, repo.Dir, req.ID)
		return err
	})
	return repo.Dir, err
}

// loadPR returns the PR number and head sha of a requirement (0, "" if none).
func (s *Scheduler) loadPR(ctx context.Context, reqID string) (int, string, error) {
	var n sql.NullInt64
	var head sql.NullString
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT number, head_sha FROM pull_requests WHERE req_id = ?`, reqID).Scan(&n, &head)
	})
	if err == sql.ErrNoRows {
		return 0, "", nil
	}
	return int(n.Int64), head.String, err
}

// savePR upserts the E-PR row (one PR per requirement, D-042).
func (s *Scheduler) savePR(ctx context.Context, req reqRow, number int, head string) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO pull_requests (req_id, number, head_branch, head_sha, state)
			VALUES (?, ?, ?, ?, 'open')
			ON CONFLICT (req_id) DO UPDATE SET number = excluded.number, head_sha = excluded.head_sha,
				state = CASE WHEN state = 'merged' THEN state ELSE 'open' END`, req.ID, number, req.Branch, head)
		return err
	})
}
