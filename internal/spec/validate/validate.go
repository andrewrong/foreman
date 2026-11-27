// Package validate enforces the constraints of spec/design/entities.yaml on
// Requirement and Spec values. Rule strings have the form
// "<entity id>.<field>.<constraint>", e.g. "E-SPEC.title.max_len".
package validate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/andrewrong/foreman/internal/spec/ears"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// Error is one violated constraint.
type Error struct {
	Field   string // path of the offending field, e.g. spec.outcome[0].ears
	Rule    string // the entities.yaml constraint violated
	Message string
}

func (e Error) Error() string { return fmt.Sprintf("%s: %s [%s]", e.Field, e.Message, e.Rule) }

type collector []Error

func (c *collector) add(field, rule, format string, args ...any) {
	*c = append(*c, Error{Field: field, Rule: rule, Message: fmt.Sprintf(format, args...)})
}

func (c *collector) required(field, rule, v string) {
	if strings.TrimSpace(v) == "" {
		c.add(field, rule, "required field is empty")
	}
}

func (c *collector) maxLen(field, rule, v string, n int) {
	if l := utf8.RuneCountInString(v); l > n {
		c.add(field, rule, "length %d exceeds max_len %d", l, n)
	}
}

// enum checks a required enum value; "" is reported as missing.
func enum[T ~string](c *collector, field, entity string, v T, allowed ...T) {
	if v == "" {
		c.add(field, entity+"."+leaf(field)+".required", "required enum is empty")
		return
	}
	for _, a := range allowed {
		if v == a {
			return
		}
	}
	c.add(field, entity+"."+leaf(field)+".enum", "value %q not in allowed set", string(v))
}

func leaf(field string) string {
	if i := strings.LastIndex(field, "."); i >= 0 {
		field = field[i+1:]
	}
	if i := strings.Index(field, "["); i >= 0 {
		field = field[:i]
	}
	return field
}

var (
	reqID = regexp.MustCompile(`^R-[^-\s]+(?:-[^-\s]+)*-\d+$`)
	accID = regexp.MustCompile(`^A\d+$`)
	unkID = regexp.MustCompile(`^U\d+$`)
)

// Requirement validates r. A draft Requirement (status = draft) has an
// incomplete spec by design (ACR-14), so its spec is not validated; a
// submitted one is validated as a whole, including Spec.
func Requirement(r model.Requirement) []Error {
	var c collector
	c.required("id", "E-REQ.id.required", r.ID)
	if r.ID != "" && !reqID.MatchString(r.ID) {
		c.add("id", "E-REQ.id.format", "id %q does not match R-<project>-<n>", r.ID)
	}
	enum(&c, "kind", "E-REQ", r.Kind, model.KindFeature, model.KindBug, model.KindChore)
	enum(&c, "source", "E-REQ", r.Source, model.SourceOwner, model.SourceNightly, model.SourceProd, model.SourceImprove)
	enum(&c, "priority", "E-REQ", r.Priority, model.P0, model.P1, model.P2, model.P3)
	enum(&c, "hold", "E-REQ", r.Hold, model.HoldNone, model.HoldReplan, model.HoldRescue, model.HoldOwner, model.HoldParked)
	enum(&c, "status", "E-REQ", r.Status, model.StatusDraft, model.StatusSubmitted)
	enum(&c, "size", "E-REQ", r.Size, model.SizeS, model.SizeM, model.SizeL)
	for i, t := range r.PendingTriggers {
		enum(&c, fmt.Sprintf("pending_triggers[%d]", i), "E-REQ", t,
			model.TriggerReplan, model.TriggerRescue, model.TriggerOwner)
	}
	if r.SpecVersion < 1 {
		c.add("spec_version", "E-REQ.spec_version.min", "spec_version %d must be at least 1 (D-070)", r.SpecVersion)
	}
	c.required("branch", "E-REQ.branch.required", r.Branch)
	if r.Branch != "" && r.ID != "" && r.Branch != "foreman/"+r.ID {
		c.add("branch", "E-REQ.branch.format", "branch %q must be foreman/%s", r.Branch, r.ID)
	}
	if r.Status == model.StatusSubmitted {
		for _, e := range Spec(r.Spec) {
			e.Field = "spec." + e.Field
			c = append(c, e)
		}
		if r.Kind == model.KindBug && r.Spec.Bug == nil {
			c.add("spec.bug", "E-SPEC.bug.required_when", "bug is required when kind == bug")
		}
	}
	return c
}

// Spec validates a Spec on its own. The kind == bug requirement for
// Spec.bug needs the Requirement's kind and is checked by Requirement.
func Spec(s model.Spec) []Error {
	var c collector
	c.required("title", "E-SPEC.title.required", s.Title)
	c.maxLen("title", "E-SPEC.title.max_len", s.Title, 80)
	c.required("goal", "E-SPEC.goal.required", s.Goal)
	c.maxLen("goal", "E-SPEC.goal.max_len", s.Goal, 500)
	for i, ref := range s.Current {
		c.required(fmt.Sprintf("current[%d].module", i), "E-REF.module.required", ref.Module)
	}
	// current may be empty (new module, D-070); change needs at least one item.
	if len(s.Change) < 1 {
		c.add("change", "E-SPEC.change.min_items", "change needs at least 1 item")
	}
	if len(s.Outcome) < 1 {
		c.add("outcome", "E-SPEC.outcome.min_items", "outcome needs at least 1 item")
	}
	for i, a := range s.Outcome {
		acceptance(&c, fmt.Sprintf("outcome[%d]", i), a)
	}
	for i, u := range s.Unknowns {
		p := fmt.Sprintf("unknowns[%d]", i)
		c.required(p+".id", "E-UNK.id.required", u.ID)
		if u.ID != "" && !unkID.MatchString(u.ID) {
			c.add(p+".id", "E-UNK.id.format", "id %q does not match U<n>", u.ID)
		}
		c.required(p+".question", "E-UNK.question.required", u.Question)
	}
	if s.Bug != nil {
		bug(&c, "bug", *s.Bug)
	}
	return c
}

func acceptance(c *collector, p string, a model.Acceptance) {
	c.required(p+".id", "E-ACC.id.required", a.ID)
	if a.ID != "" && !accID.MatchString(a.ID) {
		c.add(p+".id", "E-ACC.id.format", "id %q does not match A<n>", a.ID)
	}
	c.required(p+".ears", "E-ACC.ears.required", a.EARS)
	if a.EARS != "" {
		if _, ok := ears.Match(a.EARS); !ok {
			c.add(p+".ears", "E-ACC.ears.rule", "text does not match any EARS form (F2): %q", a.EARS)
		}
	}
	enum(c, p+".verify", "E-ACC", a.Verify, model.VerifyTest, model.VerifyCommand, model.VerifyReading)
	c.required(p+".verify_detail", "E-ACC.verify_detail.required", a.VerifyDetail)
	if (a.Verify == model.VerifyTest || a.Verify == model.VerifyCommand) && !a.SandboxOK {
		c.add(p+".sandbox_ok", "E-ACC.sandbox_ok.rule", "verify %s requires sandbox_ok = true", a.Verify)
	}
}

func bug(c *collector, p string, b model.BugInfo) {
	c.required(p+".observed", "E-BUG.observed.required", b.Observed)
	c.required(p+".expected", "E-BUG.expected.required", b.Expected)
	if len(b.Repro) < 1 {
		c.add(p+".repro", "E-BUG.repro.min_items", "repro needs at least 1 item")
	}
	enum(c, p+".cause_status", "E-BUG", b.CauseStatus, model.CauseSuspected, model.CauseProven)
	if b.CauseStatus == model.CauseProven {
		if b.CauseProof == nil {
			c.add(p+".cause_proof", "E-BUG.cause_proof.required_when", "cause_proof is required when cause_status == proven")
		}
	}
	if b.CauseProof != nil {
		proof(c, p+".cause_proof", *b.CauseProof)
	}
}

func proof(c *collector, p string, pr model.Proof) {
	enum(c, p+".kind", "E-PROOF", pr.Kind, model.ProofRepro, model.ProofCode)
	switch pr.Kind {
	case model.ProofRepro:
		if pr.Cmd == "" {
			c.add(p+".cmd", "E-PROOF.cmd.required_when", "cmd is required when kind == repro")
		}
		if pr.OutputLine == "" {
			c.add(p+".output_line", "E-PROOF.output_line.required_when", "output_line is required when kind == repro")
		}
	case model.ProofCode:
		if pr.Path == "" {
			c.add(p+".path", "E-PROOF.path.required_when", "path is required when kind == code")
		}
		if pr.Line == 0 {
			c.add(p+".line", "E-PROOF.line.required_when", "line is required when kind == code")
		}
	}
}
