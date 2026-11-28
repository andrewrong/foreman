package api

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Catalog is spec/design/skills.yaml: role -> model, retry upgrade, advisor,
// skills (D-040, D-044, D-059).
type Catalog struct {
	roles map[Role]roleEntry
}

type skillEntry struct {
	Name      string `yaml:"name"`
	Condition string `yaml:"condition"`
}

type roleEntry struct {
	Role         string `yaml:"role"`
	Phase        string `yaml:"phase"`
	Model        string `yaml:"model"`
	RetryUpgrade *struct {
		Model string `yaml:"model"`
	} `yaml:"retry_upgrade"`
	Advisor yaml.Node    `yaml:"advisor"`
	Skills  []skillEntry `yaml:"skills"`
}

// ErrPhaseV2 is returned for roles that are not implemented in v1.
var ErrPhaseV2 = errors.New("harness: role is phase v2")

const defaultAdvisorModel = "opus"

// ParseCatalog decodes skills.yaml (schema foreman.skills/2).
func ParseCatalog(data []byte) (*Catalog, error) {
	var doc struct {
		Schema string      `yaml:"schema"`
		Roles  []roleEntry `yaml:"roles"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("skills catalog: %w", err)
	}
	if doc.Schema != "foreman.skills/2" {
		return nil, fmt.Errorf("skills catalog: unsupported schema %q", doc.Schema)
	}
	c := &Catalog{roles: map[Role]roleEntry{}}
	for _, r := range doc.Roles {
		role := Role(r.Role)
		if !role.Valid() {
			return nil, fmt.Errorf("skills catalog: unknown role %q", r.Role)
		}
		if r.Model == "" {
			return nil, fmt.Errorf("skills catalog: role %s has no model", r.Role)
		}
		c.roles[role] = r
	}
	return c, nil
}

// PlanInput is the per-Run situation that model, advisor and skill choice
// depend on.
type PlanInput struct {
	// Requirement and step facts for skill conditions.
	ReqKind  string // requirement.kind, e.g. "bug"
	StepType string // step.type, e.g. "find_cause"

	// Step counters and caps (state-machine dims.counters); zero caps mean
	// the default (check_fail 3, review_rounds 2).
	CheckFail, CheckFailCap       int
	ReviewRounds, ReviewRoundsCap int

	ModelOverride string // CF-02: owner-configured model for the role
	AdvisorOff    bool   // CF-02: advisor disabled
}

// RolePlan is the resolved choice for one Run.
type RolePlan struct {
	Role         Role
	Model        string
	Upgraded     bool   // retry_upgrade applied (recorded for M-3, D-044 ③)
	AdvisorModel string // empty = no advisor
	Skills       []string
}

// Plan resolves model, advisor and skills for role. Only that role's skills
// are returned and a skill whose condition does not hold is dropped (D-059).
func (c *Catalog) Plan(role Role, in PlanInput) (RolePlan, error) {
	e, ok := c.roles[role]
	if !ok {
		return RolePlan{}, fmt.Errorf("skills catalog: no entry for role %q", role)
	}
	if e.Phase == "v2" {
		return RolePlan{}, fmt.Errorf("%w: %s", ErrPhaseV2, role)
	}
	p := RolePlan{Role: role, Model: e.Model}
	if in.ModelOverride != "" {
		p.Model = in.ModelOverride
	}
	if e.RetryUpgrade != nil && e.RetryUpgrade.Model != "" && escalate(in) {
		p.Model, p.Upgraded = e.RetryUpgrade.Model, true
	}
	if !in.AdvisorOff {
		p.AdvisorModel = advisorModel(e.Advisor)
	}
	facts := map[string]string{"requirement.kind": in.ReqKind, "step.type": in.StepType}
	for _, s := range e.Skills {
		if s.Condition == "" || evalCondition(s.Condition, facts) {
			p.Skills = append(p.Skills, s.Name)
		}
	}
	return p, nil
}

// escalate: the step's check_fail or review_rounds reached cap - 1, so the
// next coder Run uses the upgrade model (state-machine create_run:<role>).
func escalate(in PlanInput) bool {
	cf, rr := in.CheckFailCap, in.ReviewRoundsCap
	if cf == 0 {
		cf = 3
	}
	if rr == 0 {
		rr = 2
	}
	return (in.CheckFail > 0 && in.CheckFail >= cf-1) || (in.ReviewRounds > 0 && in.ReviewRounds >= rr-1)
}

// advisorModel reads `advisor: true`, `advisor: {model, default}` or absence.
func advisorModel(n yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "true" {
			return defaultAdvisorModel
		}
	case yaml.MappingNode:
		var a struct {
			Model   string `yaml:"model"`
			Default string `yaml:"default"`
		}
		if n.Decode(&a) != nil {
			return ""
		}
		if a.Default != "" && a.Default != "on" && a.Default != "true" {
			return ""
		}
		if a.Model == "" {
			return defaultAdvisorModel
		}
		return a.Model
	}
	return ""
}

// evalCondition evaluates "<fact> == <value> && ..." over facts. Anything
// else (free text) is false: an unreadable condition injects nothing.
func evalCondition(cond string, facts map[string]string) bool {
	for _, term := range strings.Split(cond, "&&") {
		k, v, ok := strings.Cut(term, "==")
		if !ok {
			return false
		}
		got, known := facts[strings.TrimSpace(k)]
		if !known || got == "" || got != strings.Trim(strings.TrimSpace(v), `"'`) {
			return false
		}
	}
	return true
}
