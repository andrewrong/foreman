package smcheck

import (
	"slices"
	"strings"
	"testing"
)

// mutate is a textual edit of one spec file, applied in memory.
type mutate func(t *testing.T, src string) string

// replace swaps old for new; old must occur exactly once so a drifting spec
// fails loudly instead of silently mutating nothing.
func replace(old, new string) mutate {
	return func(t *testing.T, src string) string {
		t.Helper()
		if n := strings.Count(src, old); n != 1 {
			t.Fatalf("mutation anchor occurs %d times (want 1): %q", n, old)
		}
		return strings.Replace(src, old, new, 1)
	}
}

// chain applies several edits in order.
func chain(ms ...mutate) mutate {
	return func(t *testing.T, src string) string {
		t.Helper()
		for _, m := range ms {
			src = m(t, src)
		}
		return src
	}
}

// dropLine deletes the single line that starts with prefix (after indentation).
func dropLine(prefix string) mutate {
	return func(t *testing.T, src string) string {
		t.Helper()
		lines := strings.Split(src, "\n")
		at := -1
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				if at >= 0 {
					t.Fatalf("line prefix matches twice: %q", prefix)
				}
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("no line starts with %q", prefix)
		}
		return strings.Join(append(lines[:at:at], lines[at+1:]...), "\n")
	}
}

type mutation struct {
	name string
	pr   bool // mutate protocol-run.yaml instead of state-machine.yaml
	edit mutate
	// want: every entry must equal one of the errors exactly (wording is shared with tools/smcheck.rb)
	want    []string
	wantErr string
}

var mutations = []mutation{
	{name: "req ignored overlaps row",
		edit: replace(`- { from: [none, replan, rescue], events: [owner_answer],`, `- { from: [none, replan, rescue, parked], events: [owner_answer],`),
		want: []string{"req ignored overlaps row: H14 hold=parked event=owner_answer"}},
	{name: "unbounded self-loop",
		edit: replace(`effects: [extend_deadline, "inc:merge_waits"] }`, `effects: [extend_deadline] }`),
		want: []string{"unbounded self-loop: S48", "unbounded cycle: S48"}},
	{name: "unbounded cycle through reset",
		edit: replace(`effects: ["inc:rereviews"], note:`, `effects: ["inc:rereviews", "reset:rereviews"], note:`),
		want: []string{"unbounded cycle: S06,S08,S14,S43,S46,S71,S73,S75"}},
	{name: "uncovered event",
		edit: dropLine(`- { id: S47,`),
		want: []string{`uncovered: {:state=>"running", :stage=>"merge", :type=>"integrate"} event=guardrail_hit`}},
	{name: "unbounded cycle without access_requests inc",
		edit: replace(`effects: ["inc:access_requests"] }`, `}`),
		want: []string{"unbounded cycle: S06,S14,S15,S17,S71,S73,S75,S78", "unbounded cycle: S04,S14,S15,S17,S71,S73,S75,S78"}},
	{name: "guard gap",
		edit: dropLine(`- { id: S46b,`),
		want: []string{`guard gap: {:state=>"running", :stage=>"merge", :type=>"integrate"} event=needs_rereview {"counter:rereviews"=>"at_cap"}`}},
	{name: "invalid target",
		edit: replace(`to: { state: queued, stage: review }, effects: ["inc:rereviews"]`, `to: { state: queued, stage: bogus }, effects: ["inc:rereviews"]`),
		want: []string{`invalid target: S46 from {:state=>"running", :stage=>"merge", :type=>"integrate"} -> {"state"=>"queued", "stage"=>"bogus"}`}},
	{name: "no exit",
		edit: chain(
			replace(`    hold:   { types: all,       stages: all,                 deadline: none_bounded,  note: "由表 B 保证终止" }`,
				"    hold:   { types: all,       stages: all,                 deadline: none_bounded,  note: \"由表 B 保证终止\" }\n    limbo:  { types: all,       stages: all }"),
			replace(`- { id: S90, from: { state: [queued, running, waiting],`, `- { id: S90, from: { state: [queued, running],`)),
		want: []string{`no exit: {:state=>"waiting", :stage=>"code", :type=>"build", :reason=>"limbo"}`}},
	{name: "unguarded inc",
		edit: replace(`event: needs_rereview, guard: { counter: { name: rereviews, is: below } }`, `event: needs_rereview, guard: { counter: { name: merge_waits, is: below } }`),
		want: []string{"unguarded inc: S46 inc:rereviews"}},
	{name: "req guard gap",
		edit: dropLine(`- { id: H05,`),
		want: []string{`req guard gap: replan planner_failed {"req_counter:planner_failures"=>"at_cap"}`}},
	{name: "req hold no exit",
		edit: replace(`values: [none, replan, rescue, owner, parked]`, `values: [none, replan, rescue, owner, parked, limbo]`),
		want: []string{"req hold no exit: limbo", "req trigger unhandled: limbo replan_trigger (precedence note covers only lower-priority)"}},
	{name: "req trigger unhandled",
		edit: dropLine(`- { id: H13,`),
		want: []string{"req trigger unhandled: none owner_trigger (precedence note covers only lower-priority)", "req trigger unhandled: replan owner_trigger (precedence note covers only lower-priority)", "req trigger unhandled: rescue owner_trigger (precedence note covers only lower-priority)"}},
	{name: "counter never incremented",
		edit: replace(`effects: ["inc:rereviews"], note:`, `effects: [], note:`),
		want: []string{"counter never incremented: rereviews", "unbounded cycle: S06,S08,S14,S43,S46,S71,S73,S75"}},
	{name: "ignored overlaps row",
		edit: replace(`- { states: [done, dropped], events: all }`, `- { states: [queued, done, dropped], events: all }`),
		want: []string{`ignored overlaps row: S01 {:state=>"queued", :stage=>"code", :type=>"build"} event=dispatch`}},
	{name: "req uncovered",
		edit: dropLine(`- { from: [none], events: [planner_ok,`),
		want: []string{"req uncovered: hold=none event=planner_ok"}},
	{name: "protocol step_decision mismatch", pr: true,
		edit: replace(`enum retry|drop_step|replan|ask_owner`, `enum retry|drop_step|replan`),
		want: []string{`protocol step_decision mismatch: ["retry", "drop_step", "replan"] vs ["retry", "drop_step", "replan", "ask_owner"]`}},
	{name: "protocol req_decision mismatch", pr: true,
		edit: replace(`需求级：enum retry|replan|drop_requirement|ask_owner`, `需求级：enum retry|replan|ask_owner`),
		want: []string{`protocol req_decision mismatch: ["retry", "replan", "ask_owner"] vs ["retry", "replan", "drop_requirement", "ask_owner"]`}},
	{name: "undeclared guard enum",
		edit:    replace(`guard: { tier: R1,`, `guard: { tierx: R1,`),
		wantErr: "guard dim without declared enum: tierx"},
	{name: "step decision enum gains an unhandled value",
		edit: replace(`step_decision: [retry, drop_step, replan, ask_owner]`, `step_decision: [retry, drop_step, replan, ask_owner, extra]`),
		want: []string{`guard gap: {:state=>"waiting", :stage=>"code", :type=>"build", :reason=>"rescue"} event=rescue_decision {"decision"=>"extra"}`, `protocol step_decision mismatch: ["retry", "drop_step", "replan", "ask_owner"] vs ["retry", "drop_step", "replan", "ask_owner", "extra"]`}},
	{name: "answer enum gains an unhandled value",
		edit: replace(`answer:        [resume, replan, drop, drop_step, grant, deny]`, `answer:        [resume, replan, drop, drop_step, grant, deny, extra]`),
		want: []string{`req guard gap: owner owner_answer {"answer"=>"extra"}`, `req guard gap: parked owner_answer {"answer"=>"extra"}`}},
}

func TestSmcheckMutations(t *testing.T) {
	sm, pr := readSpec(t, "state-machine.yaml"), readSpec(t, "protocol-run.yaml")
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			s, p := sm, pr
			if m.pr {
				p = m.edit(t, pr)
			} else {
				s = m.edit(t, sm)
			}
			res, err := Check([]byte(s), []byte(p))
			if m.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), m.wantErr) {
					t.Fatalf("want Check error %q, got %v", m.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range m.want {
				if !slices.Contains(res.Errors, w) {
					t.Errorf("no error contains %q; errors:\n%s", w, strings.Join(res.Errors, "\n"))
				}
			}
		})
	}
}
