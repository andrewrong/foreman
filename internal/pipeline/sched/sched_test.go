package sched

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/spec/model"
)

func TestNewReportsEveryMissingDependency(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Fatal("New accepted empty deps")
	}
	for _, name := range []string{"DB", "Engine", "Clock", "Gate", "Meters", "Home", "Forge", "Workspace", "Procs", "Code", "Review", "Check", "Integrate", "Merge"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// countingStage is a SessionStage that only counts launches.
type countingStage struct{ starts int }

func (c *countingStage) Timeout() time.Duration { return time.Minute }
func (c *countingStage) Start(context.Context, SessionJob, func() error) (Proc, error) {
	c.starts++
	return nil, nil
}
func (c *countingStage) Collect(context.Context, SessionJob, Proc, bool) (model.Event, error) {
	return model.Event{}, nil
}

// noPids is a recover.Procs without any process.
type noPids struct{}

func (noPids) ReadPid(string) (recover.PidFile, bool, error) { return recover.PidFile{}, false, nil }
func (noPids) Alive(recover.PidFile) bool                    { return false }
func (noPids) ForgetPid(string) error                        { return nil }
func (noPids) KillRun(string) ([]int, error)                 { return nil, nil }

// A session that is already starting must not be launched again by polls: a
// stage that hands out one script per launch would otherwise be drained.
func TestDriveSessionPollsDoNotRelaunch(t *testing.T) {
	dir := t.TempDir()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	if err := recover.TakeLock(dir, clk.Now(), 1); err != nil {
		t.Fatal(err)
	}
	stage := &countingStage{}
	s := &Scheduler{d: Deps{Clock: clk, Procs: noPids{}}}
	job := SessionJob{RunID: "r1", Role: RoleCoder, StepID: "S1", RunDir: dir}
	for i := 0; i < 3; i++ {
		_, done, err := s.driveSession(context.Background(), stage, job)
		if err != nil || done {
			t.Fatalf("poll %d: done=%v err=%v, want a session still starting", i, done, err)
		}
	}
	if stage.starts != 0 {
		t.Errorf("stage started %d times while polling a starting session, want 0", stage.starts)
	}
}

func TestSingleStepGuard(t *testing.T) {
	step := func(typ, state string) stepRow { return stepRow{Type: typ, State: state} }
	cases := []struct {
		name  string
		steps []stepRow
		want  string
	}{
		{"one done build plus integrate", []stepRow{step("build", "done"), step("integrate", "waiting")}, "one"},
		{"two builds", []stepRow{step("build", "done"), step("build", "done"), step("integrate", "waiting")}, "many"},
		{"a fix step", []stepRow{step("build", "done"), step("fix", "done"), step("integrate", "waiting")}, "many"},
		{"build not done", []stepRow{step("build", "running"), step("integrate", "waiting")}, "many"},
		{"dropped build ignored", []stepRow{step("build", "done"), step("build", "dropped"), step("integrate", "waiting")}, "one"},
	}
	for _, c := range cases {
		if got := singleStep(c.steps); got != c.want {
			t.Errorf("%s: single_step = %s, want %s", c.name, got, c.want)
		}
	}
}
