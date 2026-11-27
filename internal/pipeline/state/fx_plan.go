package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// PlanDiff is the validated plan.json of a planner Run (protocol-plan.yaml).
// Event.Plan carries it to apply_plan_diff, which re-checks the rules that
// need the current database (PV-1, PV-3, PV-9) and the base version.
type PlanDiff struct {
	RunID           string // the planner Run, to find its snapshot (PV-9)
	BaseSpecVersion int
	SpecPatch       *SpecPatch
	Steps           []PlanStep
}

// SpecPatch holds the Spec fields a plan may change; nil means unchanged.
type SpecPatch struct {
	Change     *[]string
	Outcome    *[]model.Acceptance
	Boundaries *[]string
	Unknowns   *[]model.Unknown
	Hints      *[]string
}

// PlanStep is one entry of plan.json steps.
type PlanStep struct {
	Op         string // keep|update|drop|add
	ID         string
	TempID     string // N<n>, op add
	Type       string // build|find_cause|fix, op add
	Goal       string
	Acceptance []string
	DependsOn  []string // step ids or temp ids
	Touches    []string
	Reason     string
}

// snapshotBasis returns the steps PV-1 is judged against: the planner Run's
// snapshot (PV-9) or, without one, the current steps.
func (c *txc) snapshotBasis(runID string, steps []*stepRec) (map[string]bool, error) {
	basis := map[string]bool{}
	if runID != "" {
		var text sql.NullString
		err := c.tx.QueryRowContext(c.ctx, `SELECT p.steps FROM runs r JOIN plan_snapshots p ON p.id = r.snapshot_id
			WHERE r.id = ? AND r.req_id = ?`, runID, c.req.id).Scan(&text)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if text.Valid {
			var snap []struct{ ID, State string }
			if err := json.Unmarshal([]byte(text.String), &snap); err != nil {
				return nil, err
			}
			for _, s := range snap {
				if s.State != "done" && s.State != "dropped" {
					basis[s.ID] = true
				}
			}
			return basis, nil
		}
	}
	for _, s := range steps {
		if s.state != "done" && s.state != "dropped" {
			basis[s.id] = true
		}
	}
	return basis, nil
}

func fxApplyPlanDiff(c *txc, _ string) error {
	p := c.ev.Plan
	if p == nil {
		return fmt.Errorf("apply_plan_diff: event %s carries no plan", c.ev.Name)
	}
	if p.BaseSpecVersion != c.req.specVersion {
		return fmt.Errorf("apply_plan_diff: base_spec_version %d, requirement is at %d", p.BaseSpecVersion, c.req.specVersion)
	}
	steps, err := loadSteps(c.ctx, c.tx, c.req.id)
	if err != nil {
		return err
	}
	byID := map[string]*stepRec{}
	maxSeq := 0
	for _, s := range steps {
		byID[s.id] = s
		maxSeq = max(maxSeq, s.seq)
	}
	basis, err := c.snapshotBasis(p.RunID, steps)
	if err != nil {
		return err
	}

	// PV-1: every non-terminal, non-integrate step of the basis exactly once.
	seen := map[string]bool{}
	drops := map[string]string{} // id -> reason
	var updates, adds []PlanStep
	for i, ps := range p.Steps {
		switch ps.Op {
		case "keep", "update", "drop":
			s := byID[ps.ID]
			if s == nil {
				return fmt.Errorf("plan step %d: unknown step %q", i, ps.ID)
			}
			if seen[ps.ID] {
				return fmt.Errorf("plan step %d: %s appears twice (PV-1)", i, ps.ID)
			}
			seen[ps.ID] = true
			if s.typ == "integrate" {
				return fmt.Errorf("plan step %d: integrate step %s is not planned (DT-2)", i, ps.ID)
			}
			if s.state == "dropped" || (s.state == "done" && !basis[s.id]) {
				return fmt.Errorf("plan step %d: %s is terminal (PV-1)", i, ps.ID)
			}
			if s.state == "done" { // finished since the snapshot: keep/update mean keep, drop is invalid (PV-9)
				continue
			}
			if ps.Op == "drop" {
				drops[ps.ID] = ps.Reason
			}
			if ps.Op == "update" {
				updates = append(updates, ps)
			}
		case "add":
			adds = append(adds, ps)
		default:
			return fmt.Errorf("plan step %d: op %q", i, ps.Op)
		}
	}
	for id := range basis {
		if byID[id] != nil && byID[id].typ != "integrate" && !seen[id] {
			return fmt.Errorf("apply_plan_diff: step %s missing from the plan (PV-1)", id)
		}
	}
	// Non-done integrate steps are dropped; DT-2 recreates them.
	for _, s := range steps {
		if s.typ == "integrate" && s.state != "done" && s.state != "dropped" {
			drops[s.id] = "integrate step recreated by DT-2 after replan"
		}
	}

	// New ids, then dependency resolution and the acyclicity check (PV-3).
	temp := map[string]string{}
	for _, ps := range adds {
		if ps.TempID == "" || temp[ps.TempID] != "" {
			return fmt.Errorf("add step: temp_id %q missing or duplicated", ps.TempID)
		}
		maxSeq++
		temp[ps.TempID] = fmt.Sprintf("%s/S%d", c.req.id, maxSeq)
	}
	deps := map[string][]string{} // all steps that stay
	for _, s := range steps {
		if _, dropped := drops[s.id]; !dropped && s.state != "dropped" {
			deps[s.id] = c.dependsOf(s.id)
		}
	}
	resolve := func(refs []string) ([]string, error) {
		out := []string{}
		for _, r := range refs {
			if id, ok := temp[r]; ok {
				r = id
			} else if _, dropping := drops[r]; byID[r] == nil || byID[r].state == "dropped" || dropping {
				return nil, fmt.Errorf("depends_on %q does not exist or is dropped (PV-3)", r)
			}
			out = append(out, r)
		}
		return out, nil
	}
	newDeps := map[string][]string{}
	for _, ps := range updates {
		d, err := resolve(ps.DependsOn)
		if err != nil {
			return err
		}
		newDeps[ps.ID], deps[ps.ID] = d, d
	}
	for _, ps := range adds {
		d, err := resolve(ps.DependsOn)
		if err != nil {
			return err
		}
		newDeps[temp[ps.TempID]], deps[temp[ps.TempID]] = d, d
	}
	if cyc := findCycle(deps); cyc != nil {
		return fmt.Errorf("apply_plan_diff: dependency cycle %s (PV-3)", strings.Join(cyc, " -> "))
	}

	for id, reason := range drops {
		if err := c.killRuns(`step_id = ?`, id); err != nil {
			return err
		}
		if err := c.dropSteps(`id = ?`, firstNonEmpty(reason, "dropped by plan"), id); err != nil {
			return err
		}
	}
	for _, ps := range updates {
		if err := c.updateStep(byID[ps.ID], ps, newDeps[ps.ID]); err != nil {
			return err
		}
	}
	for _, ps := range adds {
		if err := c.addStep(temp[ps.TempID], maxSeqOf(temp[ps.TempID]), ps, newDeps[temp[ps.TempID]]); err != nil {
			return err
		}
	}
	return c.applySpecPatch(p.SpecPatch)
}

func maxSeqOf(id string) int {
	_, n, _ := strings.Cut(id, "/S")
	var v int
	fmt.Sscanf(n, "%d", &v)
	return v
}

func (c *txc) dependsOf(id string) []string {
	var text string
	if err := c.tx.QueryRowContext(c.ctx, `SELECT depends_on FROM steps WHERE id = ?`, id).Scan(&text); err != nil {
		return nil
	}
	var d []string
	_ = json.Unmarshal([]byte(text), &d)
	return d
}

func checkPlanFields(ps PlanStep) error {
	if ps.Goal == "" || utf8.RuneCountInString(ps.Goal) > 300 {
		return fmt.Errorf("step goal must be 1..300 characters")
	}
	if len(ps.Acceptance) == 0 || len(ps.Touches) == 0 {
		return fmt.Errorf("step %s: acceptance and touches need at least 1 item", firstNonEmpty(ps.ID, ps.TempID))
	}
	return nil
}

// updateStep applies an update op: fields change, counters stay, progress
// restarts at the first stage (waiting(hold) via resume_stage).
func (c *txc) updateStep(s *stepRec, ps PlanStep, deps []string) error {
	if err := checkPlanFields(ps); err != nil {
		return err
	}
	if _, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET goal = ?, acceptance = ?, depends_on = ?, touches = ? WHERE id = ?`,
		ps.Goal, jsonText(ps.Acceptance), jsonText(deps), jsonText(ps.Touches), s.id); err != nil {
		return err
	}
	first := firstStageOf(s.typ)
	switch {
	case s.state == "waiting" && s.reason == "hold":
		_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET resume_stage = ? WHERE id = ?`, first, s.id)
		return err
	case s.state == "queued":
		_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET stage = ? WHERE id = ?`, first, s.id)
		return err
	}
	return nil
}

func (c *txc) addStep(id string, seq int, ps PlanStep, deps []string) error {
	if err := checkPlanFields(ps); err != nil {
		return err
	}
	if !slices.Contains([]string{"build", "find_cause", "fix"}, ps.Type) {
		return fmt.Errorf("add step %s: type %q", ps.TempID, ps.Type)
	}
	role := "coder"
	if ps.Type == "find_cause" && c.req.source == "prod" {
		role = "diagnoser"
	}
	_, err := c.tx.ExecContext(c.ctx, `INSERT INTO steps (id, req_id, seq, type, code_role, goal, acceptance,
		depends_on, touches, state, stage, deadline, counters)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, '{}')`,
		id, c.req.id, seq, ps.Type, role, ps.Goal, jsonText(ps.Acceptance), jsonText(deps), jsonText(ps.Touches),
		firstStageOf(ps.Type), fmtTime(c.now.Add(c.e.cfg.Deadlines.Queued)))
	return err
}

// applySpecPatch bumps spec_version, keeping the replaced version.
func (c *txc) applySpecPatch(p *SpecPatch) error {
	if p == nil {
		return nil
	}
	var sp model.Spec
	if err := json.Unmarshal([]byte(c.req.spec), &sp); err != nil {
		return err
	}
	if _, err := c.tx.ExecContext(c.ctx, `INSERT INTO req_spec_versions (req_id, version, spec, saved_at) VALUES (?, ?, ?, ?)`,
		c.req.id, c.req.specVersion, c.req.spec, fmtTime(c.now)); err != nil {
		return err
	}
	if p.Change != nil {
		sp.Change = *p.Change
	}
	if p.Outcome != nil {
		sp.Outcome = *p.Outcome
	}
	if p.Boundaries != nil {
		sp.Boundaries = *p.Boundaries
	}
	if p.Unknowns != nil {
		sp.Unknowns = *p.Unknowns
	}
	if p.Hints != nil {
		sp.Hints = *p.Hints
	}
	c.req.spec, c.req.specVersion, c.req.dirty = jsonText(sp), c.req.specVersion+1, true
	return nil
}

// findCycle returns one cycle of the graph as a node path, or nil.
func findCycle(g map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack, cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range g[n] {
			switch color[m] {
			case grey:
				i := slices.Index(stack, m)
				cycle = append(slices.Clone(stack[i:]), m)
				return true
			case white:
				if _, ok := g[m]; ok && visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if color[k] == white && visit(k) {
			return cycle
		}
	}
	return nil
}
