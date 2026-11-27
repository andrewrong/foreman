package state

import (
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// The findings table is the sink: even a caller that forgot to redact cannot
// store a secret or a local path (C-09).
func TestCheckFindingStoresRedactedLog(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	st := e.step("R-t-1", model.StepBuild, model.StateRunning, model.StageCheck)
	secret := "gh" + "p_" + strings.Repeat("a1B2", 9)
	tail := "FAIL\nread /Us" + "ers/zed/key\ntoken " + secret
	e.transition(st, Event{Name: "check_fail", FailingTests: []string{"TestX/zed" + "@corp." + "io"}, LogTail: tail})
	var tests, got string
	e.read(`SELECT failing_tests, log_tail FROM findings WHERE scope = ?`, []any{st}, &tests, &got)
	for _, leak := range []string{"zed", secret} {
		if strings.Contains(got, leak) || strings.Contains(tests, leak) {
			t.Errorf("finding stores %q: tests %q tail %q", leak, tests, got)
		}
	}
	if !strings.Contains(got, "[redacted:abs-path]") || !strings.Contains(got, "[redacted:token]") || !strings.Contains(tests, "TestX/[redacted:email]") {
		t.Errorf("finding payload %q %q", tests, got)
	}
}
