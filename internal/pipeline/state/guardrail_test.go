package state

import (
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// S47 puts the integrate step on an owner H6 hold with an ask; the owner's
// grant (H21) writes E-PR.guardrail_approved_sha with the head the pull request
// row holds, with no Resource needed, and the step resumes at merge.
func TestGuardrailGrantApprovesHead(t *testing.T) {
	e := newEnv(t)
	e.req("R-t-1")
	e.exec(`INSERT INTO pull_requests (req_id, head_branch, head_sha, state) VALUES ('R-t-1', 'foreman/R-t-1', 'pr-head', 'open')`)
	in := e.step("R-t-1", model.StepIntegrate, model.StateRunning, model.StageMerge)

	out := e.transition(in, Event{Name: "guardrail_hit", Head: "pr-head",
		Ask: &AskContent{HItem: "H6", Checked: "G-09 internal/workspace/x.go"}})
	wantRow(t, out.RowID, "S47")
	wantInfo(t, e.info(in), "waiting", "merge", "hold")
	var hItem, subject, checked, status string
	e.read(`SELECT h_item, subject, checked, status FROM asks WHERE req_id = 'R-t-1'`, nil, &hItem, &subject, &checked, &status)
	if hItem != "H6" || subject != in || status != "open" || checked != "G-09 internal/workspace/x.go" {
		t.Errorf("ask %s %s %q %s", hItem, subject, checked, status)
	}
	if e.hold("R-t-1") != "owner" {
		t.Errorf("hold %s, want owner", e.hold("R-t-1"))
	}

	wantRow(t, e.transitionReq("R-t-1", Event{Name: "owner_answer", Answer: "grant"}).RowID, "H21")
	var approved string
	e.read(`SELECT guardrail_approved_sha FROM pull_requests WHERE req_id = 'R-t-1'`, nil, &approved)
	if approved != "pr-head" {
		t.Errorf("guardrail_approved_sha %q, want pr-head", approved)
	}
	wantInfo(t, e.info(in), "queued", "merge", "")
	if e.hold("R-t-1") != "none" {
		t.Errorf("hold %s after the grant", e.hold("R-t-1"))
	}
}
