package meter

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Reading is one measurement of a resource (RC-1 reading_fields).
type Reading struct {
	Resource   string
	Capacity   int64 // 0 when unknown
	Used       int64
	ResetsAt   time.Time // zero when unknown
	MeasuredAt time.Time
	Source     string
}

// Remaining is capacity - used.
func (r Reading) Remaining() int64 { return r.Capacity - r.Used }

// Meter actively measures one resource. Read must not consume the resource.
// Passive resources (model windows) have no Meter: their readings arrive via
// Store.Observe / the Ingest helpers.
type Meter interface {
	Read(ctx context.Context) (Reading, error)
}

// Store keeps the latest Reading per resource, exhaustion marks and the
// registered active Meters. Safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	readings  map[string]Reading
	exhausted map[string]time.Time
	meters    map[string]Meter
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		readings:  map[string]Reading{},
		exhausted: map[string]time.Time{},
		meters:    map[string]Meter{},
	}
}

// Observe records a reading. An older reading never replaces a newer one.
func (s *Store) Observe(r Reading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.readings[r.Resource]; ok && cur.MeasuredAt.After(r.MeasuredAt) {
		return
	}
	s.readings[r.Resource] = r
}

// Get returns the latest reading of a resource.
func (s *Store) Get(resource string) (Reading, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.readings[resource]
	return r, ok
}

// WithPrefix returns all readings whose resource starts with prefix, sorted
// by resource name.
func (s *Store) WithPrefix(prefix string) []Reading {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Reading
	for k, r := range s.readings {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

// MarkExhausted marks a resource exhausted until resetsAt (RC-1). harness and
// forge call it when they receive a quota error.
func (s *Store) MarkExhausted(resource string, resetsAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exhausted[resource] = resetsAt
}

// ExhaustedUntil returns the exhaustion deadline, if set and still in the
// future at now. Expired marks are cleared.
func (s *Store) ExhaustedUntil(resource string, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.exhausted[resource]
	if ok && !now.Before(t) {
		delete(s.exhausted, resource)
		return time.Time{}, false
	}
	return t, ok
}

// Register attaches an active Meter for a resource.
func (s *Store) Register(resource string, m Meter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meters[resource] = m
}

// ErrNoMeter is returned by Refresh for resources without an active Meter.
var ErrNoMeter = errors.New("meter: no active meter registered")

// Refresh re-measures a resource through its registered Meter and stores the
// reading.
func (s *Store) Refresh(ctx context.Context, resource string) error {
	s.mu.Lock()
	m := s.meters[resource]
	s.mu.Unlock()
	if m == nil {
		return ErrNoMeter
	}
	r, err := m.Read(ctx)
	if err != nil {
		return err
	}
	if r.Resource == "" {
		r.Resource = resource
	}
	s.Observe(r)
	return nil
}
