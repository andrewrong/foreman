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
	if role == "reviewer" {
		in.Diff = s.diff(ctx, st)
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
func (s *Scheduler) diff(ctx context.Context, st stepRow) string {
	base := "refs/remotes/mirror/" + s.d.Integ.Default
	if st.LastReviewed != "" {
		base = st.LastReviewed
	}
	out, err := s.d.Git.Run(ctx, s.d.Integ.Path, "diff", "--no-color", base+"..."+integrate.StepRefPrefix+"steps/"+st.ID)
	if err != nil {
		return ""
	}
	if len(out) > maxDiffBytes {
		out = out[:maxDiffBytes]
	}
	return out
}
