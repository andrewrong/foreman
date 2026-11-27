// Package meter holds the resource kinds registry (resources.yaml RES-*), the
// store of latest readings per resource, and the ingestion of observations
// reported by harness and forge adapters (RC-1).
package meter

import (
	"sort"
	"strings"
	"time"
)

// Priority is the dispatch priority P0 (most urgent) .. P3.
type Priority int

// Priorities (resources.yaml section 3).
const (
	P0 Priority = iota
	P1
	P2
	P3
)

// Valid reports whether p is one of P0..P3.
func (p Priority) Valid() bool { return p >= P0 && p <= P3 }

// Limit is a threshold on the remaining amount of a resource. A zero Limit
// means "none". The effective threshold is max(Abs, capacity*Pct/100).
type Limit struct {
	Pct int64 // percent of capacity
	Abs int64 // absolute amount in the resource's unit
}

// None reports whether the limit is unset.
func (l Limit) None() bool { return l.Pct == 0 && l.Abs == 0 }

// Of returns the threshold for the given capacity.
func (l Limit) Of(capacity int64) int64 {
	t := capacity * l.Pct / 100
	if l.Abs > t {
		t = l.Abs
	}
	return t
}

// Counter is a self-counted rolling-window limit (RES-GH-WRITE).
type Counter struct {
	Window   time.Duration
	Capacity int64
}

// Kind is one registered resource kind (resources.yaml kinds). The numbers
// are defaults; Config overrides them.
type Kind struct {
	ID     string
	Prefix string // resources of this kind start with Prefix

	Floor, Warn Limit

	// PriorityGateBelowPct: when remaining < this percent of capacity, only
	// priorities <= PriorityGateMax are admitted. 0 disables.
	PriorityGateBelowPct int64
	PriorityGateMax      Priority

	// EssentialReserve is kept back for Demand.Essential (merge/push/PR).
	EssentialReserve int64

	Pacing      bool  // 7d-window daily pacing (RES-MODEL)
	Passive     bool  // readings only arrive with usage; stale => estimate
	Binary      bool  // capacity 0/1, never consumed (RES-BASE)
	SelfCounted bool  // capacity from config, usage = leases held (slots)
	DefaultCap  int64 // capacity when SelfCounted
	Counters    []Counter
	DailyWrite  int64 // warn when settled amounts in 24h exceed this (0 = off)
}

// Config overrides registry defaults. Loading it from runtime.toml [resources]
// is the config module's job; this is the plain struct it fills.
type Config struct {
	StaleAfter   time.Duration
	Kinds        map[string]Kind   // full replacement of a kind, by Kind.ID
	PacingWindow map[string]string // harness -> window name, e.g. "7d"
	DefaultPacer string            // used for harnesses not in PacingWindow

	SessionSlots int64
	L0CheckSlots int64
	MergeSlots   int64

	FallbackReservePoints int64 // reservation when there are too few samples
	EstimateRuns          int   // runs considered for the p75
	EstimateMinSamples    int   // k_model samples needed
	Weights               TokenWeights
}

// TokenWeights weight token classes into one number (RES-MODEL estimate).
type TokenWeights struct{ Input, Output, CacheRead float64 }

const gib = int64(1) << 30

// DefaultConfig returns the defaults written in resources.yaml.
func DefaultConfig() Config {
	return Config{
		StaleAfter:            10 * time.Minute,
		PacingWindow:          map[string]string{"claude": "7d", "codex": "7d", "gemini": "7d"},
		DefaultPacer:          "7d",
		SessionSlots:          4, // CF-04 is not in this task; interpretation, see report
		L0CheckSlots:          2,
		MergeSlots:            1,
		FallbackReservePoints: 2,
		EstimateRuns:          20,
		EstimateMinSamples:    10,
		Weights:               TokenWeights{Input: 1, Output: 5, CacheRead: 0.1},
	}
}

func defaultKinds() []Kind {
	gh := func(id, prefix string) Kind {
		return Kind{ID: id, Prefix: prefix, Floor: Limit{Pct: 10}, Warn: Limit{Pct: 20}, EssentialReserve: 500}
	}
	return []Kind{
		{ID: "RES-MODEL", Prefix: "model/", Floor: Limit{Pct: 10}, Warn: Limit{Pct: 30},
			PriorityGateBelowPct: 30, PriorityGateMax: P1, Pacing: true, Passive: true},
		gh("RES-GH-REST", "github/rest"),
		gh("RES-GH-GRAPHQL", "github/graphql"),
		{ID: "RES-GH-WRITE", Prefix: "github/content-create", SelfCounted: true,
			Counters: []Counter{{time.Minute, 60}, {time.Hour, 400}}},
		{ID: "RES-GH-ACTIONS", Prefix: "github/actions-minutes/", Warn: Limit{Pct: 20}},
		{ID: "RES-DISK", Prefix: "local/disk/", Floor: Limit{Abs: 10 * gib}, Warn: Limit{Abs: 20 * gib},
			DailyWrite: 40 * gib},
		{ID: "RES-INODES", Prefix: "local/inodes/", Floor: Limit{Pct: 5}, Warn: Limit{Pct: 20}},
		{ID: "RES-MEM", Prefix: "local/mem", Warn: Limit{Pct: 15}},
		{ID: "RES-SLOTS", Prefix: "local/slots/", SelfCounted: true},
		{ID: "RES-BASE", Prefix: "github/base-health/", Binary: true},
	}
}

// Resolve returns the kind of a resource name with Config applied.
func (c Config) Resolve(resource string) (Kind, bool) {
	var best Kind
	found := false
	for _, k := range defaultKinds() {
		if o, ok := c.Kinds[k.ID]; ok {
			k = o
		}
		if strings.HasPrefix(resource, k.Prefix) && (!found || len(k.Prefix) > len(best.Prefix)) {
			best, found = k, true
		}
	}
	if found && best.ID == "RES-SLOTS" && best.DefaultCap == 0 {
		best.DefaultCap = c.slotCapacity(resource)
	}
	return best, found
}

func (c Config) slotCapacity(resource string) int64 {
	kind := strings.TrimPrefix(resource, "local/slots/")
	switch {
	case kind == "session":
		return c.SessionSlots
	case kind == "l0-check":
		return c.L0CheckSlots
	case strings.HasPrefix(kind, "merge/"):
		return c.MergeSlots
	}
	return 0
}

// ModelParts splits "model/<harness>/<account>[/<window>]". window is empty
// when absent.
func ModelParts(resource string) (harness, account, window string, ok bool) {
	p := strings.Split(resource, "/")
	if len(p) < 3 || p[0] != "model" {
		return "", "", "", false
	}
	if len(p) >= 4 {
		window = strings.Join(p[3:], "/")
	}
	return p[1], p[2], window, true
}

// PacingWindowOf returns the window name used for pacing for a harness.
func (c Config) PacingWindowOf(harness string) string {
	if w, ok := c.PacingWindow[harness]; ok {
		return w
	}
	return c.DefaultPacer
}

// Windows lists the known windows of a model account, sorted.
func (s *Store) Windows(harness, account string) []string {
	var out []string
	for _, r := range s.WithPrefix("model/" + harness + "/" + account + "/") {
		_, _, w, _ := ModelParts(r.Resource)
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}
