package sched

import (
	"context"
	"database/sql"

	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
)

// fixContext adds what a fix step's coder needs. The step's acceptance items
// are the ids of the findings it must close (add_fix_step), and those findings
// are scoped to the integrate step it repairs, not to the fix step, so the
// ordinary lookups by step id do not see them: the acceptance lines carry the
// findings' problem text, and the integrate step's open check finding supplies
// "Previous failure" (failing tests, log tail) unless the fix step has failed
// a check of its own.
func (s *Scheduler) fixContext(ctx context.Context, st stepRow, in *ctxmd.Input) error {
	in.StepAcceptance = nil
	for _, id := range st.Acceptance {
		var problem, closeWhen string
		err := s.d.DB.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT problem, close_when FROM findings WHERE id = ?`, id).Scan(&problem, &closeWhen)
		})
		if err == sql.ErrNoRows {
			in.StepAcceptance = append(in.StepAcceptance, ctxmd.Acc{ID: id})
			continue
		}
		if err != nil {
			return err
		}
		in.StepAcceptance = append(in.StepAcceptance, ctxmd.Acc{ID: id, EARS: problem + " (close when: " + closeWhen + ")"})
	}
	if st.FixFor == "" || in.Role == "reviewer" {
		return nil
	}
	tests, tail, ok, err := s.lastCheckFailure(ctx, st.FixFor)
	if err != nil || !ok {
		return err
	}
	in.LastFailure = &ctxmd.Failure{FailingTests: tests, LogTail: tail}
	return nil
}
