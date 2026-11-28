package meter

import (
	"net/http"
	"testing"
	"time"
)

func t0() time.Time {
	return time.Unix(1_800_000_000, 0)
}

func TestIngestClaudeKeepsAbsentWindows(t *testing.T) {
	s := NewStore()
	raw := []byte(`{"unifiedWindows":{"seven_day":{"utilization":0.89,"resetsAt":1800600000,"status":"allowed"},"five_hour":{"utilization":0.2,"resetsAt":1800010000,"status":"allowed"}}}`)
	info, err := ParseClaudeRateLimitInfo(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.IngestClaude("a", info, t0())
	only7, _ := ParseClaudeRateLimitInfo([]byte(`{"unifiedWindows":{"seven_day":{"utilization":0.91,"resetsAt":1800600000}}}`))
	s.IngestClaude("a", only7, t0().Add(time.Minute))

	r7, _ := s.Get("model/claude/a/7d")
	r5, _ := s.Get("model/claude/a/5h")
	if r7.Used != 91 || r7.Capacity != 100 || r7.ResetsAt.Unix() != 1800600000 {
		t.Fatalf("7d = %+v", r7)
	}
	if r5.Used != 20 || !r5.MeasuredAt.Equal(t0()) {
		t.Fatalf("5h must keep its last value, got %+v", r5)
	}
	if w := s.Windows("claude", "a"); len(w) != 2 || w[0] != "5h" || w[1] != "7d" {
		t.Fatalf("windows = %v", w)
	}
}

func TestObserveIgnoresOlderReading(t *testing.T) {
	s := NewStore()
	s.Observe(Reading{Resource: "x", Used: 2, MeasuredAt: t0().Add(time.Minute)})
	s.Observe(Reading{Resource: "x", Used: 1, MeasuredAt: t0()})
	if r, _ := s.Get("x"); r.Used != 2 {
		t.Fatalf("used = %d", r.Used)
	}
}

func TestGitHubHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Remaining", "4200")
	h.Set("X-RateLimit-Reset", "1800003600")
	l, ok := ParseGitHubHeaders(h)
	if !ok {
		t.Fatal("headers not parsed")
	}
	s := NewStore()
	s.IngestGitHub("github/rest", l, t0())
	r, _ := s.Get("github/rest")
	if r.Capacity != 5000 || r.Used != 800 || r.ResetsAt.Unix() != 1800003600 {
		t.Fatalf("reading = %+v", r)
	}
	if _, ok := ParseGitHubHeaders(http.Header{}); ok {
		t.Fatal("empty headers must not parse")
	}
}

func TestResolveDefaultsAndLongestPrefix(t *testing.T) {
	c := DefaultConfig()
	cases := map[string]string{
		"model/claude/a/7d":        "RES-MODEL",
		"github/rest":              "RES-GH-REST",
		"github/content-create":    "RES-GH-WRITE",
		"local/slots/merge/r":      "RES-SLOTS",
		"github/base-health/r":     "RES-BASE",
		"local/disk/vol":           "RES-DISK",
		"github/actions-minutes/o": "RES-GH-ACTIONS",
	}
	for res, id := range cases {
		k, ok := c.Resolve(res)
		if !ok || k.ID != id {
			t.Errorf("%s -> %q %v, want %s", res, k.ID, ok, id)
		}
	}
	if k, _ := c.Resolve("local/slots/merge/r"); k.DefaultCap != 1 {
		t.Errorf("merge slot capacity = %d, want 1", k.DefaultCap)
	}
	if k, _ := c.Resolve("model/claude/a/7d"); k.Floor.Pct != 10 || k.Warn.Pct != 30 {
		t.Errorf("model thresholds = %+v", k)
	}
	if _, ok := c.Resolve("bogus"); ok {
		t.Error("bogus resolved")
	}
}

func TestEstimatorFallbackAndP75(t *testing.T) {
	cfg := DefaultConfig()
	e := NewEstimator(cfg)
	if got := e.Estimate("coder", "sonnet"); got != 2 {
		t.Fatalf("fallback = %d, want 2", got)
	}
	// 9 usable k samples: still the fallback.
	for i := 0; i < 9; i++ {
		e.RecordDelta("sonnet", Tokens{Input: 1000}, 1) // k = 0.001
	}
	e.RecordDelta("sonnet", Tokens{Input: 1000}, 0)  // decayed: dropped
	e.RecordDelta("sonnet", Tokens{Input: 1000}, -3) // dropped
	e.RecordRun("coder", Tokens{Input: 100000})
	if got := e.Estimate("coder", "sonnet"); got != 2 {
		t.Fatalf("9 samples: %d, want fallback 2", got)
	}
	e.RecordDelta("sonnet", Tokens{Input: 1000}, 1) // 10th sample
	for _, in := range []int64{20000, 40000, 60000} {
		e.RecordRun("coder", Tokens{Input: in})
	}
	// runs: 100k,20k,40k,60k -> sorted 20,40,60,100 -> p75 = 60k; k=0.001 -> 60
	if got := e.Estimate("coder", "sonnet"); got != 60 {
		t.Fatalf("estimate = %d, want 60", got)
	}
	// Other model has no samples.
	if got := e.Estimate("coder", "opus"); got != 2 {
		t.Fatalf("other model = %d, want 2", got)
	}
}

func TestWeightedTokens(t *testing.T) {
	w := DefaultConfig().Weights
	if got := (Tokens{Input: 10, Output: 2, CacheRead: 100}).Weighted(w); got != 30 {
		t.Fatalf("weighted = %v, want 30", got)
	}
}
