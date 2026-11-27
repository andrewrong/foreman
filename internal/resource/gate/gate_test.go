package gate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

var t0 = time.Unix(1_800_000_000, 0)

const acct = "model/claude/max-1/"

type env struct {
	t   *testing.T
	clk *clock.Fake
	m   *meter.Store
	l   *ledger.Ledger
	g   *Gate
}

func newEnv(t *testing.T, mod ...func(*meter.Config)) *env {
	t.Helper()
	cfg := meter.DefaultConfig()
	for _, f := range mod {
		f(&cfg)
	}
	e := &env{t: t, clk: clock.NewFake(t0), m: meter.NewStore(), l: ledger.New()}
	e.g = New(Options{Clock: e.clk, Config: cfg, Meters: e.m, Ledger: e.l})
	return e
}

func (e *env) acquire(prio Priority, amount int64, res ...string) Decision {
	e.t.Helper()
	items := make([]Item, len(res))
	for i, r := range res {
		items[i] = Item{Resource: r, Amount: amount}
	}
	d, err := e.g.Acquire(context.Background(), Demand{Items: items, Priority: prio, Caller: Caller{Project: "p", Stage: "code"}})
	if err != nil {
		e.t.Fatalf("Acquire: %v", err)
	}
	return d
}

func (e *env) claude(windows map[string]meter.ClaudeWindow) {
	e.m.IngestClaude("max-1", meter.ClaudeRateLimitInfo{UnifiedWindows: windows}, e.clk.Now())
}

func wantWait(t *testing.T, d Decision, code, res string) {
	t.Helper()
	if d.Allowed() || d.Wait == nil {
		t.Fatalf("want Wait(%s:%s), got allowed (warnings %v)", code, res, d.Warnings)
	}
	for _, r := range d.Wait.Reasons {
		if r.Code == code && r.Resource == res {
			return
		}
	}
	t.Fatalf("want reason %s:%s, got %v", code, res, d.Wait.Reasons)
}

func wantAllowed(t *testing.T, d Decision) {
	t.Helper()
	if !d.Allowed() {
		t.Fatalf("want Allow, got Wait %+v", d.Wait)
	}
}

func TestQuotaAllWindowsGate(t *testing.T) {
	e := newEnv(t)
	r5 := t0.Add(3 * time.Hour)
	e.claude(map[string]meter.ClaudeWindow{
		"seven_day": {Utilization: 0.50, ResetsAt: t0.Add(7 * 24 * time.Hour).Unix()},
		"five_hour": {Utilization: 0.95, ResetsAt: r5.Unix()},
	})
	// The demand names only the 7d window, but the 5h window is below floor.
	d := e.acquire(P1, 2, acct+"7d")
	wantWait(t, d, "below_floor", acct+"5h")
	if !d.Wait.Until.Equal(r5) {
		t.Fatalf("Until = %v, want %v", d.Wait.Until, r5)
	}
	// An event reporting only seven_day keeps the 5h value.
	e.clk.Advance(time.Minute)
	e.claude(map[string]meter.ClaudeWindow{
		"seven_day": {Utilization: 0.50, ResetsAt: t0.Add(7 * 24 * time.Hour).Unix()},
	})
	wantWait(t, e.acquire(P1, 2, acct+"7d"), "below_floor", acct+"5h")
	// Nothing was held by the refused demands.
	if got := e.l.Reserved(acct + "7d"); got != 0 {
		t.Fatalf("reserved after Wait = %d", got)
	}
	// After the 5h window resets, it no longer blocks.
	e.clk.Advance(3 * time.Hour)
	wantAllowed(t, e.acquire(P1, 2, acct+"7d"))
}

func TestQuotaDailyPacing(t *testing.T) {
	e := newEnv(t)
	reset := t0.Add(7 * 24 * time.Hour)
	seven := func(u float64) map[string]meter.ClaudeWindow {
		return map[string]meter.ClaudeWindow{"seven_day": {Utilization: u, ResetsAt: reset.Unix()}}
	}
	e.claude(seven(0.40)) // 60 points over 7 days: 8 per day
	first := e.acquire(P1, 5, acct+"7d")
	wantAllowed(t, first)
	d := e.acquire(P1, 5, acct+"7d")
	wantWait(t, d, "over_daily_pace", acct+"7d")
	if want := t0.Add(24 * time.Hour); !d.Wait.Until.Equal(want) {
		t.Fatalf("Until = %v, want next day %v", d.Wait.Until, want)
	}
	p0 := e.acquire(P0, 5, acct+"7d")
	wantAllowed(t, p0)
	for _, l := range []*Lease{first.Lease, p0.Lease} {
		if _, err := l.Settle(context.Background(), []Item{{Resource: acct + "7d", Amount: 5}}); err != nil {
			t.Fatal(err)
		}
	}
	// Next day: a fresh reading, a fresh allowance.
	e.clk.Advance(24 * time.Hour)
	e.claude(seven(0.50))
	wantAllowed(t, e.acquire(P1, 5, acct+"7d"))
}

func TestPriorityGateBelowWarn(t *testing.T) {
	e := newEnv(t)
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.75}}) // 25 left
	wantAllowed(t, e.acquire(P1, 2, acct+"7d"))
	wantWait(t, e.acquire(P2, 2, acct+"7d"), "priority_gate", acct+"7d")
	d := e.acquire(P0, 2, acct+"7d")
	wantAllowed(t, d)
	if len(d.Warnings) == 0 || d.Warnings[0].Code != "below_warn" {
		t.Fatalf("want below_warn warning, got %v", d.Warnings)
	}
}

func TestConfigOverridesKind(t *testing.T) {
	e := newEnv(t, func(c *meter.Config) {
		c.Kinds = map[string]meter.Kind{"RES-MODEL": {ID: "RES-MODEL", Prefix: "model/", Floor: meter.Limit{Pct: 30}, Passive: true}}
	})
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.75}}) // 25 left < 30 floor
	wantWait(t, e.acquire(P0, 1, acct+"7d"), "below_floor", acct+"7d")
}

func TestConcurrentDemandsAdmitOnlyOne(t *testing.T) {
	e := newEnv(t, func(c *meter.Config) { c.SessionSlots = 1 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := e.g.Acquire(context.Background(), Demand{Items: []Item{{Resource: "local/slots/session", Amount: 1}}, Priority: P1})
			if err != nil {
				t.Error(err)
				return
			}
			if d.Allowed() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("allowed = %d, want 1", allowed)
	}
	// Model variant: 20 left, floor 10, two demands of 6.
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.80}})
	wantAllowed(t, e.acquire(P0, 6, acct+"7d"))
	wantWait(t, e.acquire(P0, 6, acct+"7d"), "below_floor", acct+"7d")
}

func TestMultiResourceDemandHoldsNothingOnShortfall(t *testing.T) {
	e := newEnv(t, func(c *meter.Config) { c.SessionSlots = 1 })
	e.m.IngestGitHub("github/rest", meter.GitHubLimits{Limit: 5000, Remaining: 100}, e.clk.Now())
	d := e.acquire(P1, 1, "local/slots/session", "github/rest")
	wantWait(t, d, "below_floor", "github/rest")
	if e.l.Reserved("local/slots/session") != 0 {
		t.Fatal("slot was held by a refused demand")
	}
	wantAllowed(t, e.acquire(P1, 1, "local/slots/session"))
}

func TestEssentialUsesReservedGitHubShare(t *testing.T) {
	e := newEnv(t)
	// 5000 limit, floor 500, reserved_for merge 500: 900 remaining.
	e.m.IngestGitHub("github/rest", meter.GitHubLimits{Limit: 5000, Remaining: 1100}, e.clk.Now())
	wantAllowed(t, e.acquire(P1, 50, "github/rest"))
	wantWait(t, e.acquire(P1, 100, "github/rest"), "below_floor", "github/rest")
	d, err := e.g.Acquire(context.Background(), Demand{Items: []Item{{Resource: "github/rest", Amount: 100}}, Priority: P1, Essential: true})
	if err != nil || !d.Allowed() {
		t.Fatalf("essential demand: %v %+v", err, d.Wait)
	}
}

func TestMarkExhaustedWaitsUntilReset(t *testing.T) {
	e := newEnv(t)
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.10}})
	reset := t0.Add(2 * time.Hour)
	e.m.MarkExhausted(acct+"7d", reset)
	d := e.acquire(P0, 1, acct+"7d")
	wantWait(t, d, "exhausted", acct+"7d")
	if !d.Wait.Until.Equal(reset) {
		t.Fatalf("Until = %v, want %v", d.Wait.Until, reset)
	}
	e.clk.Advance(2 * time.Hour)
	wantAllowed(t, e.acquire(P0, 1, acct+"7d"))
}

type fakeMeter struct {
	calls int
	fail  bool
	clk   clock.Clock
}

func (f *fakeMeter) Read(context.Context) (meter.Reading, error) {
	f.calls++
	if f.fail {
		return meter.Reading{}, errors.New("boom")
	}
	return meter.Reading{Resource: "github/rest", Capacity: 5000, Used: 100, MeasuredAt: f.clk.Now()}, nil
}

func TestStaleActiveResourceRemeasuresThenOnlyP0(t *testing.T) {
	e := newEnv(t)
	fm := &fakeMeter{clk: e.clk}
	e.m.Register("github/rest", fm)
	wantAllowed(t, e.acquire(P1, 1, "github/rest"))
	if fm.calls != 1 {
		t.Fatalf("calls = %d, want 1 (no reading yet)", fm.calls)
	}
	e.clk.Advance(5 * time.Minute)
	wantAllowed(t, e.acquire(P1, 1, "github/rest"))
	if fm.calls != 1 {
		t.Fatalf("calls = %d, fresh reading must not be re-measured", fm.calls)
	}
	e.clk.Advance(6 * time.Minute) // 11m: stale
	fm.fail = true
	wantWait(t, e.acquire(P1, 1, "github/rest"), "stale", "github/rest")
	if fm.calls != 2 {
		t.Fatalf("calls = %d, want re-measure attempt", fm.calls)
	}
	d := e.acquire(P0, 1, "github/rest")
	wantAllowed(t, d)
	if len(d.Warnings) == 0 || d.Warnings[0].Code != "stale" {
		t.Fatalf("want stale warning, got %v", d.Warnings)
	}
	fm.fail = false
	wantAllowed(t, e.acquire(P1, 1, "github/rest"))
}

func TestStaleModelReadingEstimatesFromLedger(t *testing.T) {
	e := newEnv(t)
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.50}})
	first := e.acquire(P1, 35, acct+"7d")
	wantAllowed(t, first)
	e.clk.Advance(time.Hour) // far beyond stale_after; no re-measure possible
	// 100 - 50 used - 35 reserved = 15 available; 6 more would cross the floor.
	wantWait(t, e.acquire(P1, 6, acct+"7d"), "below_floor", acct+"7d")
	wantAllowed(t, e.acquire(P1, 5, acct+"7d"))
}

func TestStaleModelReadingNotDegradedToP0Only(t *testing.T) {
	e := newEnv(t)
	e.claude(map[string]meter.ClaudeWindow{"seven_day": {Utilization: 0.40}})
	e.clk.Advance(time.Hour)
	d := e.acquire(P1, 5, acct+"7d")
	wantAllowed(t, d)
	if len(d.Warnings) == 0 || d.Warnings[0].Code != "estimated" {
		t.Fatalf("want estimated warning, got %v", d.Warnings)
	}
	// Settled consumption after the last reading counts against it.
	if _, err := d.Lease.Settle(context.Background(), []Item{{Resource: acct + "7d", Amount: 45}}); err != nil {
		t.Fatal(err)
	}
	// 100 - 40 - 45 settled since the reading = 15 available.
	wantWait(t, e.acquire(P1, 6, acct+"7d"), "below_floor", acct+"7d")
}

func TestOneMergeAtATime(t *testing.T) {
	e := newEnv(t)
	e.m.IngestBaseHealth("r1", true, e.clk.Now())
	e.m.IngestBaseHealth("r2", true, e.clk.Now())
	a := e.acquire(P1, 1, "local/slots/merge/r1")
	wantAllowed(t, a)
	wantWait(t, e.acquire(P1, 1, "local/slots/merge/r1"), "slot_full", "local/slots/merge/r1")
	wantAllowed(t, e.acquire(P1, 1, "local/slots/merge/r2")) // other repo is independent
	if err := a.Lease.Release(); err != nil {
		t.Fatal(err)
	}
	wantAllowed(t, e.acquire(P1, 1, "local/slots/merge/r1"))
}

func TestRedBaseBlocksMerge(t *testing.T) {
	e := newEnv(t)
	e.m.IngestBaseHealth("r1", false, e.clk.Now())
	wantWait(t, e.acquire(P0, 1, "local/slots/merge/r1"), "red_base", "github/base-health/r1")
	if e.l.Reserved("local/slots/merge/r1") != 0 {
		t.Fatal("merge slot held while base is red")
	}
	e.clk.Advance(time.Minute)
	e.m.IngestBaseHealth("r1", true, e.clk.Now())
	wantAllowed(t, e.acquire(P1, 1, "local/slots/merge/r1"))
}

func TestContentCreateCounter(t *testing.T) {
	e := newEnv(t)
	d := e.acquire(P1, 60, "github/content-create")
	wantAllowed(t, d)
	if _, err := d.Lease.Settle(context.Background(), []Item{{Resource: "github/content-create", Amount: 60}}); err != nil {
		t.Fatal(err)
	}
	w := e.acquire(P1, 1, "github/content-create")
	wantWait(t, w, "counter_full", "github/content-create")
	if want := t0.Add(time.Minute); !w.Wait.Until.Equal(want) {
		t.Fatalf("Until = %v, want %v", w.Wait.Until, want)
	}
	e.clk.Advance(time.Minute)
	wantAllowed(t, e.acquire(P1, 1, "github/content-create"))
}

func TestDailyWriteWarns(t *testing.T) {
	e := newEnv(t)
	const gib = int64(1) << 30
	e.m.Observe(meter.Reading{Resource: "local/disk/v", Capacity: 1000 * gib, Used: 100 * gib, MeasuredAt: e.clk.Now()})
	d := e.acquire(P1, 1*gib, "local/disk/v")
	wantAllowed(t, d)
	res, err := d.Lease.Settle(context.Background(), []Item{{Resource: "local/disk/v", Amount: 41 * gib}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "daily_write" {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestLeaseSettleOnce(t *testing.T) {
	e := newEnv(t)
	d := e.acquire(P1, 1, "local/slots/session")
	if _, err := d.Lease.Settle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Lease.Release(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second close: %v", err)
	}
}

func TestInvalidDemandAndUnknownResource(t *testing.T) {
	e := newEnv(t)
	if _, err := e.g.Acquire(context.Background(), Demand{Priority: P1}); !errors.Is(err, ErrInvalidDemand) {
		t.Fatalf("empty demand: %v", err)
	}
	wantWait(t, e.acquire(P0, 1, "nonsense/thing"), "unknown_resource", "nonsense/thing")
}
