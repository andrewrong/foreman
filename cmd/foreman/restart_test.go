package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

// crashEnv is one project whose daemon is killed and restarted on the same
// FOREMAN_HOME and the same (fake) GitHub.
type crashEnv struct {
	t      *testing.T
	f      *fixture
	clk    *clock.Fake
	gh     *fakeGitHub
	ghURL  string
	fifo   string
	events []string // "<step> <event>" of every Transition, across restarts
}

// newCrashEnv builds the fixture. With block set, the coder script waits on a
// FIFO before it writes anything, so the test decides when the session ends.
func newCrashEnv(t *testing.T, block bool) *crashEnv {
	t.Helper()
	e := &crashEnv{t: t, f: newFixture(t), clk: clock.NewFake(time.Unix(1_800_000_000, 0))}
	e.gh, e.ghURL = newFakeGitHub(t, func(branch string) string { return e.f.remoteSHA("refs/heads/" + branch) })
	if !block {
		return e
	}
	e.fifo = filepath.Join(filepath.Dir(e.f.home), "gate")
	if err := syscall.Mkfifo(e.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	path := e.f.scripts[api.RoleCoder]
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sc scripted.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	sc.Actions = append([]scripted.Action{{Block: e.fifo}}, sc.Actions...)
	if raw, err = json.Marshal(sc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// open starts a daemon: a fresh Compose with an empty in-memory ledger, as a
// new process would have.
func (e *crashEnv) open() (*App, *ledger.Ledger) {
	e.t.Helper()
	cfg := e.f.config(e.clk, e.ghURL, func(step string, ev state.Event, _ state.Outcome) {
		e.events = append(e.events, step+" "+ev.Name)
	})
	app, err := Compose(context.Background(), cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { app.Close() })
	return app, cfg.Ledger
}

// crash stops the daemon without cleaning up after it: no session is killed
// and no lease is settled (Close only gives back the database handles and the
// directory lock, which the operating system does on a real crash).
func (e *crashEnv) crash(app *App) {
	e.t.Helper()
	if err := app.Close(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *crashEnv) tick(app *App) {
	e.t.Helper()
	e.clk.Advance(time.Second)
	if _, err := app.Sched.Tick(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// release lets blocked coder sessions go on: opening the FIFO for writing
// completes the reader's open, closing it gives the reader its end of file.
func (e *crashEnv) release() {
	e.t.Helper()
	w, err := os.OpenFile(e.fifo, os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	w.Close()
}

// drive ticks until cond holds, at most 60 times. After each Tick it waits
// for the sessions this daemon started to really exit (a goroutine on
// os.Process.Wait, no sleeping).
func (e *crashEnv) drive(app *App, cond func() bool) {
	e.t.Helper()
	for i := 0; i < 60; i++ {
		if cond() {
			return
		}
		e.tick(app)
		app.Sched.WaitSessions()
	}
	if !cond() {
		e.t.Fatalf("condition not reached in 60 ticks; events: %v", e.events)
	}
}

func (e *crashEnv) allDone(app *App) func() bool {
	return func() bool {
		var open int
		query(e.t, app, `SELECT COUNT(*) FROM steps WHERE state <> 'done'`, &open)
		return open == 0
	}
}

func (e *crashEnv) runRow(app *App, role string) (id, dir, status string, pid int) {
	e.t.Helper()
	var p *int
	err := app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id, dir, status, pid FROM runs WHERE role = ? ORDER BY id DESC LIMIT 1`, role).Scan(&id, &dir, &status, &p)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if p != nil {
		pid = *p
	}
	return
}

func (e *crashEnv) count(app *App, q string, args ...any) int {
	e.t.Helper()
	var n int
	err := app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) })
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// assertOnce checks the invariants the restart must keep: the requirement is
// done with one PR created and merged, every stage ran exactly once (no
// duplicate Run), nothing is left running and the ledger holds nothing.
func (e *crashEnv) assertOnce(app *App, led *ledger.Ledger, coderRuns int) {
	e.t.Helper()
	e.gh.mu.Lock()
	created, merges := len(e.gh.created), len(e.gh.merges)
	e.gh.mu.Unlock()
	if created != 1 || merges != 1 {
		e.t.Errorf("PRs created %d, merges %d, want 1 and 1", created, merges)
	}
	if n := e.count(app, `SELECT COUNT(*) FROM pull_requests WHERE state = 'merged'`); n != 1 {
		e.t.Errorf("merged pull request rows: %d", n)
	}
	want := map[string]int{"coder": coderRuns, "check": 1, "reviewer": 1, "ci_watch": 1, "merge": 1}
	for role, n := range want {
		if got := e.count(app, `SELECT COUNT(*) FROM runs WHERE role = ?`, role); got != n {
			e.t.Errorf("%s runs: %d, want %d", role, got, n)
		}
	}
	if n := e.count(app, `SELECT COUNT(*) FROM runs WHERE status <> 'ended'`); n != 0 {
		e.t.Errorf("%d runs not ended", n)
	}
	if n := e.count(app, `SELECT COUNT(*) FROM runs WHERE reserved <> '[]'`); n != 0 {
		e.t.Errorf("%d ended runs still list reservations", n)
	}
	if open := led.Open(); len(open) != 0 {
		e.t.Errorf("ledger not empty after the lane: %+v", open)
	}
}

func openItems(led *ledger.Ledger) []ledger.Item {
	var out []ledger.Item
	for _, r := range led.Open() {
		out = append(out, r.Items...)
	}
	return out
}

func sameItems(a, b []ledger.Item) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// coderStarted ticks the first daemon until the coder's session is running.
func (e *crashEnv) coderStarted(app *App) (runID, dir string, pid int) {
	e.t.Helper()
	e.tick(app)
	runID, dir, status, pid := e.runRow(app, "coder")
	if status != "running" || pid == 0 {
		e.t.Fatalf("coder run %s: status %s pid %d after one Tick, want a running session with its pid recorded", runID, status, pid)
	}
	return runID, dir, pid
}

// TestRestartRecovery kills the daemon in the middle of a coder session and of
// waiting(ci), reopens the same FOREMAN_HOME and keeps ticking: the
// requirement reaches done without a second PR, a second Run for a stage, or
// an inconsistent ledger.
func TestRestartRecovery(t *testing.T) {
	t.Parallel()
	t.Run("coder session still alive at the restart", restartCoderAlive)
	t.Run("coder session ended before the restart", restartCoderEnded)
	t.Run("launch.lock without pid.json is relaunched once after 60 seconds", restartLaunchLockRelaunch)
	t.Run("session killed with no result is an infra event, then the stage runs again", restartKilledNoResult)
	t.Run("daemon killed while waiting for CI", restartKilledInCI)
}

func restartCoderAlive(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, true)
	app1, led1 := e.open()
	seed(t, app1, e.clk)
	runID, dir, pid := e.coderStarted(app1)
	held := openItems(led1)
	if len(held) == 0 {
		t.Fatal("no reservation held while the session runs")
	}
	e.crash(app1)

	app2, led2 := e.open()
	if got := openItems(led2); !sameItems(got, held) {
		t.Errorf("ledger rebuilt from runs.reserved holds %+v, want %+v", got, held)
	}
	for i := 0; i < 3; i++ { // the session is alive: nothing to do, nothing launched
		e.tick(app2)
	}
	if id, _, status, p := e.runRow(app2, "coder"); id != runID || status != "running" || p != pid {
		t.Errorf("coder run after restart: %s %s pid %d, want %s running pid %d", id, status, p, runID, pid)
	}
	if pf, ok, err := launch.ReadPidFile(dir); err != nil || !ok || pf.Pid != pid {
		t.Errorf("pid.json after restart: %+v ok=%v err=%v, want the original session %d", pf, ok, err, pid)
	}
	e.release()
	app1.Sched.WaitSessions() // the old daemon's reaper: a real process exit
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 1)
}

func restartCoderEnded(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, true)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	_, dir, _ := e.coderStarted(app1)
	e.release()
	app1.Sched.WaitSessions()
	if _, err := os.Stat(filepath.Join(dir, "result.json")); err != nil {
		t.Fatalf("session ended without result.json: %v", err)
	}
	e.crash(app1) // result.json is on disk, its event was never applied

	app2, led2 := e.open()
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 1)
}

func restartLaunchLockRelaunch(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, true)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	_, dir, pid := e.coderStarted(app1)
	// The launcher died before it wrote pid.json: lose the session and the file.
	if _, err := launch.KillRun(dir); err != nil {
		t.Fatal(err)
	}
	app1.Sched.WaitSessions()
	if err := os.Remove(filepath.Join(dir, launch.PidFileName)); err != nil {
		t.Fatal(err)
	}
	e.crash(app1)

	app2, led2 := e.open()
	e.tick(app2) // the lock is a second old: the launcher may still be starting
	if _, ok, _ := launch.ReadPidFile(dir); ok {
		t.Fatal("session relaunched while launch.lock was younger than 60 s")
	}
	e.clk.Advance(recover.StartGrace)
	e.tick(app2) // now it is a failed start: lock removed, one new session
	pf, ok, err := launch.ReadPidFile(dir)
	if err != nil || !ok || pf.Pid == pid {
		t.Fatalf("relaunch: pid.json %+v ok=%v err=%v (old pid %d)", pf, ok, err, pid)
	}
	e.release()
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 1)
}

func restartKilledNoResult(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, true)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	firstID, dir, _ := e.coderStarted(app1)
	if _, err := launch.KillRun(dir); err != nil {
		t.Fatal(err)
	}
	app1.Sched.WaitSessions()
	e.crash(app1)

	app2, led2 := e.open()
	e.tick(app2)
	var outcome string
	query(t, app2, `SELECT outcome FROM runs WHERE id = '`+firstID+`'`, &outcome)
	if outcome != "infra" {
		t.Fatalf("run %s outcome %q, want infra", firstID, outcome)
	}
	e.clk.Advance(24 * time.Hour) // past the infra wait deadline
	for i := 0; i < 60 && e.count(app2, `SELECT COUNT(*) FROM runs WHERE role = 'coder'`) < 2; i++ {
		e.tick(app2)
	}
	e.release() // the second coder session
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 2)
}

func restartKilledInCI(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, false)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	inCI := func() bool {
		var st, reason string
		query(t, app1, `SELECT state, COALESCE(reason, '') FROM steps WHERE id = 'R-m1/S2'`, &st, &reason)
		e.gh.mu.Lock()
		defer e.gh.mu.Unlock()
		return st == "waiting" && reason == "ci" && len(e.gh.created) == 1
	}
	e.drive(app1, inCI)
	e.crash(app1)

	app2, led2 := e.open()
	e.drive(app2, e.allDone(app2))
	e.assertOnce(app2, led2, 1)
}
