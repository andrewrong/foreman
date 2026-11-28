package main

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/pipeline/sched"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// stubAdapter builds an invocation that is never run.
type stubAdapter struct{}

func (stubAdapter) Name() string                   { return "stub" }
func (stubAdapter) Capabilities() api.Capabilities { return api.Capabilities{} }
func (stubAdapter) BuildInvocation(api.RunSpec) (api.Invocation, error) {
	return api.Invocation{Argv: []string{"true"}, Timeout: time.Minute}, nil
}
func (stubAdapter) ParseStream(io.Reader) iter.Seq[api.Event] { return func(func(api.Event) bool) {} }

// A session that is already starting must not consult runExtra: a script
// queue handing out one script per call would otherwise be drained by polls.
// The stage consults it once per launch, in Start, before launch.lock is taken.
func TestDriveSessionPollsDoNotConsultRunExtra(t *testing.T) {
	t.Parallel()
	short := func() string { // t.TempDir is too long for the 70-byte socket path limit on macOS
		d, err := os.MkdirTemp("/tmp", "fms")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		return d
	}
	calls := 0
	errStop := errors.New("stop before launch")
	stage := sessionStage{
		launcher: launch.Launcher{PassThrough: true},
		policy: policy.Inputs{DenyWrite: []string{short()}, Path: "/usr/bin",
			SessionGit: policy.SessionGit{Name: "s", Email: "s@users.noreply.example", DefaultBranch: "main"}},
		newAdapter: func(api.Role, *gate.Lease) (api.Adapter, error) { return stubAdapter{}, nil },
		runExtra:   func(api.Role, string) map[string]string { calls++; return nil },
		finish:     codeFinish,
	}
	job := sched.SessionJob{RunID: "r1", StepID: "S1", Role: sched.RoleCoder, RunDir: short(), SessionRepo: short()}
	if _, err := stage.Start(context.Background(), job, func() error { return errStop }); !errors.Is(err, errStop) {
		t.Fatalf("Start error = %v, want the beforeLaunch error", err)
	}
	if calls != 1 {
		t.Errorf("runExtra called %d times by one launch, want 1", calls)
	}
	if _, err := stage.Collect(context.Background(), job, nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("runExtra called %d times after Collect, want still 1", calls)
	}
}

// recover.Inspect over the real launcher: a pid.json whose process is alive
// with the recorded start time is Alive; the same pid with another start time
// (pid reuse) and a pid with no process are Exited.
func TestInspectTellsLiveFromGoneAndRecycledPid(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	dir := t.TempDir()
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
	phase := func() recover.Phase {
		p, err := recover.Inspect(launchProcs{}, dir, clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		return p.Phase
	}
	write(os.Getpid(), own)
	if p := phase(); p != recover.Alive {
		t.Errorf("live process with matching start time: %v", p)
	}
	write(os.Getpid(), "Thu Jan  1 00:00:00 1970") // same pid, other start time: pid reuse
	if p := phase(); p != recover.Exited {
		t.Errorf("recycled pid: %v, want Exited", p)
	}
	write(1<<22+12345, own) // no such process
	if p := phase(); p != recover.Exited {
		t.Errorf("missing pid: %v, want Exited", p)
	}
}
