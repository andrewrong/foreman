package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

var t0 = time.Unix(1_800_000_000, 0)

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
	e := &env{t: t, clk: clock.NewFake(t0), m: meter.NewStore()}
	e.m.IngestGitHub(ResREST, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: t0.Add(time.Hour).Unix()}, t0)
	e.g = gate.New(gate.Options{Clock: e.clk, Config: meter.DefaultConfig(), Meters: e.m})
	e.c = New(Config{BaseURL: srv.URL, Token: "tok-123", Meters: e.m, Clock: e.clk})
	return e
}

func (e *env) lease(res ...string) *gate.Lease {
	e.t.Helper()
	var items []gate.Item
	for _, r := range res {
		items = append(items, gate.Item{Resource: r, Amount: 1})
	}
	d, err := e.g.Acquire(context.Background(), gate.Demand{Items: items, Priority: gate.P1})
	if err != nil || !d.Allowed() {
		e.t.Fatalf("acquire: %v %+v", err, d.Wait)
	}
	return d.Lease
}

func (e *env) get(path string) (Response, error) {
	return e.c.Do(context.Background(), Request{Method: "GET", Path: path, Resource: ResREST, Lease: e.lease(ResREST)})
}

func TestForgeUpdatesMeterFromHeaders(t *testing.T) {
	var gotAuth, gotVer string
	e := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotVer = r.Header.Get("Authorization"), r.Header.Get("X-GitHub-Api-Version")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(t0.Add(30*time.Minute).Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := e.get("/x"); err != nil {
		t.Fatal(err)
	}
	rd, ok := e.m.Get(ResREST)
	if !ok || rd.Capacity != 5000 || rd.Used != 679 || !rd.ResetsAt.Equal(t0.Add(30*time.Minute)) {
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
		_, _ = w.Write([]byte(`{}`))
	})
	e.m.IngestGitHub(ResGraphQL, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: 1800003600}, t0)
	_, err := e.c.Do(context.Background(), Request{Method: "POST", Path: "/graphql", Resource: ResGraphQL, Lease: e.lease(ResGraphQL)})
	if err != nil {
		t.Fatal(err)
	}
	if rd, _ := e.m.Get(ResGraphQL); rd.Used != 10 {
		t.Fatalf("graphql used = %d", rd.Used)
	}
	if rd, _ := e.m.Get(ResREST); rd.Used != 0 {
		t.Fatalf("rest touched by graphql response: %+v", rd)
	}
}

func TestForgeErrorMapping(t *testing.T) {
	reset := strconv.FormatInt(t0.Add(20*time.Minute).Unix(), 10)
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
				if !errors.As(err, &rl) || !rl.Reset.Equal(t0.Add(20*time.Minute)) {
					t.Fatalf("err = %#v", err)
				}
				if until, ok := e.m.ExhaustedUntil(ResREST, t0); !ok || !until.Equal(rl.Reset) {
					t.Fatalf("meter not marked exhausted: %v %v", until, ok)
				}
			}},
		{"primary-429", 429, map[string]string{"X-RateLimit-Reset": reset}, `{"message":"slow down"}`,
			func(t *testing.T, err error, e *env) {
				var rl RateLimited
				if !errors.As(err, &rl) {
					t.Fatalf("err = %#v", err)
				}
			}},
		{"secondary-retry-after", 403, map[string]string{"Retry-After": "90"}, `{"message":"You have exceeded a secondary rate limit"}`,
			func(t *testing.T, err error, e *env) {
				var sl SecondaryLimited
				if !errors.As(err, &sl) || sl.RetryAfter != 90*time.Second {
					t.Fatalf("err = %#v", err)
				}
				if until, ok := e.m.ExhaustedUntil(ResREST, t0); !ok || !until.Equal(t0.Add(90*time.Second)) {
					t.Fatalf("meter not marked: %v %v", until, ok)
				}
			}},
		{"secondary-no-header", 403, nil, `{"message":"secondary rate limit"}`,
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
			resp, err := e.get("/x")
			if err == nil {
				t.Fatal("want error")
			}
			if resp.Status != tc.status {
				t.Fatalf("status = %d", resp.Status)
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
	e.c.base = "http://127.0.0.1:1" // nothing listens here
	_, err := e.get("/x")
	var tr Transient
	if !errors.As(err, &tr) {
		t.Fatalf("err = %#v", err)
	}
}
