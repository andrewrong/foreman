package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/pipeline/state"
)

// A red base found by the poll holds the merge: the integrate step waits
// (S03, quota) with no merge call; when the base is green again the next
// dispatch after the wait deadline merges.
func TestM1RedBaseHoldsMergeUntilGreen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	var rows []string
	gh, ghURL := newFakeGitHub(t, func(branch string) string { return f.remoteSHA("refs/heads/" + branch) })
	gh.baseConclusion = "failure"
	app, err := Compose(ctx, f.config(clk, ghURL, func(_ string, _ state.Event, out state.Outcome) { rows = append(rows, out.RowID) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	seed(t, app, clk)

	tick := func() {
		t.Helper()
		clk.Advance(time.Second)
		if _, err := app.Sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		app.Sched.WaitSessions()
	}
	var state2, reason2 string
	for i := 0; i < 40 && reason2 != "quota"; i++ {
		tick()
		query(t, app, `SELECT state, COALESCE(reason, '') FROM steps WHERE id = 'R-m1/S2'`, &state2, &reason2)
	}
	if state2 != "waiting" || reason2 != "quota" || !slices.Contains(rows, "S03") {
		t.Fatalf("integrate step %s/%s, rows %v: the red base did not hold the merge lease", state2, reason2, rows)
	}
	if len(gh.merges) != 0 {
		t.Fatal("merged on a red base")
	}

	gh.mu.Lock()
	gh.baseConclusion = "success"
	gh.mu.Unlock()
	clk.Advance(31 * time.Minute) // past the unknown-reset quota deadline (30m)
	for i := 0; i < 10 && len(gh.merges) == 0; i++ {
		tick()
	}
	if len(gh.merges) != 1 {
		t.Fatalf("no merge after the base turned green; rows %v", rows)
	}
	for _, want := range []string{"S13", "S08", "S44"} {
		if !slices.Contains(rows, want) {
			t.Errorf("row %s missing from %v", want, rows)
		}
	}
}
