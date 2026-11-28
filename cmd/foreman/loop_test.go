package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serving is a running Serve. The loop reports every tick and heartbeat on a
// channel, so a test advances the fake clock and then waits for exactly the
// event it caused: nothing sleeps.
type serving struct {
	t      *testing.T
	clk    *clock.Fake
	cancel context.CancelFunc
	done   chan error
	ticks  chan bool
	beats  chan struct{}
	out    *syncBuf
}

func serve(t *testing.T, app *App, cfg Config, clk *clock.Fake, tick time.Duration, afterTick func()) *serving {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sv := &serving{t: t, clk: clk, cancel: cancel, done: make(chan error, 1),
		ticks: make(chan bool, 1000), beats: make(chan struct{}, 1000), out: &syncBuf{}}
	t.Cleanup(cancel)
	go func() {
		sv.done <- app.Serve(ctx, cfg, LoopOptions{Tick: tick, Out: sv.out,
			OnTick: func(progressed bool, err error) {
				if err != nil {
					t.Errorf("tick error: %v", err)
				}
				if afterTick != nil {
					afterTick()
				}
				sv.ticks <- progressed
			},
			OnBeat: func() { sv.beats <- struct{}{} }})
	}()
	return sv
}

// step moves the clock by d and waits for the tick that causes.
func (sv *serving) step(d time.Duration) bool {
	sv.t.Helper()
	sv.clk.Advance(d)
	return <-sv.ticks
}

func (sv *serving) stop() {
	sv.t.Helper()
	sv.cancel()
	if err := <-sv.done; err != nil {
		sv.t.Fatalf("Serve = %v, want nil on shutdown", err)
	}
}

// The resident loop carries a requirement to done on a fake clock and the
// scripted harness, writes one log line per tick that did something and none
// for idle ticks, and re-warms the project cache when the stages moved the
// mirror's default branch head (D-088): the cache ends at the new head.
func TestServeRunsRequirementToDoneAndRewarms(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	warmedBefore := launch.WarmedCommit(s.app.cacheDir)
	s.f.pushMain(map[string]string{"OTHER.txt": "moves main\n"}, "main moves")
	head := s.f.remoteSHA("refs/heads/main")
	if warmedBefore == head {
		t.Fatal("fixture: the cache was already warmed at the new head")
	}

	sv := serve(t, s.app, s.cfg, s.clk, time.Second, s.app.Sched.WaitSessions)
	progressedTicks := 0
	if <-sv.ticks { // the first tick runs at once
		progressedTicks++
	}
	for i := 0; i < 80 && !s.allDone(); i++ {
		if sv.step(time.Second) {
			progressedTicks++
		}
	}
	if !s.allDone() {
		t.Fatalf("requirement not done after 80 ticks; rows %v\n%s", s.rows, sv.out.String())
	}
	before := sv.out.String()
	for i := 0; i < 3; i++ {
		if sv.step(time.Second) {
			t.Fatal("an idle daemon reported progress")
		}
	}
	if after := sv.out.String(); after != before {
		t.Errorf("idle ticks wrote log lines: %q", strings.TrimPrefix(after, before))
	}
	sv.stop()

	log := sv.out.String()
	if n := strings.Count(log, "tick: progressed"); n != progressedTicks || n == 0 {
		t.Errorf("%d progress lines for %d ticks that progressed\n%s", n, progressedTicks, log)
	}
	if !strings.Contains(log, "cache-warm") {
		t.Errorf("no re-warm line in the log\n%s", log)
	}
	if got := launch.WarmedCommit(s.app.cacheDir); got != head {
		t.Errorf("cache warmed at %q after the loop, want the default branch head %q", got, head)
	}
}

// The heartbeat is written when the loop starts and then once per minute of
// the injected clock, and an idle loop writes nothing else to the log.
func TestServeHeartbeatEveryMinute(t *testing.T) {
	t.Parallel()
	s := newScenarioSeeded(t, noSeed)
	start := s.clk.Now()
	sv := serve(t, s.app, s.cfg, s.clk, time.Hour, nil)
	<-sv.ticks
	check := func(want time.Time) {
		t.Helper()
		got, err := ReadHeartbeat(s.cfg.Home)
		if err != nil || !got.Equal(want) {
			t.Fatalf("heartbeat = %v (%v), want %v", got, err, want)
		}
	}
	check(start)
	sv.clk.Advance(HeartbeatEvery - time.Second)
	if len(sv.beats) != 0 {
		t.Fatal("heartbeat written before the minute was up")
	}
	sv.clk.Advance(time.Second)
	<-sv.beats
	check(start.Add(HeartbeatEvery))
	sv.clk.Advance(HeartbeatEvery)
	<-sv.beats
	check(start.Add(2 * HeartbeatEvery))
	if got := sv.out.String(); got != "" {
		t.Errorf("an idle loop wrote %q", got)
	}
	sv.stop()
}

// Shutdown stops dispatching and leaves a running session alone: the session
// is still alive after Serve returned, the Run is still running, and a new
// daemon on the same home takes it over and finishes the requirement with no
// second Run (restart recovery, RT-5).
func TestServeShutdownLeavesSessionsDetached(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, true)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	cfg := e.f.config(e.clk, e.ghURL, nil)
	e.clk.Advance(time.Second)
	sv := serve(t, app1, cfg, e.clk, time.Second, nil)
	<-sv.ticks
	runID, _, status, pid := e.runRow(app1, "coder")
	if status != "running" || pid == 0 {
		t.Fatalf("coder run %s: %s pid %d after the first tick, want a running session", runID, status, pid)
	}
	sv.stop()
	if !strings.Contains(sv.out.String(), "shutdown") {
		t.Errorf("no shutdown line in the log: %q", sv.out.String())
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("session %d did not survive the shutdown: %v", pid, err)
	}
	if _, _, st, _ := e.runRow(app1, "coder"); st != "running" {
		t.Errorf("run status after shutdown: %s, want running", st)
	}
	e.crash(app1)

	app2, led2 := e.open()
	e.release()
	app1.Sched.WaitSessions()
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 1)
}

func noSeed(*testing.T, *App, clock.Clock) {}

// A head that failed to warm is not tried again until the head moves, so a
// broken toolchain does not recompile on every tick.
func TestRewarmDoesNotRetryAFailedHead(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	bad := s.cfg
	bad.Path = "/nonexistent" // no go: every warm-up fails
	ctx := context.Background()
	advance := func(name string) {
		t.Helper()
		s.f.pushMain(map[string]string{name: "x\n"}, name)
		if err := s.app.Mirror.Update(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if c, ran := s.app.Rewarm(ctx, bad); ran || !c.OK {
		t.Fatalf("up-to-date cache: ran=%v check=%v, want a quiet no-op", ran, c)
	}
	advance("a.txt")
	if c, ran := s.app.Rewarm(ctx, bad); !ran || c.OK {
		t.Fatalf("first attempt: ran=%v check=%v, want a failed warm-up", ran, c)
	}
	if _, ran := s.app.Rewarm(ctx, bad); ran {
		t.Error("the same failing head was warmed again")
	}
	advance("b.txt")
	if _, ran := s.app.Rewarm(ctx, bad); !ran {
		t.Error("a new head was not tried")
	}
	if _, ran := s.app.Rewarm(ctx, s.cfg); ran {
		t.Error("the failed head was retried although the toolchain came back")
	}
	advance("c.txt")
	if c, ran := s.app.Rewarm(ctx, s.cfg); !ran || !c.OK {
		t.Errorf("working toolchain after the failures: ran=%v check=%v", ran, c)
	}
}
