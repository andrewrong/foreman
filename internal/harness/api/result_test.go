package api

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

const coderOK = `{"schema_version":1,"run_id":"R-foreman-12/S2/r1","role":"coder","status":"ok","commits":["3f2a9c1"],
 "acceptance":[{"id":"A3","result":"pass","evidence":"TestConcurrentDemand"}],
 "discoveries":[{"kind":"answer","ref":"U1","text":"304 not counted","evidence":{"cmd":"go test","output_line":"ok"}}]}`

const reviewerChanges = `{"schema_version":1,"run_id":"R-foreman-12/S2/r2","role":"reviewer","status":"ok","verdict":"changes",
 "findings":[{"scope":"R-foreman-12/S2","severity":"blocking","location":{"path":"internal/resource/ledger/ledger.go","line":88},"problem":"p","close_when":"c"}]}`

func ctxFor(run string, role Role) ResultContext {
	return ResultContext{RunID: run, Role: role, Scope: "step"}
}

func wantProblem(t *testing.T, raw string, ctx ResultContext, substr string) {
	t.Helper()
	_, err := ValidateResult([]byte(raw), ctx)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError containing %q, got %v", substr, err)
	}
	if !strings.Contains(ve.Error(), substr) {
		t.Fatalf("problems %q do not mention %q", ve.Error(), substr)
	}
}

func TestResultCoderOK(t *testing.T) {
	ctx := ctxFor("R-foreman-12/S2/r1", RoleCoder)
	ctx.AcceptanceIDs = []string{"A3"}
	r, err := ValidateResult([]byte(coderOK), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Commits[0] != "3f2a9c1" || r.Discoveries[0].Ref != "U1" {
		t.Fatalf("decoded wrong: %+v", r)
	}
}

func TestResultCoderRules(t *testing.T) {
	ctx := ctxFor("R-foreman-12/S2/r1", RoleCoder)
	ctx.AcceptanceIDs = []string{"A3", "A4"}
	wantProblem(t, coderOK, ctx, "A4 not covered")
	ctx.AcceptanceIDs = []string{"A3"}
	wantProblem(t, strings.Replace(coderOK, `"commits":["3f2a9c1"]`, `"commits":[]`, 1), ctx, "commits")
	wantProblem(t, strings.Replace(coderOK, `"pass"`, `"maybe"`, 1), ctx, "result")
	wantProblem(t, strings.Replace(coderOK, `"evidence":{"cmd":"go test","output_line":"ok"}`, `"evidence":{"cmd":"go test"}`, 1), ctx, "evidence")
	// non-ok statuses need no commits or acceptance
	spec := `{"schema_version":1,"run_id":"R-foreman-12/S2/r1","role":"coder","status":"spec_conflict"}`
	if _, err := ValidateResult([]byte(spec), ctx); err != nil {
		t.Fatalf("spec_conflict: %v", err)
	}
}

func TestResultIdentityAndStatus(t *testing.T) {
	ctx := ctxFor("R-foreman-12/S2/r1", RoleCoder)
	wantProblem(t, strings.Replace(coderOK, "S2/r1", "S2/r9", 1), ctx, "run_id")
	wantProblem(t, strings.Replace(coderOK, `"role":"coder"`, `"role":"reviewer"`, 1), ctx, "role")
	for _, st := range []string{"check_failed", "infra", "budget_exceeded", ""} {
		wantProblem(t, strings.Replace(coderOK, `"status":"ok"`, `"status":"`+st+`"`, 1), ctx, "status")
	}
	wantProblem(t, strings.Replace(coderOK, `"schema_version":1`, `"schema_version":2`, 1), ctx, "newer")
	wantProblem(t, strings.Replace(coderOK, `"schema_version":1,`, ``, 1), ctx, "schema_version")
	if _, err := ValidateResult([]byte(`{`), ctx); err == nil {
		t.Fatal("truncated JSON accepted")
	}
	if _, err := ValidateResult([]byte(coderOK+`{}`), ctx); err == nil {
		t.Fatal("trailing data accepted")
	}
	acc := `{"schema_version":1,"run_id":"R-foreman-12/S2/r1","role":"coder","status":"access_request"}`
	wantProblem(t, acc, ctx, "access")
	acc = strings.Replace(acc, `"status"`, `"access":{"resource":"r1","reason":"x"},"status"`, 1)
	if _, err := ValidateResult([]byte(acc), ctx); err != nil {
		t.Fatalf("access_request with access: %v", err)
	}
}

func TestResultReviewer(t *testing.T) {
	ctx := ctxFor("R-foreman-12/S2/r2", RoleReviewer)
	if _, err := ValidateResult([]byte(reviewerChanges), ctx); err != nil {
		t.Fatal(err)
	}
	// protocol-run example invalid_reviewer_verdict
	wantProblem(t, strings.Replace(reviewerChanges, `"changes"`, `"approve"`, 1), ctx, "blocking")
	// approve with only minor findings is fine; approve with no findings key is not
	minor := strings.Replace(strings.Replace(reviewerChanges, `"changes"`, `"approve"`, 1), `"blocking"`, `"minor"`, 1)
	if _, err := ValidateResult([]byte(minor), ctx); err != nil {
		t.Fatalf("approve+minor: %v", err)
	}
	noFindings := `{"schema_version":1,"run_id":"R-foreman-12/S2/r2","role":"reviewer","status":"ok","verdict":"approve"}`
	wantProblem(t, noFindings, ctx, "findings required")
	empty := strings.Replace(noFindings, `"approve"`, `"approve","findings":[]`, 1)
	if _, err := ValidateResult([]byte(empty), ctx); err != nil {
		t.Fatalf("approve with empty findings: %v", err)
	}
	wantProblem(t, strings.Replace(reviewerChanges, `"scope"`, `"id":"F1","scope"`, 1), ctx, "set by foremand")
	wantProblem(t, strings.Replace(reviewerChanges, `"location":{"path":"internal/resource/ledger/ledger.go","line":88},`, ``, 1), ctx, "location")
	wantProblem(t, strings.Replace(reviewerChanges, `"severity":"blocking"`, `"severity":"nit"`, 1), ctx, "severity")
	wantProblem(t, strings.Replace(reviewerChanges, `"verdict":"changes"`, `"verdict":"maybe"`, 1), ctx, "verdict")
}

func TestResultRescuer(t *testing.T) {
	base := `{"schema_version":1,"run_id":"R/r1","role":"rescuer","status":"ok","decision":"DEC"GUID}`
	mk := func(dec, guid string) string {
		return strings.Replace(strings.Replace(base, "DEC", dec, 1), "GUID", guid, 1)
	}
	step := ResultContext{RunID: "R/r1", Role: RoleRescuer, Scope: "step"}
	req := ResultContext{RunID: "R/r1", Role: RoleRescuer, Scope: "requirement"}
	if _, err := ValidateResult([]byte(mk("retry", `,"guidance":"try X"`)), step); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, mk("retry", ""), step, "guidance")
	wantProblem(t, mk("drop_requirement", ""), step, "decision")
	wantProblem(t, mk("drop_step", ""), req, "decision")
	if _, err := ValidateResult([]byte(mk("drop_requirement", "")), req); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateResult([]byte(mk("drop_step", "")), step); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOutputOnlyResultFiles(t *testing.T) {
	ok := fstest.MapFS{
		"context.md": {}, "stdout.log": {}, "stderr.log": {}, "todo.last-valid.json": {},
		"result.json": {}, "todo.json": {}, "result.json.tmp": {},
	}
	if err := CheckOutputs(ok); err != nil {
		t.Fatalf("allowed set rejected: %v", err)
	}
	bad := fstest.MapFS{"result.json": {}, "notes.md": {}, "patch.diff": {}}
	err := CheckOutputs(bad)
	if err == nil || !strings.Contains(err.Error(), "notes.md, patch.diff") {
		t.Fatalf("want stray notes.md and patch.diff, got %v", err)
	}
	stray, _ := StrayOutputs(bad)
	if len(stray) != 2 {
		t.Fatalf("stray %v", stray)
	}
}
