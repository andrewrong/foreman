package ops

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// CheckRun is one check run of a commit (OP-07).
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
}

// Run is one workflow run (OP-08).
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Event      string `json:"event"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
}

// RunsFilter narrows OP-08. Zero fields are omitted; PerPage defaults to 30.
type RunsFilter struct {
	Branch, Event, HeadSHA, Status string
	PerPage                        int
}

// ChecksForRef is OP-07 (conditional GET).
func (o *Ops) ChecksForRef(ctx context.Context, call Call, r Repo, ref string) ([]CheckRun, error) {
	var out []CheckRun
	err := o.do(ctx, call, opChecksForRef, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		res, resp, err := gh.Checks.ListCheckRunsForRef(ctx, r.Owner, r.Name, ref, &github.ListCheckRunsOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		if res != nil {
			for _, c := range res.CheckRuns {
				out = append(out, CheckRun{ID: c.GetID(), Name: c.GetName(), Status: c.GetStatus(), Conclusion: c.GetConclusion(), HeadSHA: c.GetHeadSHA()})
			}
		}
		return resp, err
	})
	return out, err
}

// RunsList is OP-08 (conditional GET).
func (o *Ops) RunsList(ctx context.Context, call Call, r Repo, f RunsFilter) ([]Run, error) {
	var out []Run
	err := o.do(ctx, call, opRunsList, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		res, resp, err := gh.Actions.ListRepositoryWorkflowRuns(ctx, r.Owner, r.Name, &github.ListWorkflowRunsOptions{
			Branch: f.Branch, Event: f.Event, HeadSHA: f.HeadSHA, Status: f.Status,
			ListOptions: github.ListOptions{PerPage: f.PerPage},
		})
		if res != nil {
			for _, w := range res.WorkflowRuns {
				out = append(out, Run{ID: w.GetID(), Name: w.GetName(), Status: w.GetStatus(), Conclusion: w.GetConclusion(),
					Event: w.GetEvent(), HeadBranch: w.GetHeadBranch(), HeadSHA: w.GetHeadSHA()})
			}
		}
		return resp, err
	})
	return out, err
}

// RunRerunFailed is OP-09.
func (o *Ops) RunRerunFailed(ctx context.Context, call Call, r Repo, runID int64) error {
	return o.do(ctx, call, opRunRerun, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		return gh.Actions.RerunFailedJobsByID(ctx, r.Owner, r.Name, runID)
	})
}

// JobLogs is OP-10: the raw log text. GitHub answers 302 to blob storage; the
// redirect target is downloaded without the token and the download is not an
// API call. Never cached (FG-3). The caller passes the text through privguard
// before handing it to a session.
func (o *Ops) JobLogs(ctx context.Context, call Call, r Repo, jobID int64) ([]byte, error) {
	var loc *url.URL
	err := o.do(ctx, call, opJobLogs, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		u, resp, err := gh.Actions.GetWorkflowJobLogs(ctx, r.Owner, r.Name, jobID, 1)
		loc = u
		return resp, err
	})
	if err != nil {
		return nil, err
	}
	return o.c.Download(ctx, loc)
}

// StaleAfter is how old a reading may get before EnsureFresh calls OP-11 (FG-2).
const StaleAfter = 10 * time.Minute

// RateLimit is OP-11: GET /rate_limit, not counted and needing no lease. It
// records the core and graphql resources in the meter.
func (o *Ops) RateLimit(ctx context.Context) error {
	use := client.Use{Free: true, NoCache: true}
	limits, _, err := o.c.REST().RateLimit.Get(client.WithUse(ctx, use))
	if err = o.c.MapError(use, err); err != nil {
		return err
	}
	if limits == nil {
		return errors.New("forge: empty rate_limit response")
	}
	now := o.clk.Now()
	for res, v := range map[string]*github.Rate{client.ResREST: limits.Core, client.ResGraphQL: limits.GraphQL} {
		if v != nil {
			o.meters.IngestGitHub(res, meter.GitHubLimits{Limit: int64(v.Limit), Remaining: int64(v.Remaining), ResetUnix: v.Reset.Unix()}, now)
		}
	}
	return nil
}

// EnsureFresh calls RateLimit at startup (no reading yet) and when the REST
// reading is older than StaleAfter.
func (o *Ops) EnsureFresh(ctx context.Context) error {
	if rd, ok := o.meters.Get(client.ResREST); ok && o.clk.Now().Sub(rd.MeasuredAt) <= StaleAfter {
		return nil
	}
	return o.RateLimit(ctx)
}
