package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// timeLayout is the fixed-width format internal/pipeline/state writes.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// stepRow is the part of a step the scheduler reads. It never writes the
// state columns; only state.Transition does (C-01).
type stepRow struct {
	ID, ReqID, Type, Goal string
	Seq                   int
	State, Stage, Reason  string
	Acceptance, DependsOn []string
	Deadline              time.Time // zero when none
	LastReviewed          string
	FixFor                string // fix step: the integrate step it repairs
}

// reqRow is the part of a requirement the scheduler reads.
type reqRow struct {
	ID, Priority, Branch, Hold string
	SessionRepo                string
	Spec                       model.Spec
	BudgetTokens               int
	BudgetExtensions           int
}

// runRow is an E-RUN row.
type runRow struct {
	ID, Role, Status, Model string
}

const stepColumns = `id, req_id, seq, type, goal, state, stage, reason, acceptance, depends_on, deadline, last_reviewed_sha, fix_for`

func scanStepRow(sc interface{ Scan(...any) error }) (stepRow, error) {
	var s stepRow
	var reason, deadline, last, fixFor sql.NullString
	var acc, deps string
	if err := sc.Scan(&s.ID, &s.ReqID, &s.Seq, &s.Type, &s.Goal, &s.State, &s.Stage, &reason, &acc, &deps, &deadline, &last, &fixFor); err != nil {
		return s, err
	}
	s.Reason, s.LastReviewed, s.FixFor = reason.String, last.String, fixFor.String
	if deadline.Valid {
		t, err := time.Parse(timeLayout, deadline.String)
		if err != nil {
			return s, fmt.Errorf("sched: step %s deadline: %w", s.ID, err)
		}
		s.Deadline = t
	}
	if err := json.Unmarshal([]byte(acc), &s.Acceptance); err != nil {
		return s, err
	}
	return s, json.Unmarshal([]byte(deps), &s.DependsOn)
}

// stepsIn lists steps in the given state ordered by requirement priority,
// then requirement id and seq, so P0 work dispatches first.
func (s *Scheduler) stepsIn(ctx context.Context, state string) ([]stepRow, error) {
	var out []stepRow
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.id, s.req_id, s.seq, s.type, s.goal, s.state, s.stage, s.reason, s.acceptance, s.depends_on, s.deadline, s.last_reviewed_sha, s.fix_for
			FROM steps s JOIN requirements r ON r.id = s.req_id
			WHERE s.state = ? AND r.status = 'submitted' ORDER BY r.priority, s.req_id, s.seq`, state)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			st, err := scanStepRow(rows)
			if err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Scheduler) loadReq(ctx context.Context, id string) (reqRow, error) {
	var r reqRow
	var spec string
	var session sql.NullString
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id, priority, branch, hold, session_repo, spec, budget_tokens, budget_extensions
			FROM requirements WHERE id = ?`, id).Scan(&r.ID, &r.Priority, &r.Branch, &r.Hold, &session, &spec, &r.BudgetTokens, &r.BudgetExtensions)
	})
	if err != nil {
		return r, err
	}
	r.SessionRepo = session.String
	return r, json.Unmarshal([]byte(spec), &r.Spec)
}

func (s *Scheduler) loadStep(ctx context.Context, id string) (stepRow, error) {
	var st stepRow
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = scanStepRow(tx.QueryRowContext(ctx, `SELECT `+stepColumns+` FROM steps WHERE id = ?`, id))
		return err
	})
	return st, err
}

func (s *Scheduler) stepsOf(ctx context.Context, reqID string) ([]stepRow, error) {
	var out []stepRow
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+stepColumns+` FROM steps WHERE req_id = ? ORDER BY seq`, reqID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			st, err := scanStepRow(rows)
			if err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}

// depsDone reports whether every dependency of st is done (guard deps).
func depsDone(st stepRow, all []stepRow) bool {
	state := map[string]string{}
	for _, o := range all {
		state[o.ID] = o.State
	}
	for _, d := range st.DependsOn {
		if state[d] != "done" {
			return false
		}
	}
	return true
}

// fixOutcome is the exit event of an integrate step waiting for its fix step:
// fix_done once the newest fix step for it is done, fix_dropped if it was
// dropped, "" while it is still open.
func fixOutcome(integrate stepRow, all []stepRow) string {
	var newest *stepRow
	for i := range all {
		if all[i].FixFor == integrate.ID && (newest == nil || all[i].Seq > newest.Seq) {
			newest = &all[i]
		}
	}
	switch {
	case newest == nil:
		return ""
	case newest.State == "done":
		return "fix_done"
	case newest.State == "dropped":
		return "fix_dropped"
	}
	return ""
}

// singleStep computes the guard single_step: the requirement has exactly one
// work step, it is done, and there is no fix step (S29/S30).
func singleStep(all []stepRow) string {
	work, done := 0, 0
	for _, o := range all {
		if o.Type == "integrate" || o.State == "dropped" {
			continue
		}
		if o.Type == "fix" {
			return "many"
		}
		work++
		if o.State == "done" {
			done++
		}
	}
	if work == 1 && done == 1 {
		return "one"
	}
	return "many"
}

// overBudget is the guard budget: cumulative usage above twice the
// requirement budget (H4); budget 0 means unlimited.
func (s *Scheduler) overBudget(ctx context.Context, r reqRow) (bool, error) {
	if r.BudgetTokens <= 0 {
		return false, nil
	}
	var used int64
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT `+usageTokensSQL+` FROM runs WHERE req_id = ?`, r.ID).Scan(&used)
	})
	limit := int64(r.BudgetTokens) * int64(1+r.BudgetExtensions) * 2
	return used > limit, err
}

// currentRun returns the newest Run of a step that has not ended.
func (s *Scheduler) currentRun(ctx context.Context, stepID string) (runRow, bool, error) {
	var r runRow
	var model sql.NullString
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id, role, status, model FROM runs
			WHERE step_id = ? AND status IN ('pending', 'running') ORDER BY started_at DESC, id DESC LIMIT 1`, stepID).
			Scan(&r.ID, &r.Role, &r.Status, &model)
	})
	r.Model = model.String
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	return r, err == nil, err
}

// lastCheckFailure returns the open kind=check finding of a step (ACR-01).
func (s *Scheduler) lastCheckFailure(ctx context.Context, stepID string) (tests []string, tail string, ok bool, err error) {
	var ft, lt string
	err = s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT failing_tests, log_tail FROM findings
			WHERE scope = ? AND kind = 'check' AND status = 'open' ORDER BY CAST(SUBSTR(id, 2) AS INTEGER) DESC LIMIT 1`, stepID).Scan(&ft, &lt)
	})
	if err == sql.ErrNoRows {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	if err := json.Unmarshal([]byte(ft), &tests); err != nil {
		return nil, "", false, err
	}
	return tests, lt, true, nil
}

// openReviewFindings returns the step's open kind=review findings, oldest first.
func (s *Scheduler) openReviewFindings(ctx context.Context, stepID string) ([]FindingLine, error) {
	var out []FindingLine
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, severity, location, problem, close_when FROM findings
			WHERE scope = ? AND kind = 'review' AND status = 'open' ORDER BY CAST(SUBSTR(id, 2) AS INTEGER)`, stepID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f FindingLine
			var loc sql.NullString
			if err := rows.Scan(&f.ID, &f.Severity, &loc, &f.Problem, &f.CloseWhen); err != nil {
				return err
			}
			var l struct {
				Path string `json:"path"`
				Line int    `json:"line"`
			}
			_ = json.Unmarshal([]byte(loc.String), &l)
			f.Location = fmt.Sprintf("%s:%d", l.Path, l.Line)
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}
