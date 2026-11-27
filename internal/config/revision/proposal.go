package revision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Pending lists proposals awaiting the owner's decision (the H6 button).
func (s *Store) Pending(ctx context.Context) ([]Proposal, error) {
	var out []Proposal
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, file, diff, actor, time FROM config_proposals WHERE state='pending' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Proposal
			var at string
			if err := rows.Scan(&p.ID, &p.File, &p.Diff, &p.Actor, &at); err != nil {
				return err
			}
			p.Time, _ = time.Parse(time.RFC3339Nano, at)
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// Accept applies a pending proposal as an owner save. The proposal content is
// validated again, so a schema change since the proposal cannot slip it in.
func (s *Store) Accept(ctx context.Context, id int64) (Saved, error) {
	var file, content string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT file, content FROM config_proposals WHERE id=? AND state='pending'`, id).Scan(&file, &content)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Saved{}, fmt.Errorf("%w: %d", ErrNoSuchProposal, id)
	}
	if err != nil {
		return Saved{}, err
	}
	saved, err := s.Save(ctx, Owner, file, []byte(content))
	if err != nil && !errors.Is(err, ErrNoChange) {
		return Saved{}, err
	}
	return saved, s.decide(ctx, id, "accepted")
}

// Reject discards a pending proposal.
func (s *Store) Reject(ctx context.Context, id int64) error { return s.decide(ctx, id, "rejected") }

func (s *Store) decide(ctx context.Context, id int64, state string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE config_proposals SET state=?, decided_at=? WHERE id=? AND state='pending'`, state, s.now(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %d", ErrNoSuchProposal, id)
		}
		return nil
	})
}
