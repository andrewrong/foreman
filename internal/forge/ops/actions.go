package ops

import (
	"context"
	"net/http"
	"net/url"
	"time"

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
	var out struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	q := url.Values{"per_page": {"100"}}
	_, err := o.getJSON(ctx, call, opChecksForRef, r.path("/commits/%s/check-runs", url.PathEscape(ref)), q, &out)
	return out.CheckRuns, err
}

// RunsList is OP-08 (conditional GET).
func (o *Ops) RunsList(ctx context.Context, call Call, r Repo, f RunsFilter) ([]Run, error) {
	var out struct {
		WorkflowRuns []Run `json:"workflow_runs"`
	}
	q := url.Values{}
	for k, v := range map[string]string{"branch": f.Branch, "event": f.Event, "head_sha": f.HeadSHA, "status": f.Status} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.PerPage > 0 {
		q.Set("per_page", itoa(f.PerPage))
	}
	_, err := o.getJSON(ctx, call, opRunsList, r.path("/actions/runs"), q, &out)
	return out.WorkflowRuns, err
}

// RunRerunFailed is OP-09.
func (o *Ops) RunRerunFailed(ctx context.Context, call Call, r Repo, runID int64) error {
	_, err := o.sendJSON(ctx, call, opRunRerun, http.MethodPost, r.path("/actions/runs/%d/rerun-failed-jobs", runID), nil, nil)
	return err
}

// JobLogs is OP-10: the raw log text (GitHub redirects to blob storage; the
// redirect is followed without the token). Never cached (FG-3). The caller
// passes the text through privguard before handing it to a session.
func (o *Ops) JobLogs(ctx context.Context, call Call, r Repo, jobID int64) ([]byte, error) {
	resp, err := o.run(ctx, call, opJobLogs, client.Request{
		Method: http.MethodGet, Path: r.path("/actions/jobs/%d/logs", jobID), NoCache: true,
	})
	return resp.Body, err
}

// StaleAfter is how old a reading may get before EnsureFresh calls OP-11 (FG-2).
const StaleAfter = 10 * time.Minute

// RateLimit is OP-11: GET /rate_limit, not counted and needing no lease. It
// records the core and graphql resources in the meter.
func (o *Ops) RateLimit(ctx context.Context) error {
	resp, err := o.c.Do(ctx, client.Request{Method: http.MethodGet, Path: "/rate_limit", NoCache: true})
	if err != nil {
		return err
	}
	var out struct {
		Resources map[string]struct {
			Limit, Remaining int64
			Reset            int64
		} `json:"resources"`
	}
	if err := decode(resp.Body, &out); err != nil {
		return err
	}
	now := o.clk.Now()
	for name, res := range map[string]string{"core": client.ResREST, "graphql": client.ResGraphQL} {
		if v, ok := out.Resources[name]; ok {
			o.meters.IngestGitHub(res, meter.GitHubLimits{Limit: v.Limit, Remaining: v.Remaining, ResetUnix: v.Reset}, now)
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
