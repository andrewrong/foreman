package context

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/spec/model"
)

const resultEventLine = `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":41,"result":"","total_cost_usd":0.37,` +
	`"usage":{"input_tokens":120,"output_tokens":3400,"cache_creation_input_tokens":9000,"cache_read_input_tokens":50000}}` + "\n"

// D-091: the usage comes from the stream's result event and is kept for a
// session that ended without a usable result too; without a result event it is
// unknown, not zero.
func TestSessionUsageFromResultEvent(t *testing.T) {
	env := Env{Adapter: &claude.Adapter{}}
	req := func(stdout string) Request {
		dir := t.TempDir()
		if stdout != "" {
			if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(stdout), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return Request{RunID: "r1", Role: "coder", RunDir: dir}
	}

	timedOut := env.Collect(req(resultEventLine), &launch.Exit{Code: -1, Signaled: true}, true)
	want := model.Usage{Known: true, Input: 120, Output: 3400, CacheRead: 50000, CacheWrite: 9000, Turns: 41, CostUSD: 0.37}
	if timedOut.Event.Name != model.EventInfra || timedOut.Event.Usage != want {
		t.Errorf("timed-out session: event %+v, want infra carrying usage %+v", timedOut.Event, want)
	}
	badResult := env.Collect(req(resultEventLine), &launch.Exit{}, false)
	if badResult.Event.Name != model.EventBadResult || badResult.Event.Usage != want {
		t.Errorf("session without result.json: event %+v", badResult.Event)
	}
	for name, stdout := range map[string]string{"no stdout.log": "", "stream without a result event": `{"type":"system","subtype":"init"}` + "\n"} {
		got := env.Collect(req(stdout), &launch.Exit{}, false)
		if got.Event.Usage.Known || got.Event.Usage != (model.Usage{}) {
			t.Errorf("%s: usage %+v, want unknown", name, got.Event.Usage)
		}
	}
}
