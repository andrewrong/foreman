package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
)

// The claude adapter uses bypassPermissions (D-056) and may only run inside
// srt. Composed without an srt path, the first coder dispatch is refused
// before any process or run directory exists, and the step stays running(code)
// with its lease still held for a retry once srt is configured.
func TestClaudeHarnessWithoutSrtIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	_, ghURL := newFakeGitHub(t, func(string) string { return "" })
	cfg := f.config(clk, ghURL, nil)
	cfg.Harness, cfg.SrtPath = "claude", ""
	app, err := Compose(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	seed(t, app, clk)

	clk.Advance(time.Second)
	_, err = app.Sched.Tick(ctx)
	if !errors.Is(err, ctxmd.ErrNeedsSandbox) {
		t.Fatalf("tick error = %v, want ErrNeedsSandbox", err)
	}
	var runs int
	query(t, app, `SELECT COUNT(*) FROM runs WHERE status = 'ended'`, &runs)
	if runs != 0 {
		t.Errorf("%d runs ended although nothing launched", runs)
	}
}
