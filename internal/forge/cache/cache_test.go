package cache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

// fakeGitHub serves resources with ETags; only non-304 answers count.
type fakeGitHub struct {
	mu     sync.Mutex
	bodies map[string]string
	etags  map[string]string
	used   int
	notMod int
	ifNone []string
	srv    *httptest.Server
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{bodies: map[string]string{}, etags: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		inm := r.Header.Get("If-None-Match")
		f.ifNone = append(f.ifNone, inm)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", "1800003600")
		w.Header().Set("X-RateLimit-Resource", "core")
		et := f.etags[r.URL.Path]
		if inm != "" && inm == et {
			f.notMod++
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-f.used))
			w.WriteHeader(http.StatusNotModified)
			return
		}
		f.used++
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-f.used))
		w.Header().Set("ETag", et)
		_, _ = w.Write([]byte(f.bodies[r.URL.Path]))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type rig struct {
	t   *testing.T
	clk *clock.Fake
	m   *meter.Store
	g   *gate.Gate
	c   *client.Client
	ca  *Cache
}

func newRig(t *testing.T, f *fakeGitHub, maxTotal int64) *rig {
	t.Helper()
	clk := clock.NewFake(t0())
	st, err := sqlite.Open(context.Background(), t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := meter.NewStore()
	m.IngestGitHub(client.ResREST, meter.GitHubLimits{Limit: 5000, Remaining: 5000, ResetUnix: 1800003600}, t0())
	r := &rig{t: t, clk: clk, m: m, ca: New(st.Global, clk, maxTotal)}
	r.g = gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m})
	r.c = client.New(client.Config{BaseURL: f.srv.URL, Token: "t", Meters: m, Clock: clk, Cache: r.ca})
	return r
}

// got is what a test reads of one GET.
type got struct {
	Status      int
	Body        []byte
	NotModified bool // served from the cache after a 304
}

func (r *rig) get(path string) got {
	r.t.Helper()
	ctx := context.Background()
	d, err := r.g.Acquire(ctx, gate.Demand{Items: []gate.Item{{Resource: client.ResREST, Amount: 1}}, Priority: gate.P1})
	if err != nil || !d.Allowed() {
		r.t.Fatalf("acquire: %v", err)
	}
	ctx = client.WithUse(ctx, client.Use{Resource: client.ResREST, Lease: d.Lease})
	req, err := r.c.REST().NewRequest(ctx, "GET", strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	var buf bytes.Buffer
	resp, err := r.c.REST().Do(req, &buf)
	if err != nil {
		r.t.Fatal(err)
	}
	return got{Status: resp.StatusCode, Body: buf.Bytes(), NotModified: resp.Header.Get(client.CacheHitHeader) != ""}
}

func TestForgeConditionalGet(t *testing.T) {
	f := newFake(t)
	f.bodies["/pr/1"], f.etags["/pr/1"] = `{"n":1}`, `"v1"`
	r := newRig(t, f, 0)

	first := r.get("/pr/1")
	if first.NotModified || string(first.Body) != `{"n":1}` {
		t.Fatalf("first = %+v", first)
	}
	usedAfterFirst, _ := r.m.Get(client.ResREST)

	second := r.get("/pr/1")
	if !second.NotModified || string(second.Body) != `{"n":1}` || second.Status != 200 {
		t.Fatalf("second = %+v", second)
	}
	if f.ifNone[1] != `"v1"` {
		t.Fatalf("If-None-Match = %q", f.ifNone[1])
	}
	usedAfterSecond, _ := r.m.Get(client.ResREST)
	if usedAfterSecond.Used != usedAfterFirst.Used || f.used != 1 || f.notMod != 1 {
		t.Fatalf("304 consumed budget: used %d -> %d (server counted %d)", usedAfterFirst.Used, usedAfterSecond.Used, f.used)
	}

	// A changed resource is fetched again and the cache is replaced.
	f.bodies["/pr/1"], f.etags["/pr/1"] = `{"n":2}`, `"v2"`
	if third := r.get("/pr/1"); third.NotModified || string(third.Body) != `{"n":2}` {
		t.Fatalf("third = %+v", third)
	}
	if fourth := r.get("/pr/1"); !fourth.NotModified || string(fourth.Body) != `{"n":2}` {
		t.Fatalf("fourth = %+v", fourth)
	}
}

func TestForgeConditionalGetSkipsLargeBodies(t *testing.T) {
	f := newFake(t)
	f.bodies["/big"], f.etags["/big"] = strings.Repeat("x", MaxBodyBytes+1), `"big"`
	r := newRig(t, f, 0)
	r.get("/big")
	r.get("/big")
	if f.notMod != 0 || f.ifNone[1] != "" {
		t.Fatalf("large body was cached: notMod=%d inm=%q", f.notMod, f.ifNone[1])
	}
	if n, _ := r.ca.Size(context.Background()); n != 0 {
		t.Fatalf("size = %d", n)
	}
}

func TestForgeConditionalGetEvictsOldestFetchedAt(t *testing.T) {
	f := newFake(t)
	for _, p := range []string{"/a", "/b", "/c"} {
		f.bodies[p], f.etags[p] = strings.Repeat("y", 100), `"e`+p+`"`
	}
	r := newRig(t, f, 250) // room for two bodies
	ctx := context.Background()
	r.get("/a")
	r.clk.Advance(time.Second)
	r.get("/b")
	r.clk.Advance(time.Second)
	r.get("/a") // 304: refreshes /a, so /b is now the oldest
	r.clk.Advance(time.Second)
	r.get("/c")
	if n, _ := r.ca.Size(ctx); n != 200 {
		t.Fatalf("size = %d, want 200", n)
	}
	if _, _, ok, _ := r.ca.Lookup(ctx, f.srv.URL+"/b"); ok {
		t.Fatal("/b should have been evicted")
	}
	for _, p := range []string{"/a", "/c"} {
		if _, _, ok, _ := r.ca.Lookup(ctx, f.srv.URL+p); !ok {
			t.Fatalf("%s evicted", p)
		}
	}
}
