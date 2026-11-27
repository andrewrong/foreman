package history

import (
	"context"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

var t0 = time.Unix(1_800_000_000, 0).Truncate(time.Hour).Add(10 * time.Minute)

func open(t *testing.T) (*History, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(t0)
	s, err := sqlite.Open(context.Background(), t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(s.Global, clk), clk
}

func TestRecordAccumulatesPerHourAndAttribution(t *testing.T) {
	ctx := context.Background()
	h, _ := open(t)
	rec := func(at time.Time, stage string, n int64) {
		t.Helper()
		if err := h.Record(ctx, at, "github/rest", stage, "p", n, 5000); err != nil {
			t.Fatal(err)
		}
	}
	rec(t0, "code", 10)
	rec(t0.Add(20*time.Minute), "code", 5) // same hour: summed
	rec(t0, "review", 7)                   // other stage: own row
	rec(t0.Add(time.Hour), "code", 3)      // next hour
	rows, err := h.Query(ctx, "github/rest", t0.Add(-time.Hour), t0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].CallerStage != "code" || rows[0].Consumed != 15 || rows[0].Capacity != 5000 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[1].CallerStage != "review" || rows[1].Consumed != 7 {
		t.Fatalf("row1 = %+v", rows[1])
	}
	if !rows[2].Hour.Equal(t0.Truncate(time.Hour).Add(time.Hour)) {
		t.Fatalf("row2 = %+v", rows[2])
	}
}

func TestRecordPrunesRowsOlderThan90Days(t *testing.T) {
	ctx := context.Background()
	h, clk := open(t)
	if err := h.Record(ctx, t0, "x", "s", "p", 1, 10); err != nil {
		t.Fatal(err)
	}
	clk.Advance(Retention + 2*time.Hour)
	if err := h.Record(ctx, clk.Now(), "x", "s", "p", 1, 10); err != nil {
		t.Fatal(err)
	}
	rows, err := h.Query(ctx, "", t0.Add(-time.Hour), clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Hour.After(t0.Add(Retention)) {
		t.Fatalf("rows = %+v", rows)
	}
}
