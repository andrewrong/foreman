package sched

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	stagecode "github.com/andrewrong/foreman/internal/stage/code"
)

func TestNewReportsEveryMissingDependency(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Fatal("New accepted empty deps")
	}
	for _, name := range []string{"DB", "Engine", "Clock", "Gate", "Meters", "Home", "Ops", "Git", "Mirror", "Integ", "NewAdapter"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// A session that is already starting must not consult RunExtra: a script
// queue handing out one script per call would otherwise be drained by polls.
func TestDriveSessionPollsDoNotConsultRunExtra(t *testing.T) {
	dir := t.TempDir()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	if err := recover.TakeLock(dir, clk.Now(), 1); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &Scheduler{d: Deps{Clock: clk, RunExtra: func(api.Role, string) map[string]string { calls++; return nil }}}
	for i := 0; i < 3; i++ {
		_, done, err := s.driveSession(context.Background(), stagecode.Env{}, stagecode.Request{RunID: "r1", Role: api.RoleCoder, StepID: "S1", RunDir: dir})
		if err != nil || done {
			t.Fatalf("poll %d: done=%v err=%v, want a session still starting", i, done, err)
		}
	}
	if calls != 0 {
		t.Errorf("RunExtra called %d times while polling a starting session, want 0", calls)
	}
	if got := s.withExtra(stagecode.Request{Role: api.RoleCoder, StepID: "S1"}); got.Extra != nil || calls != 1 {
		t.Errorf("withExtra: Extra=%v calls=%d, want one call at launch", got.Extra, calls)
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
