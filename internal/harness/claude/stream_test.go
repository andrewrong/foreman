package claude

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

func fixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestStreamGolden(t *testing.T) {
	store := meter.NewStore()
	clk := clock.NewFake(t0)
	a := newAdapter(t, nil)
	sum := api.Collect(a.ParseStream(fixture(t, "coder_advisor_skill.jsonl")), api.MeterSink(store, "claude", "max-1", clk))

	if sum.Init == nil || sum.Init.SessionID != "sess-1" || len(sum.Init.Tools) != 6 {
		t.Fatalf("init %+v", sum.Init)
	}
	if sum.AssistantTurns != 2 || !reflect.DeepEqual(sum.ToolCounts, map[string]int{"Read": 1, "Skill": 1, "Bash": 2}) {
		t.Fatalf("assistant %d %v", sum.AssistantTurns, sum.ToolCounts)
	}
	if sum.AdvisorCalls != 1 {
		t.Fatalf("advisor calls %d", sum.AdvisorCalls)
	}
	if len(sum.Errors) != 1 || sum.Errors[0].Line != 7 {
		t.Fatalf("errors %+v (want exactly the bad line 7)", sum.Errors)
	}
	if len(sum.RateLimits) != 2 {
		t.Fatalf("rate limits %d", len(sum.RateLimits))
	}
	r := sum.Result
	if r == nil || r.Subtype != "success" || r.IsError || r.Turns != 3 || r.Text != "done" || r.CostUSD != 0.0421 ||
		r.TerminalReason != "completed" || r.Denials != 1 {
		t.Fatalf("result %+v", r)
	}
	if r.Usage != (api.Usage{InputTokens: 120, OutputTokens: 340, CacheCreationTokens: 3174, CacheReadTokens: 5205}) {
		t.Fatalf("usage %+v", r.Usage)
	}
	if len(r.ModelUsage) != 2 || r.ModelUsage["claude-opus-5-5"].OutputTokens != 40 || r.ModelUsage["claude-sonnet-5-5"].CostUSD != 0.04 {
		t.Fatalf("modelUsage %+v", r.ModelUsage)
	}
	// Both windows reached the meter; each stays independent (D-051).
	for res, used := range map[string]int64{"model/claude/max-1/7d": 89, "model/claude/max-1/5h": 31} {
		got, ok := store.Get(res)
		if !ok || got.Used != used {
			t.Errorf("%s: %+v ok=%v, want used %d", res, got, ok, used)
		}
	}
}

func TestHarnessReportsSkillCalls(t *testing.T) {
	sum := api.Collect(ParseStream(fixture(t, "coder_advisor_skill.jsonl")), nil)
	if got := sum.SkillNames(); !reflect.DeepEqual(got, []string{"tdd"}) || sum.SkillCalls["tdd"] != 1 {
		t.Fatalf("skill calls %v %v", got, sum.SkillCalls)
	}
	// Skill tool_use with the alternative "name" input key, and no skill calls.
	in := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"name":"grilling"}}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}` + "\n"
	sum = api.Collect(ParseStream(strings.NewReader(in)), nil)
	if got := sum.SkillNames(); !reflect.DeepEqual(got, []string{"grilling"}) {
		t.Fatalf("skill names %v", got)
	}
	if !(&Adapter{}).Capabilities().ReportsSkillCalls {
		t.Fatal("adapter must declare reports_skill_calls")
	}
}

func TestStreamEdgeCases(t *testing.T) {
	// no trailing newline, blank lines, CRLF
	in := "\r\n{\"type\":\"result\",\"subtype\":\"error_max_turns\",\"is_error\":true,\"num_turns\":40}"
	sum := api.Collect(ParseStream(strings.NewReader(in)), nil)
	if sum.Result == nil || !sum.Result.IsError || sum.Result.Subtype != "error_max_turns" {
		t.Fatalf("%+v", sum.Result)
	}
	// bad rate_limit_info becomes an error event, not a panic or a reading
	bad := `{"type":"rate_limit_event","rate_limit_info":"oops"}` + "\n"
	sum = api.Collect(ParseStream(strings.NewReader(bad)), nil)
	if len(sum.Errors) != 1 || len(sum.RateLimits) != 0 {
		t.Fatalf("%+v", sum)
	}
	// read error mid-stream surfaces as an error event
	rd := iotest.TimeoutReader(strings.NewReader(`{"type":"system","subtype":"init"}` + "\n" + `{"type":"res`))
	sum = api.Collect(ParseStream(rd), nil)
	if sum.Init == nil || len(sum.Errors) == 0 {
		t.Fatalf("%+v", sum)
	}
	// consumer may stop early
	n := 0
	for range ParseStream(fixture(t, "coder_advisor_skill.jsonl")) {
		n++
		break
	}
	if n != 1 {
		t.Fatal("early break")
	}
}
