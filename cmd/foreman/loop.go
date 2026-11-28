package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HeartbeatEvery is the heartbeat period (D-047 ③: every minute).
const HeartbeatEvery = time.Minute

// HeartbeatFile is where foremand writes its heartbeat. It is a file, not a
// table: the database directory is locked by the running daemon, so a
// `foreman status` process could not open the store to read it.
func HeartbeatFile(home string) string { return filepath.Join(home, "heartbeat") }

// LoopOptions configures Serve.
type LoopOptions struct {
	// Tick is the pause between scheduler rounds. It must be positive.
	Tick time.Duration
	// Heartbeat is the heartbeat period; zero means HeartbeatEvery.
	Heartbeat time.Duration
	// Out receives one line per tick in which something happened.
	Out io.Writer
	// OnTick, if set, is called after every tick (also idle ones) once the
	// next tick is scheduled; tests use it to step the loop.
	OnTick func(progressed bool, err error)
	// OnBeat, if set, is called after every heartbeat write.
	OnBeat func()
}

// Serve is the resident loop of foremand (D-011): it ticks the scheduler
// every o.Tick and writes the heartbeat every o.Heartbeat, both on the
// injected clock, until ctx is done. Shutdown is orderly: no further tick
// is started, sessions are left running and detached (the next daemon takes
// them over, RT-5), nothing is killed.
//
// A tick that has begun is not interrupted: its context is detached from
// ctx, so a signal never cuts a git operation, a push or a Transition in
// half. Sessions are started detached (StartDetached takes no context), so
// they survive either way. A second signal kills the process at once.
//
// A failing tick does not stop the loop; its error is logged once for as
// long as it repeats unchanged.
func (a *App) Serve(ctx context.Context, cfg Config, o LoopOptions) error {
	if o.Tick <= 0 {
		return fmt.Errorf("serve: tick interval must be positive, got %s", o.Tick)
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = HeartbeatEvery
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	clk := cfg.Clock
	tick, beat := clk.NewTimer(0), clk.NewTimer(o.Heartbeat)
	defer tick.Stop()
	defer beat.Stop()
	a.beat(cfg, o.Out) // a fresh daemon is alive from its first moment
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(o.Out, "shutdown: signal received, sessions left running")
			return nil
		case <-beat.C():
			a.beat(cfg, o.Out)
			beat.Reset(o.Heartbeat)
			if o.OnBeat != nil {
				o.OnBeat()
			}
		case <-tick.C():
			progressed, err := a.tickOnce(context.WithoutCancel(ctx), cfg, o.Out, &lastErr)
			tick.Reset(o.Tick)
			if o.OnTick != nil {
				o.OnTick(progressed, err)
			}
		}
	}
}

// tickOnce runs one scheduler round and, when it did something, the re-warm
// check (D-088: the merge stage moved the default branch of the mirror).
func (a *App) tickOnce(ctx context.Context, cfg Config, out io.Writer, lastErr *string) (bool, error) {
	progressed, err := a.Sched.Tick(ctx)
	if err != nil {
		if msg := err.Error(); msg != *lastErr {
			fmt.Fprintf(out, "%s tick: error: %s\n", cfg.Clock.Now().UTC().Format(time.RFC3339), msg)
			*lastErr = msg
		}
	} else {
		*lastErr = ""
	}
	if progressed {
		fmt.Fprintf(out, "%s tick: progressed\n", cfg.Clock.Now().UTC().Format(time.RFC3339))
		if c, ran := a.Rewarm(ctx, cfg); ran {
			fmt.Fprintln(out, c)
		}
	}
	return progressed, err
}

// beat writes the heartbeat (the clock's time, RFC 3339 UTC) atomically. A
// failed write is reported and the daemon goes on: a missing heartbeat is
// what tells the owner, and it is the owner's signal, not a reason to stop.
func (a *App) beat(cfg Config, out io.Writer) {
	path := HeartbeatFile(cfg.Home)
	tmp := path + ".tmp"
	body := cfg.Clock.Now().UTC().Format(time.RFC3339) + "\n"
	err := os.WriteFile(tmp, []byte(body), 0o600)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		fmt.Fprintf(out, "heartbeat: %v\n", err)
	}
}

// ReadHeartbeat returns the time of the last heartbeat under home.
func ReadHeartbeat(home string) (time.Time, error) {
	raw, err := os.ReadFile(HeartbeatFile(home))
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
}
