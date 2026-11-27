package ops

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// TestForgeLiveSmoke runs read-only operations against a real sandbox repo.
// It needs FOREMAN_TEST_GITHUB_TOKEN_FILE and FOREMAN_TEST_GITHUB_REPO
// (owner/name); otherwise it is skipped. The token is never printed.
func TestForgeLiveSmoke(t *testing.T) {
	tokFile := os.Getenv("FOREMAN_TEST_GITHUB_TOKEN_FILE")
	full := os.Getenv("FOREMAN_TEST_GITHUB_REPO")
	owner, name, ok := strings.Cut(full, "/")
	if tokFile == "" || !ok {
		t.Skip("FOREMAN_TEST_GITHUB_TOKEN_FILE / FOREMAN_TEST_GITHUB_REPO not set")
	}
	raw, err := os.ReadFile(tokFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	m := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m})
	c := client.New(client.Config{Token: strings.TrimSpace(string(raw)), Meters: m, Clock: clk})
	o := New(Config{Client: c, Gate: g, Meters: m, Clock: clk})
	ctx := context.Background()
	if err := o.RateLimit(ctx); err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	r := Repo{owner, name}
	if _, err := o.PRListOpen(ctx, call, r); err != nil {
		t.Fatalf("PRListOpen: %v", err)
	}
	if _, err := o.RunsList(ctx, call, r, RunsFilter{PerPage: 1}); err != nil {
		t.Fatalf("RunsList: %v", err)
	}
}
