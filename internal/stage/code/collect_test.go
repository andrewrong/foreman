package code

import (
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// Without a result.json the event depends on how the session ended: a timeout
// or non-zero exit or an unknown exit (daemon restarted) is infra; a clean
// exit without a result is bad_result (PP-4).
func TestCollectWithoutResultMapsHowTheSessionEnded(t *testing.T) {
	r := Request{RunID: "r1", RunDir: t.TempDir()}
	zero, three := launch.Exit{}, launch.Exit{Code: 3}
	for _, tc := range []struct {
		name     string
		exit     *launch.Exit
		timedOut bool
		want     string
	}{
		{"clean exit", &zero, false, model.EventBadResult},
		{"non-zero exit", &three, false, model.EventInfra},
		{"exit unknown after restart", nil, false, model.EventInfra},
		{"killed for timeout", nil, true, model.EventInfra},
	} {
		if got := (Env{}).Collect(r, tc.exit, tc.timedOut).Event.Name; got != tc.want {
			t.Errorf("%s: event %q, want %q", tc.name, got, tc.want)
		}
	}
}
