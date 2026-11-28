package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

type env struct {
	t   *testing.T
	clk *clock.Fake
	m   *meter.Store
	g   *gate.Gate
	c   *Client
}

func newEnv(t *testing.T, h http.HandlerFunc) *env {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &env{t: t, clk: clock.NewFake(t0()), m: meter.NewStore()}
	e.m.IngestGitHub(ResREST, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: t0().Add(time.Hour).Unix()}, t0())
	e.g = gate.New(gate.Options{Clock: e.clk, Config: meter.DefaultConfig(), Meters: e.m})
	e.c = New(Config{BaseURL: srv.URL, Token: "tok-123", Meters: e.m, Clock: e.clk})
	return e
}

func (e *env) use(res string) Use {
	e.t.Helper()
	d, err := e.g.Acquire(context.Background(), gate.Demand{Items: []gate.Item{{Resource: res, Amount: 1}}, Priority: gate.P1})
	if err != nil || !d.Allowed() {
		e.t.Fatalf("acquire: %v %+v", err, d.Wait)
	}
	return Use{Resource: res, Lease: d.Lease}
}

// get sends GET path through the REST client with a REST lease and maps the error.
func (e *env) get(path string) (status int, err error) {
	e.t.Helper()
	u := e.use(ResREST)
	ctx := WithUse(context.Background(), u)
	req, err := e.c.REST().NewRequest(ctx, "GET", path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	var buf bytes.Buffer
	resp, err := e.c.REST().Do(req, &buf)
	if resp != nil {
		status = resp.StatusCode
	}
	return status, e.c.MapError(u, err)
}

func TestForgeUpdatesMeterFromHeaders(t *testing.T) {
	var gotAuth, gotVer string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotVer = r.Header.Get("Authorization"), r.Header.Get("X-GitHub-Api-Version")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(t0().Add(30*time.Minute).Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := e.get("x"); err != nil {
		t.Fatal(err)
	}
	rd, ok := e.m.Get(ResREST)
	if !ok || rd.Capacity != 5000 || rd.Used != 679 || !rd.ResetsAt.Equal(t0().Add(30*time.Minute)) {
		t.Fatalf("reading = %+v", rd)
	}
	if gotAuth != "Bearer tok-123" || gotVer == "" {
		t.Fatalf("headers: auth=%q version=%q", gotAuth, gotVer)
	}
}

func TestForgeUpdatesMeterFromHeadersGraphQLResource(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4990")
		w.Header().Set("X-RateLimit-Reset", "1800003600")
		w.Header().Set("X-RateLimit-Resource", "graphql")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"x"}}}`))
	})
	e.m.IngestGitHub(ResGraphQL, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: 1800003600}, t0())
	ctx := WithUse(context.Background(), e.use(ResGraphQL))
	var q struct {
		Viewer struct{ Login githubv4.String }
	}
	if err := e.c.GraphQL().Query(ctx, &q, nil); err != nil {
		t.Fatal(err)
	}
	if rd, _ := e.m.Get(ResGraphQL); rd.Used != 10 {
		t.Fatalf("graphql used = %d", rd.Used)
	}
	if rd, _ := e.m.Get(ResREST); rd.Used != 0 {
		t.Fatalf("rest touched by graphql response: %+v", rd)
	}
}

const secondaryDoc = "https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"

func TestForgeErrorMapping(t *testing.T) {
	reset := strconv.FormatInt(t0().Add(20*time.Minute).Unix(), 10)
	cases := []struct {
		name   string
		status int
		hdr    map[string]string
		body   string
		check  func(t *testing.T, err error, e *env)
	}{
		{"primary", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset, "X-RateLimit-Resource": "core"},
			`{"message":"API rate limit exceeded"}`, func(t *testing.T, err error, e *env) {
				var rl RateLimited
				if !errors.As(err, &rl) || !rl.Reset.Equal(t0().Add(20*time.Minute)) {
					t.Fatalf("err = %#v", err)
				}
				if until, ok := e.m.ExhaustedUntil(ResREST, t0()); !ok || !until.Equal(rl.Reset) {
					t.Fatalf("meter not marked exhausted: %v %v", until, ok)
				}
			}},
		{"primary-429", 429, map[string]string{"X-RateLimit-Reset": reset}, `{"message":"slow down"}`,
			func(t *testing.T, err error, e *env) {
				var rl RateLimited
				if !errors.As(err, &rl) || !rl.Reset.Equal(t0().Add(20*time.Minute)) {
					t.Fatalf("err = %#v", err)
				}
			}},
		{"secondary-retry-after", 403, map[string]string{"Retry-After": "90"},
			`{"message":"You have exceeded a secondary rate limit","documentation_url":"` + secondaryDoc + `"}`,
			func(t *testing.T, err error, e *env) {
				var sl SecondaryLimited
				if !errors.As(err, &sl) || sl.RetryAfter != 90*time.Second {
					t.Fatalf("err = %#v", err)
				}
				if until, ok := e.m.ExhaustedUntil(ResREST, t0()); !ok || !until.Equal(t0().Add(90*time.Second)) {
					t.Fatalf("meter not marked: %v %v", until, ok)
				}
			}},
		{"secondary-no-header", 403, nil, `{"message":"secondary rate limit","documentation_url":"` + secondaryDoc + `"}`,
			func(t *testing.T, err error, e *env) {
				var sl SecondaryLimited
				if !errors.As(err, &sl) || sl.RetryAfter != DefaultSecondaryWait {
					t.Fatalf("err = %#v", err)
				}
			}},
		{"not-found", 404, nil, `{"message":"Not Found"}`, wantType[NotFound]},
		{"conflict", 409, nil, `{"message":"Head branch was modified"}`, wantType[Conflict]},
		{"unprocessable", 422, nil, `{"message":"Validation Failed"}`, wantType[Unprocessable]},
		{"unauthorized-401", 401, nil, `{"message":"Bad credentials"}`, wantType[Unauthorized]},
		{"forbidden-permission", 403, map[string]string{"X-RateLimit-Remaining": "100"}, `{"message":"Resource not accessible by personal access token"}`, wantType[Unauthorized]},
		{"server", 502, nil, `bad gateway`, wantType[Transient]},
		{"other", 405, nil, `{"message":"not mergeable"}`, wantType[APIError]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.hdr {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			status, err := e.get("x")
			if err == nil {
				t.Fatal("want error")
			}
			if status != tc.status {
				t.Fatalf("status = %d", status)
			}
			tc.check(t, err, e)
		})
	}
}

func wantType[T error](t *testing.T, err error, _ *env) {
	t.Helper()
	var target T
	if !errors.As(err, &target) {
		t.Fatalf("err = %#v, want %T", err, target)
	}
}

func TestForgeErrorMappingTransportFailure(t *testing.T) {
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	e.c = New(Config{BaseURL: "http://127.0.0.1:1", Token: "tok", Meters: e.m, Clock: e.clk}) // nothing listens here
	_, err := e.get("x")
	var tr Transient
	if !errors.As(err, &tr) {
		t.Fatalf("err = %#v", err)
	}
}
