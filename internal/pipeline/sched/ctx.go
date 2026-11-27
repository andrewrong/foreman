package sched

import (
	"context"

	"github.com/andrewrong/foreman/internal/workspace/integrate"

	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
)

const (
	coderPrompt = "You are the coder of one step. Read context.md in the run directory, implement the step in the " +
		"session repository, commit, and write result.json as protocol-run.yaml PF-RESULT describes.\n"
	reviewerPrompt = "You are the reviewer of one step. Read context.md in the run directory, judge the diff against " +
		"the acceptance items, and write result.json with a verdict.\n"
)

func renderContext(in ctxmd.Input) string { return ctxmd.Render(in) }

// maxDiffBytes caps the diff given to a reviewer.
const maxDiffBytes = 100 << 10

// contextInput gathers the PF-CTX inputs of a step from the database.
func (s *Scheduler) contextInput(ctx context.Context, st stepRow, req reqRow, role string) (ctxmd.Input, error) {
	in := ctxmd.Input{Role: role, Constitution: s.d.Constitution, Goal: req.Spec.Goal, Boundaries: req.Spec.Boundaries,
		StepID: st.ID, StepGoal: st.Goal}
	ears := map[string]string{}
	for _, a := range req.Spec.Outcome {
		in.Outcome = append(in.Outcome, ctxmd.Acc{ID: a.ID, EARS: a.EARS})
		ears[a.ID] = a.EARS
	}
	for _, id := range st.Acceptance {
		in.StepAcceptance = append(in.StepAcceptance, ctxmd.Acc{ID: id, EARS: ears[id]})
	}
	all, err := s.stepsOf(ctx, st.ReqID)
	if err != nil {
		return in, err
	}
	for _, o := range all {
		if o.ID != st.ID {
			in.Others = append(in.Others, ctxmd.StepLine{ID: o.ID, Type: o.Type, Goal: firstLine(o.Goal), State: o.State})
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
	if role == "reviewer" {
		in.Diff = s.diff(ctx, st, req)
		return in, nil
	}
	tests, tail, ok, err := s.lastCheckFailure(ctx, st.ID)
	if err != nil {
		return in, err
	}
	if ok {
		in.LastFailure = &ctxmd.Failure{FailingTests: tests, LogTail: tail}
	}
	return in, nil
}

// diff is the step's change since the requirement started, read in the
// integration directory from the head the check stage fetched (never with a
// session repo as cwd). An unreadable diff is empty: the reviewer then reads
// the repository.
func (s *Scheduler) diff(ctx context.Context, st stepRow, req reqRow) string {
	base := "refs/remotes/mirror/" + s.d.Integ.Default
	head := integrate.StepRefPrefix + "steps/" + st.ID
	if st.LastReviewed != "" {
		base = st.LastReviewed
	}
	if st.Type == "integrate" {
		// The integrate step's review reads the pull request: its branch in
		// the integration directory, since E-PR.last_reviewed_sha (the
		// increment after update-branch, S46).
		head = "refs/heads/" + req.Branch
		if row, err := s.loadPRRow(ctx, req.ID); err == nil && row.LastReviewed != "" {
			base = row.LastReviewed
		}
	}
	out, err := s.d.Git.Run(ctx, s.d.Integ.Path, "diff", "--no-color", base+"..."+head)
	if err != nil {
		return ""
	}
	if len(out) > maxDiffBytes {
		out = out[:maxDiffBytes]
	}
	return out
}
