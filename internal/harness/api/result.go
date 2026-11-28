package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Result file schema versions foremand accepts (protocol-run compat: the
// current and the previous one; today only 1 exists).
const (
	ResultSchemaVersion    = 1
	minResultSchemaVersion = 1
)

// Result mirrors PF-RESULT. Finding and Discovery are defined here because
// internal/spec/model has no Finding/Discovery types yet (entities E-FIND,
// E-DISC are stored in work.db); the shapes below are the session-written
// subset.
type Result struct {
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	Role          Role             `json:"role"`
	Status        string           `json:"status"`
	Commits       []string         `json:"commits,omitempty"`
	Acceptance    []AcceptanceItem `json:"acceptance,omitempty"`
	Findings      *[]Finding       `json:"findings,omitempty"`
	Verdict       string           `json:"verdict,omitempty"`
	ClosedFinds   []string         `json:"closed_findings,omitempty"`
	Discoveries   []Discovery      `json:"discoveries,omitempty"`
	Decision      string           `json:"decision,omitempty"`
	Guidance      string           `json:"guidance,omitempty"`
	Access        *AccessRequest   `json:"access,omitempty"`
	Note          string           `json:"note,omitempty"`
}

// AcceptanceItem is one coder acceptance self-report.
type AcceptanceItem struct {
	ID       string `json:"id"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
}

// Location is Finding.location.
type Location struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Finding is a reviewer finding as written by the session: no id, status or
// round; kind is fixed to review (PF-RESULT).
type Finding struct {
	Scope     string    `json:"scope"`
	Severity  string    `json:"severity"`
	Location  *Location `json:"location"`
	Problem   string    `json:"problem"`
	CloseWhen string    `json:"close_when"`

	// Fields a session must not set; present only to detect them.
	ID     *string `json:"id,omitempty"`
	Kind   *string `json:"kind,omitempty"`
	Status *string `json:"status,omitempty"`
	Round  *int    `json:"round,omitempty"`
}

// Evidence is {path, line} or {cmd, output_line}.
type Evidence struct {
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Cmd        string `json:"cmd,omitempty"`
	OutputLine string `json:"output_line,omitempty"`
}

// Discovery is E-DISC as written by the session.
type Discovery struct {
	Kind       string    `json:"kind"`
	Ref        string    `json:"ref,omitempty"`
	Text       string    `json:"text"`
	Evidence   *Evidence `json:"evidence,omitempty"`
	Supersedes string    `json:"supersedes,omitempty"`
}

// AccessRequest is result.access.
type AccessRequest struct {
	Resource string `json:"resource"`
	Reason   string `json:"reason"`
}

// ResultContext is what foremand knows about the run being validated.
type ResultContext struct {
	RunID string
	Role  Role
	// Scope is "step" or "requirement"; it selects the rescuer decision enum.
	Scope string
	// AcceptanceIDs are the current step's acceptance ids; a coder ok result
	// must cover all of them (empty = no coverage check).
	AcceptanceIDs []string
}

// ValidationError lists every violation found (bad_result, PP-4).
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "result.json invalid: " + strings.Join(e.Problems, "; ")
}

func discKinds() []string {
	return []string{"answer", "extra_file", "spec_conflict", "cause_proof", "access_request", "note"}
}

func reqDecisions() []string {
	return []string{"retry", "replan", "drop_requirement", "ask_owner"}
}

func stepDecisions() []string {
	return []string{"retry", "drop_step", "replan", "ask_owner"}
}

func in(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// ValidateResult decodes and validates result.json against PF-RESULT. On
// success it returns the Result; otherwise a *ValidationError (or a plain
// error for undecodable JSON).
func ValidateResult(raw []byte, ctx ResultContext) (*Result, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var r Result
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("result.json invalid: %w", err)
	}
	if dec.More() {
		return nil, errors.New("result.json invalid: trailing data")
	}
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }

	switch {
	case r.SchemaVersion > ResultSchemaVersion:
		bad("schema_version %d is newer than supported %d", r.SchemaVersion, ResultSchemaVersion)
	case r.SchemaVersion < minResultSchemaVersion:
		bad("schema_version %d is not supported", r.SchemaVersion)
	}
	if r.RunID != ctx.RunID {
		bad("run_id %q does not match run %q", r.RunID, ctx.RunID)
	}
	if r.Role != ctx.Role {
		bad("role %q does not match run role %q", r.Role, ctx.Role)
	}
	switch r.Status {
	case "ok", "spec_conflict", "access_request":
	default:
		bad("status %q must be ok, spec_conflict or access_request", r.Status)
	}
	if len(r.Note) > 500 {
		bad("note longer than 500")
	}
	ok := r.Status == "ok"

	if r.Status == "access_request" && (r.Access == nil || r.Access.Resource == "" || r.Access.Reason == "") {
		bad("access {resource, reason} required when status == access_request")
	}
	if ok && ctx.Role == RoleCoder {
		validateCoder(&r, ctx, bad)
	}
	if ok && ctx.Role == RoleReviewer {
		validateReviewer(&r, ctx, bad)
	}
	if ok && ctx.Role == RoleRescuer {
		validateRescuer(&r, ctx, bad)
	}
	for i, d := range r.Discoveries {
		validateDiscovery(i, d, bad)
	}
	if len(p) > 0 {
		return nil, &ValidationError{Problems: p}
	}
	return &r, nil
}

func validateCoder(r *Result, ctx ResultContext, bad func(string, ...any)) {
	if len(r.Commits) < 1 {
		bad("commits: at least 1 required for coder ok")
	}
	seen := map[string]bool{}
	for _, a := range r.Acceptance {
		if a.ID == "" {
			bad("acceptance item without id")
		}
		if !in(a.Result, []string{"pass", "fail", "cannot_check"}) {
			bad("acceptance %s: result %q must be pass, fail or cannot_check", a.ID, a.Result)
		}
		seen[a.ID] = true
	}
	if len(r.Acceptance) == 0 && len(ctx.AcceptanceIDs) == 0 {
		bad("acceptance required for coder ok")
	}
	for _, id := range ctx.AcceptanceIDs {
		if !seen[id] {
			bad("acceptance %s not covered", id)
		}
	}
}

func validateReviewer(r *Result, _ ResultContext, bad func(string, ...any)) {
	if r.Findings == nil {
		bad("findings required for reviewer ok")
	}
	if r.Verdict != "approve" && r.Verdict != "changes" {
		bad("verdict %q must be approve or changes", r.Verdict)
	}
	if r.Findings == nil {
		return
	}
	blocking := false
	for i, f := range *r.Findings {
		if f.ID != nil || f.Status != nil || f.Round != nil {
			bad("finding %d: id, status and round are set by foremand", i)
		}
		if f.Kind != nil && *f.Kind != "review" {
			bad("finding %d: kind must be review", i)
		}
		if f.Scope == "" || f.Problem == "" || f.CloseWhen == "" {
			bad("finding %d: scope, problem and close_when are required", i)
		}
		if len(f.Problem) > 300 || len(f.CloseWhen) > 200 {
			bad("finding %d: problem max 300, close_when max 200", i)
		}
		switch f.Severity {
		case "blocking":
			blocking = true
		case "minor":
		default:
			bad("finding %d: severity %q must be blocking or minor", i, f.Severity)
		}
		if f.Location == nil || f.Location.Path == "" {
			bad("finding %d: location.path required", i)
		}
	}
	if blocking && r.Verdict == "approve" {
		bad("blocking finding present but verdict is approve")
	}
}

func validateRescuer(r *Result, ctx ResultContext, bad func(string, ...any)) {
	set := stepDecisions()
	if ctx.Scope == "requirement" {
		set = reqDecisions()
	}
	if !in(r.Decision, set) {
		bad("decision %q not in %v for %s scope", r.Decision, set, ctx.Scope)
	}
	if r.Decision == "retry" && r.Guidance == "" {
		bad("guidance required when decision == retry")
	}
	if len(r.Guidance) > 1000 {
		bad("guidance longer than 1000")
	}
}

func validateDiscovery(i int, d Discovery, bad func(string, ...any)) {
	if !in(d.Kind, discKinds()) {
		bad("discovery %d: kind %q unknown", i, d.Kind)
	}
	if d.Text == "" || len(d.Text) > 500 {
		bad("discovery %d: text required, max 500", i)
	}
	if in(d.Kind, []string{"answer", "spec_conflict", "extra_file"}) {
		e := d.Evidence
		if e == nil || !((e.Path != "" && e.Line > 0) || (e.Cmd != "" && e.OutputLine != "")) {
			bad("discovery %d: evidence {path,line} or {cmd,output_line} required for kind %s", i, d.Kind)
		}
	}
}
