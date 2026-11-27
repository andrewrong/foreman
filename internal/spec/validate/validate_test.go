package validate

import (
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

func validSpec() model.Spec {
	return model.Spec{
		Title:   "forge 轮询间隔可配置",
		Goal:    "owner 需要在额度紧张时调慢 GitHub 轮询",
		Current: []model.Ref{{Module: "forge", Path: "internal/forge/ops"}},
		Change:  []string{"轮询间隔从常量改为读取 runtime.toml"},
		Outcome: []model.Acceptance{{
			ID:           "A1",
			EARS:         "当 runtime.toml 的 forge.poll_interval 改变时，系统应当在下一轮轮询使用新值",
			Verify:       model.VerifyTest,
			VerifyDetail: "改配置后下一次轮询的间隔等于新值",
			SandboxOK:    true,
		}},
	}
}

func validReq() model.Requirement {
	return model.Requirement{
		ID: "R-foreman-20", Kind: model.KindFeature, Source: model.SourceOwner,
		Priority: model.P2, SpecVersion: 1, Spec: validSpec(), Branch: "foreman/R-foreman-20",
		Hold: model.HoldNone, Status: model.StatusSubmitted, Size: model.SizeS,
	}
}

func validBugSpec() model.Spec {
	s := validSpec()
	s.Bug = &model.BugInfo{
		Observed: "崩溃", Expected: "不崩溃", Repro: []string{"run x"},
		CauseStatus: model.CauseProven,
		CauseProof:  &model.Proof{Kind: model.ProofRepro, Cmd: "go test", OutputLine: "panic: x"},
	}
	return s
}

func TestSpecValidateRequirement(t *testing.T) {
	if errs := Requirement(validReq()); len(errs) != 0 {
		t.Fatalf("valid requirement rejected: %v", errs)
	}
	if errs := Spec(validSpec()); len(errs) != 0 {
		t.Fatalf("valid spec rejected: %v", errs)
	}
	bugReq := validReq()
	bugReq.Kind = model.KindBug
	bugReq.Spec = validBugSpec()
	if errs := Requirement(bugReq); len(errs) != 0 {
		t.Fatalf("valid bug requirement rejected: %v", errs)
	}
	draft := validReq()
	draft.Status = model.StatusDraft
	draft.Spec = model.Spec{}
	if errs := Requirement(draft); len(errs) != 0 {
		t.Fatalf("draft with empty spec must not be validated: %v", errs)
	}

	specCases := []struct {
		name, rule string
		mut        func(*model.Spec)
	}{
		{"title empty", "E-SPEC.title.required", func(s *model.Spec) { s.Title = "" }},
		{"title too long", "E-SPEC.title.max_len", func(s *model.Spec) { s.Title = strings.Repeat("标", 81) }},
		{"goal empty", "E-SPEC.goal.required", func(s *model.Spec) { s.Goal = "" }},
		{"goal too long", "E-SPEC.goal.max_len", func(s *model.Spec) { s.Goal = strings.Repeat("g", 501) }},
		{"ref without module", "E-REF.module.required", func(s *model.Spec) { s.Current = []model.Ref{{Path: "x"}} }},
		{"no outcome", "E-SPEC.outcome.min_items", func(s *model.Spec) { s.Outcome = nil }},
		{"acc id empty", "E-ACC.id.required", func(s *model.Spec) { s.Outcome[0].ID = "" }},
		{"acc id format", "E-ACC.id.format", func(s *model.Spec) { s.Outcome[0].ID = "X1" }},
		{"ears empty", "E-ACC.ears.required", func(s *model.Spec) { s.Outcome[0].EARS = "" }},
		{"ears mismatch", "E-ACC.ears.rule", func(s *model.Spec) { s.Outcome[0].EARS = "轮询应当使用新值" }},
		{"verify empty", "E-ACC.verify.required", func(s *model.Spec) { s.Outcome[0].Verify = "" }},
		{"verify bad enum", "E-ACC.verify.enum", func(s *model.Spec) { s.Outcome[0].Verify = "magic" }},
		{"verify_detail empty", "E-ACC.verify_detail.required", func(s *model.Spec) { s.Outcome[0].VerifyDetail = "" }},
		{"test not sandboxed", "E-ACC.sandbox_ok.rule", func(s *model.Spec) { s.Outcome[0].SandboxOK = false }},
		{"command not sandboxed", "E-ACC.sandbox_ok.rule", func(s *model.Spec) {
			s.Outcome[0].Verify = model.VerifyCommand
			s.Outcome[0].SandboxOK = false
		}},
		{"unknown id format", "E-UNK.id.format", func(s *model.Spec) { s.Unknowns = []model.Unknown{{ID: "Q1", Question: "?"}} }},
		{"unknown id empty", "E-UNK.id.required", func(s *model.Spec) { s.Unknowns = []model.Unknown{{Question: "?"}} }},
		{"unknown question empty", "E-UNK.question.required", func(s *model.Spec) { s.Unknowns = []model.Unknown{{ID: "U1"}} }},
	}
	for _, c := range specCases {
		s := validSpec()
		c.mut(&s)
		expectRule(t, "spec/"+c.name, Spec(s), c.rule)
	}

	bugCases := []struct {
		name, rule string
		mut        func(*model.BugInfo)
	}{
		{"observed", "E-BUG.observed.required", func(b *model.BugInfo) { b.Observed = "" }},
		{"expected", "E-BUG.expected.required", func(b *model.BugInfo) { b.Expected = "" }},
		{"repro", "E-BUG.repro.required", func(b *model.BugInfo) { b.Repro = nil }},
		{"cause_status empty", "E-BUG.cause_status.required", func(b *model.BugInfo) { b.CauseStatus = "" }},
		{"cause_status enum", "E-BUG.cause_status.enum", func(b *model.BugInfo) { b.CauseStatus = "maybe" }},
		{"proven without proof", "E-BUG.cause_proof.required_when", func(b *model.BugInfo) { b.CauseProof = nil }},
		{"proof kind", "E-PROOF.kind.required", func(b *model.BugInfo) { b.CauseProof.Kind = "" }},
		{"repro without cmd", "E-PROOF.cmd.required_when", func(b *model.BugInfo) { b.CauseProof.Cmd = "" }},
		{"repro without output_line", "E-PROOF.output_line.required_when", func(b *model.BugInfo) { b.CauseProof.OutputLine = "" }},
		{"code without path", "E-PROOF.path.required_when", func(b *model.BugInfo) {
			b.CauseProof = &model.Proof{Kind: model.ProofCode, Line: 3}
		}},
		{"code without line", "E-PROOF.line.required_when", func(b *model.BugInfo) {
			b.CauseProof = &model.Proof{Kind: model.ProofCode, Path: "a.go"}
		}},
	}
	for _, c := range bugCases {
		s := validBugSpec()
		c.mut(s.Bug)
		expectRule(t, "bug/"+c.name, Spec(s), c.rule)
	}

	reqCases := []struct {
		name, rule string
		mut        func(*model.Requirement)
	}{
		{"id empty", "E-REQ.id.required", func(r *model.Requirement) { r.ID = "" }},
		{"id format", "E-REQ.id.format", func(r *model.Requirement) { r.ID = "REQ-1"; r.Branch = "foreman/REQ-1" }},
		{"kind empty", "E-REQ.kind.required", func(r *model.Requirement) { r.Kind = "" }},
		{"kind enum", "E-REQ.kind.enum", func(r *model.Requirement) { r.Kind = "epic" }},
		{"source enum", "E-REQ.source.enum", func(r *model.Requirement) { r.Source = "bot" }},
		{"priority enum", "E-REQ.priority.enum", func(r *model.Requirement) { r.Priority = "P9" }},
		{"hold enum", "E-REQ.hold.enum", func(r *model.Requirement) { r.Hold = "stop" }},
		{"status enum", "E-REQ.status.enum", func(r *model.Requirement) { r.Status = "open" }},
		{"size empty", "E-REQ.size.required", func(r *model.Requirement) { r.Size = "" }},
		{"trigger enum", "E-REQ.pending_triggers.enum", func(r *model.Requirement) { r.PendingTriggers = []model.Trigger{"x"} }},
		{"branch empty", "E-REQ.branch.required", func(r *model.Requirement) { r.Branch = "" }},
		{"branch format", "E-REQ.branch.format", func(r *model.Requirement) { r.Branch = "feature/x" }},
		{"bug kind without bug", "E-SPEC.bug.required_when", func(r *model.Requirement) { r.Kind = model.KindBug }},
		{"nested spec error", "E-ACC.ears.rule", func(r *model.Requirement) { r.Spec.Outcome[0].EARS = "随便写写" }},
	}
	for _, c := range reqCases {
		r := validReq()
		c.mut(&r)
		expectRule(t, "req/"+c.name, Requirement(r), c.rule)
	}

	// Field path of a nested spec error is prefixed.
	r := validReq()
	r.Spec.Title = ""
	if errs := Requirement(r); len(errs) != 1 || errs[0].Field != "spec.title" {
		t.Errorf("nested field path: %v", errs)
	}
}

// expectRule requires that errs contains exactly one error and that its
// Rule is rule.
func expectRule(t *testing.T, name string, errs []Error, rule string) {
	t.Helper()
	if len(errs) != 1 || errs[0].Rule != rule {
		t.Errorf("%s: want exactly [%s], got %v", name, rule, errs)
		return
	}
	if errs[0].Field == "" || errs[0].Message == "" {
		t.Errorf("%s: error lacks field or message: %+v", name, errs[0])
	}
}
