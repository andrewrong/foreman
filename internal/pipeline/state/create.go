package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// InsertRequirement stores a new requirement with the initial hold/counters
// of r. This and InsertStep are the creation entry points of the rows
// Transition later changes (C-01: the state columns are written only here and
// in Transition). r must be valid (internal/spec/validate).
func InsertRequirement(ctx context.Context, db *sqlite.DB, r model.Requirement) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		hold := string(r.Hold)
		if hold == "" {
			hold = HoldNone
		}
		status := string(r.Status)
		if status == "" {
			status = string(model.StatusDraft)
		}
		parked := 0
		if r.Parked {
			parked = 1
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO requirements (id, kind, source, priority, spec_version, spec,
			branch, parked, hold, req_counters, last_l0_sha, session_repo, base_fix_sha, status, size,
			budget_tokens, budget_quota_pct)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Kind, r.Source, r.Priority, r.SpecVersion, jsonText(r.Spec), r.Branch, parked, hold,
			jsonText(r.ReqCounters), nullStr(r.LastL0SHA), nullStr(r.SessionRepo), nil, status, r.Size,
			r.Budget.Tokens, r.Budget.QuotaPct)
		return err
	})
}

// InsertStep stores a new step. A zero State means queued at the first stage
// of its type with the queued deadline of clk (nil = real clock). The step's
// seq is the next free number of its requirement.
func InsertStep(ctx context.Context, db *sqlite.DB, clk clock.Clock, s model.Step) error {
	if clk == nil {
		clk = clock.Real()
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		state, stage := string(s.State), string(s.Stage)
		if state == "" {
			state = "queued"
		}
		if stage == "" {
			stage = firstStageOf(string(s.Type))
		}
		role := string(s.CodeRole)
		if role == "" {
			role = "coder"
		}
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM steps WHERE req_id = ?`, s.ReqID).Scan(&seq); err != nil {
			return err
		}
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("%s/S%d", s.ReqID, seq)
		}
		var deadline any
		if state == "queued" {
			deadline = fmtTime(clk.Now().Add(defaultQueued))
		}
		counters := map[string]int(s.Counters)
		if counters == nil {
			counters = map[string]int{}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO steps (id, req_id, seq, type, code_role, goal, acceptance,
			depends_on, touches, state, stage, resume_stage, reason, deadline, counters, commit_sha)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, s.ReqID, seq, s.Type, role, s.Goal, jsonText(nonNil(s.Acceptance)), jsonText(nonNil(s.DependsOn)),
			jsonText(nonNil(s.Touches)), state, stage, nullStr(string(s.ResumeStage)), nullStr(s.Reason),
			deadline, jsonText(counters), nullStr(s.Commit))
		return err
	})
}
