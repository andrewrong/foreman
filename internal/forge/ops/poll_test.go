package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	listPath  = "GET /repos/acme/widgets/pulls"
	runsPath  = "GET /repos/acme/widgets/actions/runs"
	baseRepo  = "github/base-health/acme/widgets"
	prsOpen   = `[{"number":1,"state":"open","head":{"ref":"a","sha":"s1"}},{"number":2,"state":"open","head":{"ref":"b","sha":"s2"}},{"number":3,"state":"open","head":{"ref":"c","sha":"s3"}}]`
	runOK     = `{"workflow_runs":[{"id":1,"status":"completed","conclusion":"success","head_branch":"main"}]}`
	runFailed = `{"workflow_runs":[{"id":2,"status":"completed","conclusion":"failure","head_branch":"main"}]}`
)

func setupPoll(r *rig) {
	r.f.routes[listPath] = route{body: prsOpen, etag: `"list1"`}
	for n, sha := range map[string]string{"1": "s1", "2": "s2", "3": "s3"} {
		r.f.routes["GET /repos/acme/widgets/pulls/"+n] = route{body: `{"number":` + n + `,"head":{"sha":"` + sha + `"},"mergeable":true}`, etag: `"pr` + n + `"`}
		r.f.routes["GET /repos/acme/widgets/commits/"+sha+"/check-runs"] = route{body: `{"check_runs":[]}`, etag: `"ck` + n + `"`}
	}
	r.f.routes[runsPath] = route{body: runOK, etag: `"runs1"`}
}

func TestForgeBatchPoll(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, true)
	setupPoll(r)
	in := PollInput{Repo: repo, BaseBranch: "main", Active: true, Known: map[int]string{}, Watch: map[int]bool{}}

	// Round 1: nothing known, so every PR is followed up once.
	res, err := r.o.Poll(ctx, call, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Open) != 3 || len(res.Details) != 3 || res.BaseRun == nil {
		t.Fatalf("round 1: %+v", res)
	}
	if r.f.count(listPath+"?") != 1 || r.f.count("GET /repos/acme/widgets/pulls/") != 3 || r.f.count(runsPath) != 1 {
		t.Fatalf("round 1 calls: %v", r.f.log)
	}
	if rd, _ := r.m.Get(baseRepo); rd.Capacity != 1 {
		t.Fatalf("base health = %+v", rd)
	}
	if !strings.Contains(strings.Join(r.f.log, " "), "branch=main") || !strings.Contains(strings.Join(r.f.log, " "), "event=push") {
		t.Fatalf("runs query: %v", r.f.log)
	}

	// Round 2: heads unchanged, PR 2 is watched (waiting ci). One list, one
	// follow-up pair for PR 2 only, one runs call; unchanged GETs are 304.
	r.f.reset()
	in.Known = map[int]string{1: "s1", 2: "s2", 3: "s3"}
	in.Watch = map[int]bool{2: true}
	usedBefore, _ := r.m.Get("github/rest")
	res, err = r.o.Poll(ctx, call, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Details) != 1 || res.Details[2].PR.Number != 2 {
		t.Fatalf("round 2 details: %+v", res.Details)
	}
	if got := len(r.f.log); got != 4 { // list + pr2 + checks(s2) + runs
		t.Fatalf("round 2 made %d calls: %v", got, r.f.log)
	}
	usedAfter, _ := r.m.Get("github/rest")
	if usedAfter.Used != usedBefore.Used {
		t.Fatalf("304 round consumed budget: %d -> %d", usedBefore.Used, usedAfter.Used)
	}

	// Round 3: PR 3 got a new head, base turned red.
	r.f.reset()
	r.f.routes[listPath] = route{body: strings.Replace(prsOpen, `"s3"`, `"s3b"`, 1), etag: `"list2"`}
	r.f.routes["GET /repos/acme/widgets/commits/s3b/check-runs"] = route{body: `{"check_runs":[]}`, etag: `"ck3b"`}
	r.f.routes[runsPath] = route{body: runFailed, etag: `"runs2"`}
	in.Watch = nil
	res, err = r.o.Poll(ctx, call, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Details) != 1 || res.Details[3].PR.Number != 3 {
		t.Fatalf("round 3 details: %+v", res.Details)
	}
	if r.f.count("GET /repos/acme/widgets/commits/s3b/") != 1 {
		t.Fatalf("round 3 calls: %v", r.f.log)
	}
	if rd, _ := r.m.Get(baseRepo); rd.Capacity != 0 {
		t.Fatalf("base health after failed run = %+v", rd)
	}

	// No requirement in progress: polling stops, no request is made.
	r.f.reset()
	in.Active = false
	res, err = r.o.Poll(ctx, call, in)
	if err != nil || !res.Skipped || len(r.f.log) != 0 {
		t.Fatalf("inactive poll: %+v %v %v", res, err, r.f.log)
	}
}

func TestForgeGraphqlCost(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	r.f.routes["POST /graphql"] = route{body: `{"data":{"repository":{
		"p1":{"number":1,"state":"OPEN","headRefOid":"s1","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
		"p2":{"number":2,"state":"OPEN","headRefOid":"s2","mergeable":"CONFLICTING","commits":{"nodes":[]}}},
		"rateLimit":{"cost":3,"remaining":4997}}}`}
	out, err := r.o.PRStatusBatch(ctx, call, repo, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if out[1].CheckState() != "SUCCESS" || out[2].Mergeable != "CONFLICTING" || out[2].CheckState() != "" {
		t.Fatalf("out = %+v", out)
	}
	// Settled by the returned cost, not by the reserve or by 1.
	if got := r.settled("github/graphql"); got != 3 {
		t.Fatalf("settled graphql = %d, want 3", got)
	}
	if r.settled("github/rest") != 0 {
		t.Fatal("REST charged for a GraphQL call")
	}
	body := r.f.bodies["POST /graphql"]
	if !strings.Contains(body, "rateLimit{cost remaining}") {
		t.Fatalf("query lacks rateLimit: %s", body)
	}

	// Too small a batch does not justify GraphQL (FG-8): no request.
	r.f.reset()
	if _, err := r.o.PRStatusBatch(ctx, call, repo, []int{1}); !errors.Is(err, ErrGraphQLNotWorthIt) || len(r.f.log) != 0 {
		t.Fatalf("small batch: %v %v", err, r.f.log)
	}

	// GraphQL-level errors are surfaced.
	r.f.routes["POST /graphql"] = route{body: `{"errors":[{"message":"boom"}]}`}
	if _, err := r.o.PRStatusBatch(ctx, call, repo, []int{1, 2}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
