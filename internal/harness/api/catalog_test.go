package api

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func specCatalog(t *testing.T) *Catalog {
	t.Helper()
	data, err := os.ReadFile("../../../spec/design/skills.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func plan(t *testing.T, c *Catalog, role Role, in PlanInput) RolePlan {
	t.Helper()
	p, err := c.Plan(role, in)
	if err != nil {
		t.Fatalf("Plan(%s): %v", role, err)
	}
	return p
}

func TestSkillsInjectedByRole(t *testing.T) {
	c := specCatalog(t)
	cases := []struct {
		role Role
		in   PlanInput
		want []string
	}{
		{RoleIntake, PlanInput{}, []string{"grilling", "domain-modeling"}},
		{RolePlanner, PlanInput{}, []string{"codebase-design", "domain-modeling"}},
		{RoleCoder, PlanInput{}, []string{"tdd"}},
		{RoleCoder, PlanInput{ReqKind: "bug", StepType: "find_cause"}, []string{"tdd", "diagnosing-bugs"}},
		{RoleCoder, PlanInput{ReqKind: "bug", StepType: "fix"}, []string{"tdd"}},
		{RoleCoder, PlanInput{ReqKind: "feature", StepType: "find_cause"}, []string{"tdd"}},
		{RoleReviewer, PlanInput{}, []string{"codebase-design", "code-review"}},
		{RoleRescuer, PlanInput{}, []string{"diagnosing-bugs"}},
	}
	for _, tc := range cases {
		if got := plan(t, c, tc.role, tc.in).Skills; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %+v: skills %v, want %v", tc.role, tc.in, got, tc.want)
		}
	}
	for _, r := range []Role{RoleDiagnoser, RoleImprover} {
		if _, err := c.Plan(r, PlanInput{}); !errors.Is(err, ErrPhaseV2) {
			t.Errorf("%s: want ErrPhaseV2, got %v", r, err)
		}
	}
}

func TestRetryModelEscalation(t *testing.T) {
	c := specCatalog(t)
	cases := []struct {
		name     string
		role     Role
		in       PlanInput
		model    string
		upgraded bool
	}{
		{"first run", RoleCoder, PlanInput{}, "sonnet", false},
		{"check_fail 1 of cap 3", RoleCoder, PlanInput{CheckFail: 1}, "sonnet", false},
		{"check_fail 2 = cap-1", RoleCoder, PlanInput{CheckFail: 2}, "opus", true},
		{"review_rounds 1 = cap-1", RoleCoder, PlanInput{ReviewRounds: 1}, "opus", true},
		{"explicit caps", RoleCoder, PlanInput{CheckFail: 3, CheckFailCap: 5}, "sonnet", false},
		{"explicit caps reached", RoleCoder, PlanInput{CheckFail: 4, CheckFailCap: 5}, "opus", true},
		{"reviewer never upgrades", RoleReviewer, PlanInput{CheckFail: 2}, "opus", false},
		{"override applies below cap", RoleCoder, PlanInput{ModelOverride: "haiku"}, "haiku", false},
		{"upgrade beats override", RoleCoder, PlanInput{ModelOverride: "haiku", CheckFail: 2}, "opus", true},
	}
	for _, tc := range cases {
		p := plan(t, c, tc.role, tc.in)
		if p.Model != tc.model || p.Upgraded != tc.upgraded {
			t.Errorf("%s: got model %s upgraded %v, want %s %v", tc.name, p.Model, p.Upgraded, tc.model, tc.upgraded)
		}
	}
}

func TestPlanAdvisor(t *testing.T) {
	c := specCatalog(t)
	if got := plan(t, c, RoleCoder, PlanInput{}).AdvisorModel; got != "opus" {
		t.Errorf("coder advisor %q, want opus (default on)", got)
	}
	if got := plan(t, c, RoleCoder, PlanInput{AdvisorOff: true}).AdvisorModel; got != "" {
		t.Errorf("coder advisor off: %q", got)
	}
	if got := plan(t, c, RoleRescuer, PlanInput{}).AdvisorModel; got != "opus" {
		t.Errorf("rescuer advisor %q, want opus", got)
	}
	for _, r := range []Role{RoleIntake, RolePlanner, RoleReviewer} {
		if got := plan(t, c, r, PlanInput{}).AdvisorModel; got != "" {
			t.Errorf("%s advisor %q, want none", r, got)
		}
	}
}

func TestParseCatalogRejects(t *testing.T) {
	for name, y := range map[string]string{
		"schema":  "schema: foreman.skills/1\nroles: []\n",
		"role":    "schema: foreman.skills/2\nroles: [{role: bogus, model: x}]\n",
		"nomodel": "schema: foreman.skills/2\nroles: [{role: coder}]\n",
	} {
		if _, err := ParseCatalog([]byte(y)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
