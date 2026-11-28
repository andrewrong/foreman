package sched

import "context"

// contextInput gathers the PF-CTX inputs of a step from the database.
func (s *Scheduler) contextInput(ctx context.Context, st stepRow, req reqRow, role string) (Brief, error) {
	in := Brief{Role: role, Constitution: s.d.Constitution, Goal: req.Spec.Goal, Boundaries: req.Spec.Boundaries,
		StepID: st.ID, StepGoal: st.Goal}
	ears := map[string]string{}
	for _, a := range req.Spec.Outcome {
		in.Outcome = append(in.Outcome, Acc{ID: a.ID, EARS: a.EARS})
		ears[a.ID] = a.EARS
	}
	for _, id := range st.Acceptance {
		in.StepAcceptance = append(in.StepAcceptance, Acc{ID: id, EARS: ears[id]})
	}
	all, err := s.stepsOf(ctx, st.ReqID)
	if err != nil {
		return in, err
	}
	for _, o := range all {
		if o.ID != st.ID {
			in.Others = append(in.Others, StepLine{ID: o.ID, Type: o.Type, Goal: firstLine(o.Goal), State: o.State})
		}
	}
	if in.OpenFindings, err = s.openReviewFindings(ctx, st.ID); err != nil {
		return in, err
	}
	if st.Type == "fix" {
		if err := s.fixContext(ctx, st, &in); err != nil {
			return in, err
		}
	}
	if role == RoleReviewer {
		in.Diff = s.diff(ctx, st, req)
		return in, nil
	}
	tests, tail, ok, err := s.lastCheckFailure(ctx, st.ID)
	if err != nil {
		return in, err
	}
	if ok {
		in.LastFailure = &Failure{FailingTests: tests, LogTail: tail}
	}
	return in, nil
}

// diff is the step's change since the requirement started, read from the head
// the check stage fetched. An unreadable diff is empty: the reviewer then
// reads the repository.
func (s *Scheduler) diff(ctx context.Context, st stepRow, req reqRow) string {
	spec := DiffSpec{StepID: st.ID, Base: st.LastReviewed}
	if st.Type == "integrate" {
		// The integrate step's review reads the pull request: its branch,
		// since E-PR.last_reviewed_sha (the increment after update-branch, S46).
		spec.Branch = req.Branch
		if row, err := s.loadPRRow(ctx, req.ID); err == nil && row.LastReviewed != "" {
			spec.Base = row.LastReviewed
		}
	}
	return s.d.Workspace.Diff(ctx, spec)
}
