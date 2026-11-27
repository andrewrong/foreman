package state

import "slices"

// resultEvents are the events that report the result of a Run; the counter
// resets declared as any_valid_result and any_non_infra_result fire on them
// (state-machine.yaml counters; smcheck uses the same list).
var resultEvents = []string{"run_ok", "check_pass", "check_fail", "review_approve", "review_changes",
	"merge_back_conflict", "merged", "merge_conflict", "spec_conflict", "bad_result", "budget_exceeded",
	"access_request", "l1_pass", "l1_fail"}

// autoResetCounters applies the "reset on result" rules of the step counters:
// bad_result resets on any valid result (not on bad_result itself) and infra
// on any result that is not infra. It runs before the row's effects, so an
// inc: of the same row is not undone.
func (c *txc) autoResetCounters() {
	if c.step == nil || !slices.Contains(resultEvents, c.ev.Name) {
		return
	}
	if c.ev.Name != "bad_result" {
		c.step.counters["bad_result"] = 0
	}
	c.step.counters["infra"] = 0
}
