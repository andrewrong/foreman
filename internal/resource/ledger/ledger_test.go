package ledger

import (
	"errors"
	"testing"
	"time"
)

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

const res = "model/claude/max-1/7d"

func items(n int64) []Item { return []Item{{Resource: res, Amount: n}} }

// available = capacity - used - sum(unsettled) - consumption settled after the
// reading; the same formula the Gate evaluates.
func available(l *Ledger, capacity, used int64, readingAt time.Time) int64 {
	settled, _ := l.SettledSince(res, readingAt)
	return capacity - used - l.Reserved(res) - settled
}

func TestSettleActualBelowReservationReturnsDifference(t *testing.T) {
	l := New()
	if err := l.Reserve(Reservation{ID: "a", Items: items(20), At: t0()}); err != nil {
		t.Fatal(err)
	}
	if got := available(l, 100, 50, t0()); got != 30 {
		t.Fatalf("available while reserved = %d, want 30", got)
	}
	freed, err := l.Settle("a", items(5), t0().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0].Amount != 15 {
		t.Fatalf("freed = %+v, want 15", freed)
	}
	if got := available(l, 100, 50, t0()); got != 45 {
		t.Fatalf("available after settle = %d, want 45", got)
	}
	// A reading taken after the settlement already contains the consumption.
	if got := available(l, 100, 55, t0().Add(2*time.Minute)); got != 45 {
		t.Fatalf("available after fresh reading = %d, want 45", got)
	}
}

func TestRebuildRestoresReservedFromRuns(t *testing.T) {
	l := New()
	err := l.Rebuild([]Reservation{
		{ID: "run-1", Items: items(12)},
		{ID: "run-2", Items: items(8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Reserved(res); got != 20 {
		t.Fatalf("reserved = %d, want 20", got)
	}
	if got := len(l.Open()); got != 2 {
		t.Fatalf("open = %d", got)
	}
	if _, err := l.Settle("run-1", nil, t0()); err != nil {
		t.Fatal(err)
	}
	if got := l.Reserved(res); got != 8 {
		t.Fatalf("reserved after settle = %d, want 8", got)
	}
}

func TestPruneForgetsOldSettlements(t *testing.T) {
	l := New()
	_ = l.Reserve(Reservation{ID: "a", Items: items(1)})
	_, _ = l.Settle("a", items(1), t0())
	l.Prune(t0().Add(time.Second))
	if n, _ := l.SettledSince(res, t0().Add(-time.Hour)); n != 0 {
		t.Fatalf("settled = %d after prune", n)
	}
}

func TestRejectsDuplicateAndUnknown(t *testing.T) {
	l := New()
	r := Reservation{ID: "a", Items: items(1)}
	if err := l.Reserve(r); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve(r); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := l.Release("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	if err := l.Rebuild([]Reservation{r, r}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("rebuild duplicate: %v", err)
	}
}
