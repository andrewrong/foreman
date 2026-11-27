package sched

import (
	"testing"

	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// An integrate step waiting for its fix step leaves on the newest fix step's
// terminal state: done is fix_done (S35), dropped is fix_dropped (S36); an
// older fix step's state is history and open steps keep it waiting.
func TestFixOutcome(t *testing.T) {
	in := stepRow{ID: "R/S2", Type: "integrate"}
	fix := func(seq int, st string) stepRow {
		return stepRow{ID: "R/S" + string(rune('0'+seq)), Seq: seq, Type: "fix", State: st, FixFor: "R/S2"}
	}
	other := stepRow{ID: "R/S9", Seq: 9, Type: "fix", State: "done", FixFor: "R/S8"} // fixes another step
	for _, c := range []struct {
		name string
		all  []stepRow
		want string
	}{
		{"no fix step", []stepRow{in}, ""},
		{"fix step open", []stepRow{in, fix(3, "running")}, ""},
		{"fix step done", []stepRow{in, fix(3, "done")}, "fix_done"},
		{"fix step dropped", []stepRow{in, fix(3, "dropped")}, "fix_dropped"},
		{"newest decides", []stepRow{in, fix(3, "dropped"), fix(4, "queued")}, ""},
		{"newest done after an older drop", []stepRow{in, fix(3, "dropped"), fix(4, "done")}, "fix_done"},
		{"another step's fix is not ours", []stepRow{in, other}, ""},
	} {
		if got := fixOutcome(in, c.all); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// ci_progress and lease_wait below their cap leave the step where it was and
// the Run and lease go on; every other outcome, the cap rows included, ends
// the Run.
func TestStaysPut(t *testing.T) {
	ci := stepRow{State: "waiting", Stage: "check", Reason: "ci"}
	merge := stepRow{State: "running", Stage: "merge"}
	same := func(st stepRow) state.Outcome {
		return state.Outcome{To: state.To{State: st.State, Stage: st.Stage, Reason: st.Reason}}
	}
	hold := state.Outcome{To: state.To{State: "waiting", Stage: "check", Reason: "hold"}}
	for _, c := range []struct {
		name string
		ev   string
		st   stepRow
		out  state.Outcome
		want bool
	}{
		{"S38", model.EventCIProgress, ci, same(ci), true},
		{"S37", model.EventLeaseWait, ci, same(ci), true},
		{"S48", model.EventCIProgress, merge, same(merge), true},
		{"S45", model.EventLeaseWait, merge, same(merge), true},
		{"S38b at the cap", model.EventCIProgress, ci, hold, false},
		{"S45b at the cap", model.EventLeaseWait, merge, state.Outcome{To: state.To{State: "waiting", Stage: "merge", Reason: "hold"}}, false},
		{"l1_pass", model.EventL1Pass, ci, state.Outcome{To: state.To{State: "queued", Stage: "merge"}}, false},
		{"merged", model.EventMerged, merge, state.Outcome{To: state.To{State: "done"}}, false},
	} {
		if got := staysPut(model.Event{Name: c.ev}, c.st, c.out); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
