package gate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

const res = "model/claude/max-1/7d"

func newGate(clk clock.Clock, m *meter.Store, l *ledger.Ledger) *Gate {
	return New(Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m, Ledger: l})
}

func seed(m *meter.Store, at time.Time, util float64) {
	m.IngestClaude("max-1", meter.ClaudeRateLimitInfo{UnifiedWindows: map[string]meter.ClaudeWindow{"seven_day": {Utilization: util}}}, at)
}

// available = capacity - used - unsettled, probed through the Gate: the largest
// amount that still passes the floor.
func largestAllowed(t *testing.T, g *Gate) int64 {
	t.Helper()
	var best int64
	for n := int64(1); n <= 100; n++ {
		d, err := g.Acquire(context.Background(), Demand{Items: []Item{{Resource: res, Amount: n}}, Priority: P0})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Allowed() {
			break
		}
		best = n
		if err := d.Lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	return best
}

func TestSettleActualBelowReservationFreesDifference(t *testing.T) {
	clk := clock.NewFake(t0())
	m, l := meter.NewStore(), ledger.New()
	seed(m, clk.Now(), 0.50) // 50 left, floor 10 => 40 usable
	g := newGate(clk, m, l)

	d, err := g.Acquire(context.Background(), Demand{Items: []Item{{Resource: res, Amount: 20}}, Priority: P0})
	if err != nil || !d.Allowed() {
		t.Fatalf("acquire: %v %+v", err, d.Wait)
	}
	if got := largestAllowed(t, g); got != 20 {
		t.Fatalf("usable while reserved = %d, want 20", got)
	}
	clk.Advance(time.Minute)
	r, err := d.Lease.Settle(context.Background(), []Item{{Resource: res, Amount: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Freed) != 1 || r.Freed[0].Amount != 15 {
		t.Fatalf("freed = %+v, want 15", r.Freed)
	}
	if got := largestAllowed(t, g); got != 35 {
		t.Fatalf("usable after settle = %d, want 35 (40 - 5 consumed)", got)
	}
	// The next measurement already contains the consumption: no double count.
	clk.Advance(time.Minute)
	seed(m, clk.Now(), 0.55)
	if got := largestAllowed(t, g); got != 35 {
		t.Fatalf("usable after fresh reading = %d, want 35", got)
	}
}

func TestRestartRebuildsLedgerFromRunningRuns(t *testing.T) {
	clk := clock.NewFake(t0())
	m := meter.NewStore()
	seed(m, clk.Now(), 0.50)

	// New process: empty ledger, then rebuilt from the runs' reserved fields.
	l := ledger.New()
	g := newGate(clk, m, l)
	if got := largestAllowed(t, g); got != 40 {
		t.Fatalf("before rebuild = %d, want 40", got)
	}
	err := l.Rebuild([]ledger.Reservation{
		{ID: "run-1", Items: []ledger.Item{{Resource: res, Amount: 12}}},
		{ID: "run-2", Items: []ledger.Item{{Resource: res, Amount: 8}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Reserved(res); got != 20 {
		t.Fatalf("reserved = %d, want 20", got)
	}
	if got := largestAllowed(t, g); got != 20 {
		t.Fatalf("after rebuild = %d, want 20", got)
	}
	if _, err := l.Settle("run-1", nil, clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := l.Reserved(res); got != 8 {
		t.Fatalf("reserved after settle = %d, want 8", got)
	}
}

func TestAdoptRecoveredReservation(t *testing.T) {
	clk := clock.NewFake(t0())
	m, l := meter.NewStore(), ledger.New()
	seed(m, clk.Now(), 0.50)
	g := newGate(clk, m, l)
	lease, err := g.Adopt(ledger.Reservation{ID: "run-9", Items: []ledger.Item{{Resource: res, Amount: 30}}}, Caller{Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := largestAllowed(t, g); got != 10 {
		t.Fatalf("usable = %d, want 10", got)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if got := largestAllowed(t, g); got != 40 {
		t.Fatalf("usable after release = %d, want 40", got)
	}
}

func TestLedgerRejectsDuplicateAndUnknown(t *testing.T) {
	l := ledger.New()
	r := ledger.Reservation{ID: "a", Items: []ledger.Item{{Resource: "x", Amount: 1}}}
	if err := l.Reserve(r); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve(r); !errors.Is(err, ledger.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := l.Release("nope"); !errors.Is(err, ledger.ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if err := l.Rebuild([]ledger.Reservation{r, r}); !errors.Is(err, ledger.ErrDuplicate) {
		t.Fatalf("rebuild duplicate: %v", err)
	}
}
