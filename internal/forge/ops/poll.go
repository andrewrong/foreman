package ops

import "context"

// PollInput is one repository poll round (FG-7).
type PollInput struct {
	Repo       Repo
	BaseBranch string
	// Active is false when the repository has no requirement in progress;
	// polling then stops and no request is made.
	Active bool
	// Known maps PR number to the head sha seen in the previous round.
	Known map[int]string
	// Watch lists PRs in waiting(ci) or running(merge): they are re-queried
	// every round (with ETag) even if their head did not change.
	Watch map[int]bool
}

// PRDetail is the per-PR follow-up of a round.
type PRDetail struct {
	PR     PR
	Checks []CheckRun
}

// PollResult is the outcome of one round.
type PollResult struct {
	Skipped bool             // Active was false
	Open    []PR             // OP-02, all open PRs
	Details map[int]PRDetail // only PRs whose head changed or that are watched
	BaseRun *Run             // latest push run of the base branch, nil if none
}

// Poll runs one batch round: one OP-02 list, OP-07 + OP-03 only for PRs whose
// head sha changed or that are watched, and one OP-08 for the latest push run
// of the base branch, which updates RES-BASE. All GETs are conditional.
func (o *Ops) Poll(ctx context.Context, call Call, in PollInput) (PollResult, error) {
	res := PollResult{Details: map[int]PRDetail{}}
	if !in.Active {
		res.Skipped = true
		return res, nil
	}
	open, err := o.PRListOpen(ctx, call, in.Repo)
	if err != nil {
		return res, err
	}
	res.Open = open
	for _, p := range open {
		if in.Known[p.Number] == p.Head.SHA && !in.Watch[p.Number] {
			continue
		}
		full, err := o.PRGet(ctx, call, in.Repo, p.Number)
		if err != nil {
			return res, err
		}
		checks, err := o.ChecksForRef(ctx, call, in.Repo, p.Head.SHA)
		if err != nil {
			return res, err
		}
		res.Details[p.Number] = PRDetail{PR: full, Checks: checks}
	}
	runs, err := o.RunsList(ctx, call, in.Repo, RunsFilter{Branch: in.BaseBranch, Event: "push", PerPage: 1})
	if err != nil {
		return res, err
	}
	if len(runs) > 0 {
		res.BaseRun = &runs[0]
		o.meters.IngestBaseHealth(in.Repo.String(), baseHealthy(runs[0]), o.clk.Now())
	}
	return res, nil
}

// baseHealthy: a failed run makes the base unhealthy; success and runs still
// in progress count as healthy (resources.yaml RES-BASE).
func baseHealthy(r Run) bool {
	switch r.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return false
	}
	return true
}
