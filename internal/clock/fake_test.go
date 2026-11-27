package clock

import (
	"sync"
	"testing"
	"time"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func fired(c <-chan time.Time) (time.Time, bool) {
	select {
	case v := <-c:
		return v, true
	default:
		return time.Time{}, false
	}
}

func TestFakeTimerFiresOnlyWhenDeadlinePassed(t *testing.T) {
	f := NewFake(start)
	tm := f.NewTimer(10 * time.Second)
	f.Advance(9 * time.Second)
	if _, ok := fired(tm.C()); ok {
		t.Fatal("fired before deadline")
	}
	f.Advance(time.Second)
	v, ok := fired(tm.C())
	if !ok {
		t.Fatal("did not fire at deadline")
	}
	if want := start.Add(10 * time.Second); !v.Equal(want) {
		t.Errorf("fired value = %v, want %v", v, want)
	}
	if got := f.Now(); !got.Equal(start.Add(10 * time.Second)) {
		t.Errorf("Now = %v", got)
	}
}

func TestFakeAfter(t *testing.T) {
	f := NewFake(start)
	c := f.After(time.Minute)
	f.Advance(59 * time.Second)
	if _, ok := fired(c); ok {
		t.Fatal("fired early")
	}
	f.Advance(2 * time.Second)
	if _, ok := fired(c); !ok {
		t.Fatal("did not fire")
	}
	if got := f.Now(); !got.Equal(start.Add(61 * time.Second)) {
		t.Errorf("Now = %v", got)
	}
}

func TestFakeStopPreventsFiring(t *testing.T) {
	f := NewFake(start)
	tm := f.NewTimer(time.Second)
	if !tm.Stop() {
		t.Error("Stop on pending timer = false")
	}
	f.Advance(time.Hour)
	if _, ok := fired(tm.C()); ok {
		t.Fatal("stopped timer fired")
	}
	if tm.Stop() {
		t.Error("second Stop = true")
	}
}

func TestFakeResetReschedules(t *testing.T) {
	f := NewFake(start)
	tm := f.NewTimer(10 * time.Second)
	f.Advance(5 * time.Second)
	if !tm.Reset(10 * time.Second) {
		t.Error("Reset on pending timer = false")
	}
	f.Advance(9 * time.Second) // 14s: original deadline passed, new is 15s
	if _, ok := fired(tm.C()); ok {
		t.Fatal("fired at old deadline")
	}
	f.Advance(time.Second)
	if _, ok := fired(tm.C()); !ok {
		t.Fatal("did not fire at new deadline")
	}
	if tm.Reset(time.Second) {
		t.Error("Reset on fired timer = true")
	}
	f.Advance(time.Second)
	if _, ok := fired(tm.C()); !ok {
		t.Fatal("did not fire after reset of expired timer")
	}
}

func TestFakeFiresInDeadlineOrder(t *testing.T) {
	f := NewFake(start)
	a := f.NewTimer(30 * time.Second)
	b := f.NewTimer(10 * time.Second)
	c := f.NewTimer(20 * time.Second)
	f.Advance(15 * time.Second)
	if _, ok := fired(b.C()); !ok {
		t.Fatal("b should have fired")
	}
	if _, ok := fired(c.C()); ok {
		t.Fatal("c fired early")
	}
	f.Advance(time.Minute)
	vc, okc := fired(c.C())
	va, oka := fired(a.C())
	if !okc || !oka {
		t.Fatal("c and a should have fired")
	}
	if !vc.Equal(start.Add(20*time.Second)) || !va.Equal(start.Add(30*time.Second)) {
		t.Errorf("fire times c=%v a=%v", vc, va)
	}
}

func TestFakeNonPositiveFiresImmediately(t *testing.T) {
	f := NewFake(start)
	if _, ok := fired(f.NewTimer(0).C()); !ok {
		t.Fatal("zero timer did not fire")
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	f := NewFake(start)
	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		c := f.After(time.Duration(i) * time.Second)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-c
		}()
	}
	for i := 0; i < 50; i++ {
		f.Advance(time.Second)
		_ = f.Now()
	}
	wg.Wait()
}

func TestRealClockSatisfiesInterface(t *testing.T) {
	var c Clock = Real()
	tm := c.NewTimer(time.Hour)
	if !tm.Stop() {
		t.Error("Stop on pending real timer = false")
	}
	if c.Now().IsZero() {
		t.Error("zero Now")
	}
}
