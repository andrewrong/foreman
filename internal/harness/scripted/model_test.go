package scripted

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/harness/api"
)

// The model a Run is given reaches the child process and shows in the first
// line of its stream, so integration tests can see which model an invocation
// was built with (test:retry-model-escalation at the integration level).
func TestScriptedInvocationCarriesModel(t *testing.T) {
	script := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(script, []byte(`{"actions":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(testLease(t), Config{})
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{"opus": "opus", "": "scripted"} {
		inv, err := a.BuildInvocation(api.RunSpec{RunID: "R-1/S1/r1", Role: api.RoleCoder, Model: model, MaxTurns: 5,
			Timeout: time.Minute, RunDir: t.TempDir(), Extra: map[string]string{ExtraScript: script}})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if code := RunChild(func(k string) string { return inv.Env[k] }, &out, &out); code != 0 {
			t.Fatalf("child exit %d: %s", code, out.String())
		}
		first, _, _ := strings.Cut(out.String(), "\n")
		var init struct {
			Subtype, Model string
		}
		if err := json.Unmarshal([]byte(first), &init); err != nil || init.Subtype != "init" {
			t.Fatalf("first stream line %q", first)
		}
		if init.Model != want {
			t.Errorf("RunSpec.Model %q: stream model %q, want %q", model, init.Model, want)
		}
	}
}
