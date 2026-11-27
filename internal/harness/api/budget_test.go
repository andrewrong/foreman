package api

import (
	"errors"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

func TestBudgetFallbackDefault(t *testing.T) {
	var zero Capabilities // every harness defaults to can_stop_midrun=false
	if zero.CanStopMidrun {
		t.Fatal("zero capabilities must not stop mid-run")
	}
	spec := RunSpec{RunID: "r1", Role: RoleCoder, TokenLimit: 1000}
	if err := CheckBudgetFallback(zero, spec); !errors.Is(err, ErrBudgetFallback) {
		t.Fatalf("no max_turns/timeout: want ErrBudgetFallback, got %v", err)
	}
	spec.MaxTurns = 10
	if err := CheckBudgetFallback(zero, spec); !errors.Is(err, ErrBudgetFallback) {
		t.Fatalf("no timeout: want ErrBudgetFallback, got %v", err)
	}
	spec.Timeout = time.Minute
	if err := CheckBudgetFallback(zero, spec); err != nil {
		t.Fatalf("bounded: %v", err)
	}
	// Over the cap mid-run: not stopped without the capability bit.
	if StopMidrun(zero, 5000, 1000) {
		t.Fatal("stopped mid-run without can_stop_midrun")
	}
	if !StopMidrun(Capabilities{CanStopMidrun: true}, 5000, 1000) {
		t.Fatal("can_stop_midrun=true must stop when over the cap")
	}
	// Settlement judges by actual usage.
	over := Usage{InputTokens: 600, OutputTokens: 300, CacheCreationTokens: 200, CacheReadTokens: 9_000_000}
	if !BudgetExceeded(over, 1000) {
		t.Fatal("1100 tokens over a 1000 cap must be budget_exceeded")
	}
	if BudgetExceeded(Usage{InputTokens: 400, OutputTokens: 500, CacheReadTokens: 9_000_000}, 1000) {
		t.Fatal("cache reads must not count towards the cap")
	}
	if BudgetExceeded(over, 0) {
		t.Fatal("limit 0 means no cap")
	}
}

func TestCollectFeedsMeter(t *testing.T) {
	events := []Event{
		InitEvent{SessionID: "s"},
		AssistantEvent{ToolCounts: map[string]int{"Bash": 2}, SkillCalls: []string{"tdd"}, AdvisorCalls: 1},
		AssistantEvent{ToolCounts: map[string]int{"Bash": 1}, SkillCalls: []string{"tdd", "x"}},
		RateLimitEvent{Info: meter.ClaudeRateLimitInfo{UnifiedWindows: map[string]meter.ClaudeWindow{
			"seven_day": {Utilization: 0.89, ResetsAt: 1_800_100_000, Status: "allowed"}}}},
		ResultEvent{CostUSD: 0.5},
	}
	store := meter.NewStore()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	sum := Collect(func(yield func(Event) bool) {
		for _, e := range events {
			if !yield(e) {
				return
			}
		}
	}, MeterSink(store, "claude", "max-1", clk))
	if sum.AssistantTurns != 2 || sum.ToolCounts["Bash"] != 3 || sum.AdvisorCalls != 1 || sum.Result == nil {
		t.Fatalf("summary %+v", sum)
	}
	if got := sum.SkillNames(); len(got) != 2 || got[0] != "tdd" || sum.SkillCalls["tdd"] != 2 {
		t.Fatalf("skills %v %v", got, sum.SkillCalls)
	}
	r, ok := store.Get("model/claude/max-1/7d")
	if !ok || r.Used != 89 || !r.MeasuredAt.Equal(clk.Now()) {
		t.Fatalf("meter reading %+v ok=%v", r, ok)
	}
}
