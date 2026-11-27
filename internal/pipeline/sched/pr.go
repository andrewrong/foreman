package sched

import (
	"context"
	"database/sql"
	"encoding/json"
)

// prRow is the part of E-PR the reconciler reads.
type prRow struct {
	Number            int
	Head              string
	LastReviewed      string
	GuardrailApproved string
	Reruns            map[string]int // E-PR.l1_reruns: head sha -> count
}

// loadPRRow returns the requirement's E-PR row; the zero value if none yet.
func (s *Scheduler) loadPRRow(ctx context.Context, reqID string) (prRow, error) {
	var r prRow
	var n sql.NullInt64
	var head, last, approved sql.NullString
	var reruns string
	err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT number, head_sha, last_reviewed_sha, guardrail_approved_sha, l1_reruns
			FROM pull_requests WHERE req_id = ?`, reqID).Scan(&n, &head, &last, &approved, &reruns)
	})
	if err == sql.ErrNoRows {
		return prRow{Reruns: map[string]int{}}, nil
	}
	if err != nil {
		return r, err
	}
	r = prRow{Number: int(n.Int64), Head: head.String, LastReviewed: last.String, GuardrailApproved: approved.String}
	if err := json.Unmarshal([]byte(reruns), &r.Reruns); err != nil {
		return r, err
	}
	if r.Reruns == nil {
		r.Reruns = map[string]int{}
	}
	return r, nil
}

// markRerun counts one L1 rerun for a head (at most one per head, ACR-10). It
// is written before the rerun is requested.
func (s *Scheduler) markRerun(ctx context.Context, reqID, head string) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE pull_requests SET l1_reruns = json_set(l1_reruns, '$."' || ? || '"',
			COALESCE(json_extract(l1_reruns, '$."' || ? || '"'), 0) + 1) WHERE req_id = ?`, head, head, reqID)
		return err
	})
}

// saveHead records the PR head GitHub reports (it changes after update-branch).
func (s *Scheduler) saveHead(ctx context.Context, reqID, head string) error {
	return s.d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE pull_requests SET head_sha = ? WHERE req_id = ?`, head, reqID)
		return err
	})
}
