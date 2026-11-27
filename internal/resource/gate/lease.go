package gate

import (
	"context"
	"sync"

	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Lease is a granted reservation. harness and forge clients are constructed
// with one (MD-3). Settle or Release must be called exactly once.
type Lease struct {
	g      *Gate
	id     string
	items  []Item
	caller Caller

	mu   sync.Mutex
	done bool
}

// ID returns the lease (reservation) id.
func (l *Lease) ID() string { return l.id }

// Items returns the reserved items (model items expanded per window).
func (l *Lease) Items() []Item { return append([]Item(nil), l.items...) }

// Caller returns the attribution given at Acquire.
func (l *Lease) Caller() Caller { return l.caller }

// SettleResult reports what settling changed.
type SettleResult struct {
	Freed    []Item   // reserved - actual per resource; returned to available
	Warnings []Reason // e.g. daily_write over threshold
}

// Settle closes the lease with the real consumption. A model item names an
// account (with or without a window) and applies to every window the lease
// reserved for that account. The difference between reserved and actual goes
// back to available.
func (l *Lease) Settle(ctx context.Context, actual []Item) (SettleResult, error) {
	if !l.finish() {
		return SettleResult{}, ErrClosed
	}
	g := l.g
	now := g.o.Clock.Now()
	var rec []Item
	for _, a := range actual {
		for _, res := range l.targets(a.Resource) {
			k, ok := g.o.Config.Resolve(res)
			if !ok || k.Binary || (k.SelfCounted && len(k.Counters) == 0) {
				continue // slots and binary resources hold no consumption
			}
			rec = append(rec, Item{Resource: res, Amount: a.Amount})
		}
	}
	freed, err := g.o.Ledger.Settle(l.id, rec, now)
	if err != nil {
		return SettleResult{}, err
	}
	out := SettleResult{Freed: freed}
	for _, it := range rec {
		k, _ := g.o.Config.Resolve(it.Resource)
		if k.DailyWrite > 0 {
			if sum, _ := g.o.Ledger.SettledSince(it.Resource, now.Add(-day)); sum > k.DailyWrite {
				out.Warnings = append(out.Warnings, Reason{"daily_write", it.Resource, now})
			}
		}
		if g.o.Recorder != nil {
			capacity := int64(0)
			if rd, ok := g.o.Meters.Get(it.Resource); ok {
				capacity = rd.Capacity
			} else if k.SelfCounted {
				capacity = k.DefaultCap
			}
			if err := g.o.Recorder.Record(ctx, now, it.Resource, l.caller.Stage, l.caller.Project, it.Amount, capacity); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// Release returns an unused lease (e.g. the session failed to start).
func (l *Lease) Release() error {
	if !l.finish() {
		return ErrClosed
	}
	return l.g.o.Ledger.Release(l.id)
}

func (l *Lease) finish() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return false
	}
	l.done = true
	return true
}

// targets maps a settled resource to the reserved resources it applies to.
func (l *Lease) targets(res string) []string {
	h, a, _, ok := meter.ModelParts(res)
	if !ok {
		return []string{res}
	}
	prefix := "model/" + h + "/" + a + "/"
	var out []string
	for _, it := range l.items {
		if len(it.Resource) > len(prefix) && it.Resource[:len(prefix)] == prefix {
			out = append(out, it.Resource)
		}
	}
	if len(out) == 0 {
		out = []string{res}
	}
	return out
}
