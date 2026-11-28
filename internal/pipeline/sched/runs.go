package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"

	"github.com/andrewrong/foreman/internal/spec/model"
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
	case model.EventInfra, model.EventDeadlinePassed:
		return "infra"
	case model.EventBadResult:
		return "bad_result"
	}
	return "ok"
}

// The token keys of runs.usage; usageTokensSQL sums them over the selected
// runs (the budget guard), so writer and reader cannot drift apart.
const (
	usageKeyInput  = "input_tokens"
	usageKeyOutput = "output_tokens"
	usageTokensSQL = `COALESCE(SUM(COALESCE(json_extract(usage, '$.` + usageKeyInput + `'), 0) + COALESCE(json_extract(usage, '$.` + usageKeyOutput + `'), 0)), 0)`
)

// usageJSON is the runs.usage column: NULL when the model reported nothing
// (unknown is not zero, D-091), otherwise the keys of entities.yaml E-RUN plus
// the turns and cost of the stream's result event.
func usageJSON(u model.Usage) (any, error) {
	if !u.Known {
		return nil, nil
	}
	raw, err := json.Marshal(map[string]any{usageKeyInput: u.Input, usageKeyOutput: u.Output,
		"cache_read_tokens": u.CacheRead, "cache_creation_tokens": u.CacheWrite, "turns": u.Turns, "cost_usd": u.CostUSD})
	return string(raw), err
}

// endRun closes a Run once its event was applied (PP-3: a Run is ended once;
// the status filter makes a repeat a no-op).
func (s *Scheduler) endRun(ctx context.Context, runID string, ev model.Event) error {
	usage, err := usageJSON(ev.Usage)
	if err != nil {
		return err
	}
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'ended', ended_at = ?, outcome = ?, usage = ?, reserved = '[]'
			WHERE id = ? AND status IN ('pending', 'running')`,
			s.d.Clock.Now().UTC().Format(timeLayout), outcomeOf(ev.Name), usage, runID)
		return err
	})
}

// ensureSessionRepo creates the requirement's session repo from the mirror on
// first use (D-045) and records it.
func (s *Scheduler) ensureSessionRepo(ctx context.Context, req reqRow) (string, error) {
	if req.SessionRepo != "" {
		return req.SessionRepo, nil
	}
	dir := filepath.Join(s.d.Home, "projects", s.d.Project, "sessions", req.ID)
	repo, err := s.d.Workspace.CreateSessionRepo(ctx, req.ID, dir)
	if err != nil {
		return "", err
	}
	err = s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE requirements SET session_repo = ?, session_repo_state = 'active' WHERE id = ?`, repo, req.ID)
		return err
	})
	return repo, err
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
