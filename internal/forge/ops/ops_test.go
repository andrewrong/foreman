package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/cache"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

func resetS() string {
	return strconv.FormatInt(t0().Add(time.Hour).Unix(), 10)
}

func call() Call {
	return Call{Priority: gate.P1, Caller: gate.Caller{Project: "p", Stage: "test"}}
}

func repo() Repo {
	return Repo{"acme", "widgets"}
}

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

// route answers one "METHOD /path" with a status, body and optional ETag.
type route struct {
	status int
	body   string
	etag   string
	hdr    map[string]string
}

// fake is a recording fake GitHub. It never reads the wall clock.
type fake struct {
	mu     sync.Mutex
	routes map[string]route
	log    []string
	bodies map[string]string // request bodies by "METHOD /path"
	used   int
}

func (f *fake) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.log = append(f.log, key+queryOf(r))
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		f.bodies[key] = string(b)
	}
	rt, ok := f.routes[key]
	if !ok {
		rt = route{status: 404, body: `{"message":"Not Found"}`}
	}
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Reset", resetS())
	w.Header().Set("X-RateLimit-Resource", "core")
	if rt.etag != "" && r.Header.Get("If-None-Match") == rt.etag {
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-f.used))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f.used++
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-f.used))
	for k, v := range rt.hdr {
		w.Header().Set(k, v)
	}
	if rt.etag != "" {
		w.Header().Set("ETag", rt.etag)
	}
	if rt.status == 0 {
		rt.status = 200
	}
	w.WriteHeader(rt.status)
	_, _ = w.Write([]byte(rt.body))
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

func (f *fake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) reset() { f.mu.Lock(); f.log = nil; f.mu.Unlock() }

type rig struct {
	t   *testing.T
	f   *fake
	clk *clock.Fake
	m   *meter.Store
	rec *recorder
	g   *gate.Gate
	c   *client.Client
	o   *Ops
}

func newRig(t *testing.T, withCache bool) *rig {
	t.Helper()
	f := &fake{routes: map[string]route{}, bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	clk := clock.NewFake(t0())
	m := meter.NewStore()
	m.IngestGitHub(client.ResREST, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: t0().Add(time.Hour).Unix()}, t0())
	m.IngestGitHub(client.ResGraphQL, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: t0().Add(time.Hour).Unix()}, t0())
	rec := &recorder{sums: map[string]int64{}}
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m, Recorder: rec})
	cfg := client.Config{BaseURL: srv.URL, Token: "tok", Meters: m, Clock: clk}
	if withCache {
		st, err := sqlite.Open(context.Background(), t.TempDir(), clk)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		cfg.Cache = cache.New(st.Global, clk, 0)
	}
	c := client.New(cfg)
	return &rig{t: t, f: f, clk: clk, m: m, rec: rec, g: g, c: c, o: New(Config{Client: c, Gate: g, Meters: m, Clock: clk})}
}

// recorder is a gate.Recorder that sums settled consumption per resource.
type recorder struct {
	mu   sync.Mutex
	sums map[string]int64
}

func (c *recorder) Record(_ context.Context, _ time.Time, resource, _, _ string, consumed, _ int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sums[resource] += consumed
	return nil
}

func (r *rig) settled(res string) int64 {
	r.rec.mu.Lock()
	defer r.rec.mu.Unlock()
	return r.rec.sums[res]
}

func TestForgeRequiresLease(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	r.f.routes["GET /repos/acme/widgets/pulls/7"] = route{body: `{"number":7}`}

	// The client refuses a request without a lease, or with one that does not
	// hold what the request consumes; nothing reaches the server.
	gh := r.c.REST()
	_, _, err := gh.PullRequests.Get(ctx, "acme", "widgets", 7)
	if !errors.Is(err, client.ErrNoLease) {
		t.Fatalf("no lease: err = %v", err)
	}
	d, _ := r.g.Acquire(ctx, gate.Demand{Items: []gate.Item{{Resource: client.ResREST, Amount: 1}}, Priority: gate.P1})
	wctx := client.WithUse(ctx, client.Use{Resource: client.ResREST, Write: true, Lease: d.Lease})
	_, _, err = gh.PullRequests.Get(wctx, "acme", "widgets", 7)
	if !errors.Is(err, client.ErrLeaseResource) {
		t.Fatalf("lease without write item: err = %v", err)
	}
	if n := len(r.f.log); n != 0 {
		t.Fatalf("%d requests reached the server", n)
	}
	_ = d.Lease.Release()

	// An op acquires the lease itself and settles the real consumption.
	if _, err := r.o.PRGet(ctx, call(), repo(), 7); err != nil {
		t.Fatal(err)
	}
	if r.settled(client.ResREST) != 1 {
		t.Fatalf("rest settled=%d", r.settled(client.ResREST))
	}
	if r.settled(client.ResWrite) != 0 {
		t.Fatal("a read consumed RES-GH-WRITE")
	}

	// A write op also consumes RES-GH-WRITE.
	r.f.routes["PATCH /repos/acme/widgets/pulls/7"] = route{body: `{}`}
	if err := r.o.PRClose(ctx, call(), repo(), 7); err != nil {
		t.Fatal(err)
	}
	if r.settled(client.ResWrite) != 1 || r.settled(client.ResREST) != 2 {
		t.Fatalf("write settled=%d rest=%d", r.settled(client.ResWrite), r.settled(client.ResREST))
	}
}

func TestForgeRequiresLeaseGateWaitSendsNothing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	r.f.routes["GET /repos/acme/widgets/pulls/7"] = route{body: `{"number":7}`}
	r.f.routes["PATCH /repos/acme/widgets/pulls/7"] = route{body: `{}`}
	// 800 of 5000 left: below the floor plus the essential reserve, but above
	// the floor alone.
	r.m.IngestGitHub(client.ResREST, meter.GitHubLimits{Limit: 5000, Remaining: 800, ResetUnix: t0().Add(time.Hour).Unix()}, t0())

	_, err := r.o.PRGet(ctx, call(), repo(), 7)
	if _, ok := err.(Waiting); !ok {
		t.Fatalf("non-essential read: err = %v, want Waiting", err)
	}
	if len(r.f.log) != 0 {
		t.Fatalf("request sent despite Wait: %v", r.f.log)
	}
	if err := r.o.PRClose(ctx, call(), repo(), 7); err != nil {
		t.Fatalf("essential PRClose: %v", err)
	}
	if r.f.count("PATCH") != 1 {
		t.Fatal("PRClose not sent")
	}
}

func TestForgeRequiresLeaseExhaustedAfterRateLimited(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	r.f.routes["GET /repos/acme/widgets/pulls/7"] = route{status: 403, body: `{"message":"API rate limit exceeded"}`,
		hdr: map[string]string{"X-RateLimit-Remaining": "0"}}
	if _, err := r.o.PRGet(ctx, call(), repo(), 7); err == nil {
		t.Fatal("want rate limit error")
	}
	r.f.reset()
	_, err := r.o.PRGet(ctx, call(), repo(), 7)
	w, ok := err.(Waiting)
	if !ok || w.Wait.Reasons[0].Code != "exhausted" {
		t.Fatalf("err = %#v, want exhausted wait", err)
	}
	if len(r.f.log) != 0 {
		t.Fatal("request sent while exhausted")
	}
}

func TestForgeOpsPaths(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	pr := `{"number":7,"state":"open","head":{"ref":"foreman/r1","sha":"abc"},"base":{"ref":"main","sha":"def"}}`
	for k, v := range map[string]route{
		"POST /repos/acme/widgets/pulls":                            {status: 201, body: pr},
		"PUT /repos/acme/widgets/pulls/7/merge":                     {body: `{"sha":"m1","merged":true,"message":"ok"}`},
		"PUT /repos/acme/widgets/pulls/7/update-branch":             {status: 202, body: `{"message":"Updating pull request branch."}`},
		"POST /repos/acme/widgets/issues/7/comments":                {status: 201, body: `{}`},
		"GET /repos/acme/widgets/commits/abc/check-runs":            {body: `{"total_count":1,"check_runs":[{"id":1,"name":"ci","status":"completed","conclusion":"success"}]}`},
		"POST /repos/acme/widgets/actions/runs/9/rerun-failed-jobs": {status: 201, body: `{}`},
		"GET /repos/acme/widgets/actions/jobs/5/logs":               {status: 302, hdr: map[string]string{"Location": "/blob/job5.log"}},
		"GET /blob/job5.log":                                        {body: "line1\nline2\n"},
		"DELETE /repos/acme/widgets/git/refs/heads/foreman/r1":      {status: 204},
		"GET /repos/acme/widgets/branches/main/protection":          {status: 404, body: `{"message":"Branch not protected"}`},
		"GET /rate_limit": {body: `{"resources":{"core":{"limit":5000,"remaining":4000,"reset":1800003600},"graphql":{"limit":5000,"remaining":4900,"reset":1800003600}}}`},
	} {
		r.f.routes[k] = v
	}
	if p, err := r.o.PRCreate(ctx, call(), repo(), NewPR{Title: "t", Head: "foreman/r1", Base: "main"}); err != nil || p.Number != 7 || p.Head.SHA != "abc" {
		t.Fatalf("PRCreate: %+v %v", p, err)
	}
	if !strings.Contains(r.f.bodies["POST /repos/acme/widgets/pulls"], `"head":"foreman/r1"`) {
		t.Fatalf("create body = %s", r.f.bodies["POST /repos/acme/widgets/pulls"])
	}
	if m, err := r.o.PRMerge(ctx, call(), repo(), 7, "abc", ""); err != nil || !m.Merged {
		t.Fatalf("PRMerge: %+v %v", m, err)
	}
	var sent map[string]string
	_ = json.Unmarshal([]byte(r.f.bodies["PUT /repos/acme/widgets/pulls/7/merge"]), &sent)
	if sent["sha"] != "abc" || sent["merge_method"] != "squash" {
		t.Fatalf("merge body = %v", sent)
	}
	if msg, err := r.o.PRUpdateBranch(ctx, call(), repo(), 7, "abc"); err != nil || msg == "" {
		t.Fatalf("PRUpdateBranch: %q %v", msg, err)
	}
	if err := r.o.PRComment(ctx, call(), repo(), 7, "hi"); err != nil {
		t.Fatal(err)
	}
	if cs, err := r.o.ChecksForRef(ctx, call(), repo(), "abc"); err != nil || len(cs) != 1 || cs[0].Conclusion != "success" {
		t.Fatalf("ChecksForRef: %+v %v", cs, err)
	}
	if err := r.o.RunRerunFailed(ctx, call(), repo(), 9); err != nil {
		t.Fatal(err)
	}
	if b, err := r.o.JobLogs(ctx, call(), repo(), 5); err != nil || string(b) != "line1\nline2\n" {
		t.Fatalf("JobLogs: %q %v", b, err)
	}
	if err := r.o.RefDelete(ctx, call(), repo(), "foreman/r1"); err != nil {
		t.Fatal(err)
	}
	if p, err := r.o.BranchProtectionGet(ctx, call(), repo(), "main"); err != nil || p.Available {
		t.Fatalf("BranchProtectionGet: %+v %v", p, err)
	}
	if err := r.o.RateLimit(ctx); err != nil {
		t.Fatal(err)
	}
	if rd, _ := r.m.Get(client.ResREST); rd.Used != 1000 {
		t.Fatalf("rate_limit reading = %+v", rd)
	}
	if r.settled(client.ResWrite) != 6 { // create, merge, update-branch, comment, rerun, delete
		t.Fatalf("write settled = %d", r.settled(client.ResWrite))
	}
}

func TestForgeOpsMergeNotMergeableIsConflict(t *testing.T) {
	r := newRig(t, false)
	r.f.routes["PUT /repos/acme/widgets/pulls/7/merge"] = route{status: 405, body: `{"message":"Pull Request is not mergeable"}`}
	_, err := r.o.PRMerge(context.Background(), call(), repo(), 7, "abc", "squash")
	if _, ok := err.(client.Conflict); !ok {
		t.Fatalf("err = %#v", err)
	}
}

func TestForgeOpsEnsureFresh(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, false)
	r.f.routes["GET /rate_limit"] = route{body: `{"resources":{"core":{"limit":5000,"remaining":5000,"reset":1800003600}}}`}
	if err := r.o.EnsureFresh(ctx); err != nil || r.f.count("GET /rate_limit") != 0 {
		t.Fatalf("fresh reading triggered a call: %v", err)
	}
	r.clk.Advance(StaleAfter + time.Second)
	if err := r.o.EnsureFresh(ctx); err != nil || r.f.count("GET /rate_limit") != 1 {
		t.Fatalf("stale reading not refreshed: %v", err)
	}
	if r.settled(client.ResREST) != 0 {
		t.Fatal("rate_limit call was counted")
	}
}
