// Package ledger is the in-process reservation book (resources.yaml RC-2).
// It is global across projects and has no table of its own: foremand rebuilds
// it at start from the reserved amounts of running Runs (Rebuild).
//
//	available = capacity - used - sum(unsettled reservations)
package ledger

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Item is an amount of one resource.
type Item struct {
	Resource string
	Amount   int64
}

// Reservation is an unsettled hold. ID is caller-chosen (a Run id after a
// restart); Tag is opaque to the ledger.
type Reservation struct {
	ID    string
	Items []Item
	Tag   string
	At    time.Time
}

type settlement struct {
	resource string
	amount   int64
	at       time.Time
}

// Errors.
var (
	ErrDuplicate = errors.New("ledger: reservation id already exists")
	ErrUnknown   = errors.New("ledger: no such reservation")
)

// Ledger is safe for concurrent use.
type Ledger struct {
	mu       sync.Mutex
	open     map[string]Reservation
	settled  []settlement
	reserved map[string]int64
}

// New returns an empty Ledger.
func New() *Ledger {
	return &Ledger{open: map[string]Reservation{}, reserved: map[string]int64{}}
}

// Reserve adds a reservation. It does not check capacity: that is the Gate's
// job, which calls Reserve while holding its own lock.
func (l *Ledger) Reserve(r Reservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.open[r.ID]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, r.ID)
	}
	l.add(r, 1)
	return nil
}

func (l *Ledger) add(r Reservation, sign int64) {
	if sign > 0 {
		r.Items = append([]Item(nil), r.Items...)
		l.open[r.ID] = r
	} else {
		delete(l.open, r.ID)
	}
	for _, it := range r.Items {
		l.reserved[it.Resource] += sign * it.Amount
	}
}

// Release drops a reservation without recording consumption.
func (l *Ledger) Release(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.open[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	l.add(r, -1)
	return nil
}

// Settle closes a reservation and records the actual consumption at time at.
// The returned items are the per-resource difference reserved - actual that
// went back to available (negative when actual exceeded the reservation).
// Items in actual that were not reserved are recorded too.
func (l *Ledger) Settle(id string, actual []Item, at time.Time) ([]Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.open[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, id)
	}
	l.add(r, -1)
	diff := map[string]int64{}
	for _, it := range r.Items {
		diff[it.Resource] += it.Amount
	}
	for _, it := range actual {
		diff[it.Resource] -= it.Amount
		if it.Amount > 0 {
			l.settled = append(l.settled, settlement{it.Resource, it.Amount, at})
		}
	}
	out := make([]Item, 0, len(diff))
	for k, v := range diff {
		out = append(out, Item{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

// Reserved returns the total of unsettled reservations for a resource.
func (l *Ledger) Reserved(resource string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reserved[resource]
}

// SettledSince sums consumption settled at or after since and returns the
// time of the oldest such settlement (zero if none).
func (l *Ledger) SettledSince(resource string, since time.Time) (int64, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sum int64
	var oldest time.Time
	for _, s := range l.settled {
		if s.resource == resource && !s.at.Before(since) {
			sum += s.amount
			if oldest.IsZero() || s.at.Before(oldest) {
				oldest = s.at
			}
		}
	}
	return sum, oldest
}

// Prune forgets settlements older than before.
func (l *Ledger) Prune(before time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.settled[:0]
	for _, s := range l.settled {
		if !s.at.Before(before) {
			kept = append(kept, s)
		}
	}
	l.settled = kept
}

// Open returns the unsettled reservations sorted by ID.
func (l *Ledger) Open() []Reservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Reservation, 0, len(l.open))
	for _, r := range l.open {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Rebuild replaces the whole ledger content with reservations recovered from
// running Runs (foremand start, RT-5). Settlement history is dropped.
func (l *Ledger) Rebuild(rs []Reservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open = map[string]Reservation{}
	l.reserved = map[string]int64{}
	l.settled = nil
	for _, r := range rs {
		if _, dup := l.open[r.ID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicate, r.ID)
		}
		l.add(r, 1)
	}
	return nil
}
