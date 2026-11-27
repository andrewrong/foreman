package recover

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

func runDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(shortHome(t), "runs", "r1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func phaseOf(t *testing.T, dir string, now time.Time) Probe {
	t.Helper()
	p, err := Inspect(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The launch.lock rules of the reconciler table: one lock per run directory,
// younger than 60 s means wait, older means the start failed.
func TestLaunchLockProtocol(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	dir := runDir(t)
	if p := phaseOf(t, dir, clk.Now()); p.Phase != NotStarted {
		t.Fatalf("empty run dir: %v", p.Phase)
	}
	if err := TakeLock(dir, clk.Now(), 1); err != nil {
		t.Fatal(err)
	}
	if err := TakeLock(dir, clk.Now(), 1); !errors.Is(err, ErrLockExists) {
		t.Fatalf("second TakeLock = %v, want ErrLockExists (GEM-6)", err)
	}
	clk.Advance(StartGrace - time.Second)
	if p := phaseOf(t, dir, clk.Now()); p.Phase != Starting || p.Attempt != 1 {
		t.Errorf("59 s old lock: %+v, want Starting", p)
	}
	clk.Advance(time.Second)
	if p := phaseOf(t, dir, clk.Now()); p.Phase != StartFailed {
		t.Errorf("60 s old lock without pid.json: %+v, want StartFailed", p)
	}
	if err := DropLock(dir); err != nil {
		t.Fatal(err)
	}
	if p := phaseOf(t, dir, clk.Now()); p.Phase != NotStarted {
		t.Errorf("after DropLock: %v", p.Phase)
	}
	if err := TakeLock(dir, clk.Now(), 2); err != nil {
		t.Errorf("TakeLock after DropLock: %v", err)
	}
}

func TestLockCutShortByACrashIsReclaimed(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	dir := runDir(t)
	if err := os.WriteFile(filepath.Join(dir, LockFileName), nil, 0o600); err != nil { // created, never written
		t.Fatal(err)
	}
	if p := phaseOf(t, dir, clk.Now()); p.Phase != StartFailed {
		t.Errorf("empty launch.lock: %v, want StartFailed", p.Phase)
	}
}

func TestInspectTellsLiveFromGoneAndRecycledPid(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	dir := runDir(t)
	own, _, ok := launch.ProcInfo(os.Getpid())
	if !ok {
		t.Skip("ps cannot read this process")
	}
	write := func(pid int, start string) {
		body := []byte(`{"pid":` + strconv.Itoa(pid) + `,"start_time":"` + start + `"}`)
		if err := os.WriteFile(filepath.Join(dir, launch.PidFileName), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(os.Getpid(), own)
	if p := phaseOf(t, dir, clk.Now()); p.Phase != Alive {
		t.Errorf("live process with matching start time: %v", p.Phase)
	}
	write(os.Getpid(), "Thu Jan  1 00:00:00 1970") // same pid, other start time: pid reuse
	if p := phaseOf(t, dir, clk.Now()); p.Phase != Exited {
		t.Errorf("recycled pid: %v, want Exited", p.Phase)
	}
	write(1<<22+12345, own) // no such process
	if p := phaseOf(t, dir, clk.Now()); p.Phase != Exited {
		t.Errorf("missing pid: %v, want Exited", p.Phase)
	}
}
