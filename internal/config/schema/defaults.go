package schema

import "time"

// RoleNames lists the roles a roles.toml may configure (skills.yaml).
var RoleNames = []string{"intake", "planner", "coder", "reviewer", "rescuer", "diagnoser", "improver"}

// DefaultRoles returns the per-role defaults. Models follow skills.yaml and
// D-040/D-044/D-065: coder and intake sonnet, reviewer opus (writing and
// reviewing use different models), coder advisor on with an opus advisor and
// opus for the retry after cap-1. Effort, token cap and timeout have no
// spec value: effort is left to the harness, max_tokens 0 means only
// max_turns and timeout bound a session (protocol-run PP-5).
func DefaultRoles() map[string]Role {
	r := func(model string, advisor bool) Role {
		x := Role{Model: model, Timeout: Duration(30 * time.Minute), Advisor: advisor}
		if advisor {
			x.AdvisorModel = "opus"
		}
		return x
	}
	roles := map[string]Role{
		"intake":    r("sonnet", false),
		"planner":   r("opus", false),
		"coder":     r("sonnet", true),
		"reviewer":  r("opus", false),
		"rescuer":   r("opus", true),
		"diagnoser": r("opus", false),
		"improver":  r("opus", true),
	}
	c := roles["coder"]
	c.RetryUpgradeModel = "opus"
	roles["coder"] = c
	return roles
}

// DefaultRuntime returns runtime defaults. Retry caps are the ladder of
// D-053 (check_fail 3, review_rounds 2, merge_conflict 2, bad_result 2,
// infra 6, replans 2, planner_failures 2, req_rescues 2, fix_steps 3);
// resource numbers are those of design/resources.yaml.
func DefaultRuntime() Runtime {
	return Runtime{
		Parallel: Parallel{Workers: 2, StepsPerRequirement: 3},
		Limits: Limits{
			CheckFail: 3, ReviewRounds: 2, MergeConflict: 2, BadResult: 2, Infra: 6,
			Replans: 2, PlannerFails: 2, ReqRescues: 2, FixSteps: 3,
			PRSplitLines: 800, H4BudgetFactor: 2, // pr_split_lines: D-006/D-054 (split is v2; v1 keeps one PR)
		},
		Improve: Improve{MinIntervalHours: 24, WeeklyCap: 3},
		Resources: ResourceCfg{
			GitHubRestPerHour: 5000, GitHubGraphQLPerHour: 5000,
			GitHubWritePerMinute: 60, GitHubWritePerHour: 400, GitHubMergeReserve: 500,
			L0Slots: 2, DiskFloorGiB: 10, QuotaFloorPercent: 10, QuotaWarnPercent: 30,
		},
	}
}

// DefaultProject returns project defaults (CF-12: 120s / 2 / 2GB; CF-07: the
// production SQL tool is off, 500 rows when enabled per prod-access.yaml).
func DefaultProject(name string) Project {
	return Project{
		Name:          name,
		DefaultBranch: "main",
		L0Budget:      L0Budget{Timeout: Duration(120 * time.Second), GoMaxProcs: 2, MemoryMB: 2048},
		Prod:          Prod{RowsMax: 500},
	}
}

// Defaults returns the effective configuration with no files present.
func Defaults() *Effective {
	return &Effective{Runtime: DefaultRuntime(), Roles: DefaultRoles(), Projects: map[string]Project{}}
}
