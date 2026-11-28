package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

type baseRig struct {
	clk   *clock.Fake
	gate  *gate.Gate
	ops   *ops.Ops
	conc  atomic.Value // conclusion of the latest push run on main
	repo  ops.Repo
	quiet *meter.Store
}

func newBaseRig(t *testing.T) *baseRig {
	t.Helper()
	r := &baseRig{clk: clock.NewFake(time.Unix(1_800_000_000, 0)), repo: ops.Repo{Owner: "acme", Name: "widgets"}, quiet: meter.NewStore()}
	r.conc.Store("failure")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/rate_limit":
			_, _ = w.Write([]byte(`{"resources":{"core":{"limit":5000,"remaining":5000,"reset":1800003600}}}`))
		case "/repos/acme/widgets/pulls":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/acme/widgets/actions/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []ops.Run{{ID: 1, Status: "completed",
				Conclusion: r.conc.Load().(string), Event: "push", HeadBranch: "main"}}})
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	r.gate = gate.New(gate.Options{Clock: r.clk, Config: meter.DefaultConfig(), Meters: r.quiet, Ledger: ledger.New()})
	c := client.New(client.Config{BaseURL: srv.URL, Token: "t", Meters: r.quiet, Clock: r.clk})
	r.ops = ops.New(ops.Config{Client: c, Gate: r.gate, Meters: r.quiet, Clock: r.clk})
	if err := r.ops.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *baseRig) poll(t *testing.T) {
	t.Helper()
	_, err := r.ops.Poll(context.Background(), ops.Call{Priority: gate.P2}, ops.PollInput{Repo: r.repo, BaseBranch: "main", Active: true})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *baseRig) acquire(t *testing.T, baseFix bool) gate.Decision {
	t.Helper()
	d, err := r.gate.Acquire(context.Background(), gate.Demand{Priority: gate.P2, BaseFix: baseFix,
		Items: []gate.Item{{Resource: mergeSlot(r.repo), Amount: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The key forge ingests for base health (ops.Repo.String) is the key the
// Gate checks for the merge slot built by mergeSlot: a red base found by
// Poll blocks the merge lease with red_base, a green one releases it, and
// only the base-fix merge passes a red base (D-067).
func TestRedBaseFromPollBlocksMergeLease(t *testing.T) {
	r := newBaseRig(t)

	// Nothing measured yet: RES-BASE is not passive, so a non-P0 merge waits
	// as "unmeasured" until the first poll ingests a base run.
	if d := r.acquire(t, false); d.Allowed() || d.Wait.Reasons[0].Code != "unmeasured" {
		t.Fatalf("before any poll: %+v", d)
	}

	r.poll(t) // latest base run: failure
	rd, ok := r.quiet.Get("github/base-health/acme/widgets")
	if !ok || rd.Capacity != 0 {
		t.Fatalf("forge did not ingest a red base under the gate's key: %+v %v", rd, ok)
	}
	d := r.acquire(t, false)
	if d.Allowed() || d.Wait.Reasons[0].Code != "red_base" || d.Wait.Reasons[0].Resource != "github/base-health/acme/widgets" {
		t.Fatalf("red base did not block the merge lease: %+v", d)
	}
	fix := r.acquire(t, true)
	if !fix.Allowed() {
		t.Fatalf("base-fix merge blocked by the red base: %+v", fix.Wait)
	}
	if err := fix.Lease.Release(); err != nil {
		t.Fatal(err)
	}

	r.conc.Store("success")
	r.clk.Advance(time.Second)
	r.poll(t)
	d = r.acquire(t, false)
	if !d.Allowed() {
		t.Fatalf("green base still blocks: %+v", d.Wait)
	}
	// The one merge slot is held: a second PR of the same repo waits.
	if d2 := r.acquire(t, false); d2.Allowed() || d2.Wait.Reasons[0].Code != "slot_full" {
		t.Fatalf("second merge lease: %+v", d2)
	}
}
