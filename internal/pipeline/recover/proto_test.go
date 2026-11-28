package recover

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
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
	p, err := Inspect(&fakeProcs{}, dir, now)
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
	if err := DropLock(&fakeProcs{}, dir); err != nil {
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

// fakeProcs is Procs over an in-memory pid table.
type fakeProcs struct {
	pids  map[string]PidFile // run dir -> pid.json
	alive func(PidFile) bool
}

func (f *fakeProcs) ReadPid(runDir string) (PidFile, bool, error) {
	pf, ok := f.pids[runDir]
	return pf, ok, nil
}
func (f *fakeProcs) Alive(pf PidFile) bool { return f.alive != nil && f.alive(pf) }
func (f *fakeProcs) ForgetPid(runDir string) error {
	delete(f.pids, runDir)
	return nil
}
func (f *fakeProcs) KillRun(string) ([]int, error) { return nil, nil }

// Inspect asks the injected Procs: a pid.json whose process is the session is
// Alive, one whose process is gone or recycled is Exited, and DropLock also
// forgets the pid file.
func TestInspectUsesInjectedProcs(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	dir := runDir(t)
	live := PidFile{Pid: 7, StartTime: "t0"}
	procs := &fakeProcs{pids: map[string]PidFile{dir: live}, alive: func(pf PidFile) bool { return pf == live }}
	if p, err := Inspect(procs, dir, clk.Now()); err != nil || p.Phase != Alive || p.Pid != live {
		t.Errorf("live process: %+v, %v", p, err)
	}
	procs.pids[dir] = PidFile{Pid: 7, StartTime: "other"}
	if p, err := Inspect(procs, dir, clk.Now()); err != nil || p.Phase != Exited {
		t.Errorf("recycled pid: %+v, %v", p, err)
	}
	if err := DropLock(procs, dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := procs.pids[dir]; ok {
		t.Error("DropLock left pid.json behind")
	}
}
