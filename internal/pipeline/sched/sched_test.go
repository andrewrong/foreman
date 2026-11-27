package sched

import (
	"strings"
	"testing"
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
