package clock

import (
	"sync"
	"time"
)

// Fake is a manually advanced Clock. It never sleeps; time moves only
// through Advance. It is safe for concurrent use.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	seq    uint64
	timers []*fakeTimer
}

// NewFake returns a Fake whose current time is start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After is equivalent to NewTimer(d).C().
func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

// NewTimer creates a timer that fires once the clock reaches now+d.
// A non-positive d fires immediately.
func (f *Fake) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{f: f, c: make(chan time.Time, 1)}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedule(t, d)
	return t
}

// Advance moves the clock forward by d, firing every timer whose deadline
// is reached, in deadline order (creation order breaks ties). While a timer
// fires, Now reports that timer's deadline.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.now.Add(d)
	for {
		var next *fakeTimer
		for _, t := range f.timers {
			if t.deadline.After(target) {
				continue
			}
			if next == nil || t.deadline.Before(next.deadline) ||
				(t.deadline.Equal(next.deadline) && t.seq < next.seq) {
				next = t
			}
		}
		if next == nil {
			break
		}
		if next.deadline.After(f.now) {
			f.now = next.deadline
		}
		f.remove(next)
		next.fire(f.now)
	}
	if target.After(f.now) {
		f.now = target
	}
}

// schedule must be called with f.mu held.
func (f *Fake) schedule(t *fakeTimer, d time.Duration) {
	f.seq++
	t.seq = f.seq
	t.deadline = f.now.Add(d)
	if d <= 0 {
		t.fire(f.now)
		return
	}
	f.timers = append(f.timers, t)
}

// remove reports whether t was pending. f.mu must be held.
func (f *Fake) remove(t *fakeTimer) bool {
	for i, x := range f.timers {
		if x == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			return true
		}
	}
	return false
}

type fakeTimer struct {
	f        *Fake
	c        chan time.Time
	deadline time.Time
	seq      uint64
}

func (t *fakeTimer) fire(at time.Time) {
	select {
	case t.c <- at:
	default:
	}
}

func (t *fakeTimer) drain() {
	select {
	case <-t.c:
	default:
	}
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

// Stop prevents the timer from firing and reports whether it was pending.
// Like time.Timer since Go 1.23, it discards an unreceived stale value.
func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.f.remove(t)
	t.drain()
	return was
}

// Reset reschedules the timer to fire d from now and reports whether it was
// pending. An unreceived stale value is discarded.
func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.f.remove(t)
	t.drain()
	t.f.schedule(t, d)
	return was
}
