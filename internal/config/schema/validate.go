package schema

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	repoRE        = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchRE      = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	modelRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-\[\]]*$`)
	itemRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	efforts       = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	dbKinds       = map[string]bool{"sqlite": true, "postgres": true, "mysql": true}
)

func validProjectName(n string) bool { return projectNameRE.MatchString(n) }

// checker accumulates errors for one file.
type checker struct {
	file string
	errs []error
}

func (c *checker) bad(key, format string, a ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s: %s", c.file, key, fmt.Sprintf(format, a...)))
}

func (c *checker) rangeInt(key string, v, lo, hi int) {
	if v < lo || v > hi {
		c.bad(key, "%d out of range [%d, %d]", v, lo, hi)
	}
}

func (c *checker) absPath(key, v string, required bool) {
	switch {
	case v == "" && required:
		c.bad(key, "required")
	case v != "" && !filepath.IsAbs(v):
		c.bad(key, "must be an absolute path")
	case strings.ContainsRune(v, 0):
		c.bad(key, "contains NUL")
	}
}

func validateRuntime(file string, r *Runtime) []error {
	c := &checker{file: file}
	c.absPath("secrets_dir", r.SecretsDir, false)
	c.rangeInt("parallel.workers", r.Parallel.Workers, 1, 64)
	c.rangeInt("parallel.steps_per_requirement", r.Parallel.StepsPerRequirement, 1, 64)
	l := r.Limits
	for _, f := range []struct {
		k string
		v int
	}{
		{"limits.check_fail", l.CheckFail}, {"limits.review_rounds", l.ReviewRounds},
		{"limits.merge_conflict", l.MergeConflict}, {"limits.bad_result", l.BadResult},
		{"limits.infra", l.Infra}, {"limits.replans", l.Replans},
		{"limits.planner_failures", l.PlannerFails}, {"limits.req_rescues", l.ReqRescues},
		{"limits.fix_steps", l.FixSteps},
	} {
		c.rangeInt(f.k, f.v, 1, 100)
	}
	c.rangeInt("limits.pr_split_lines", l.PRSplitLines, 50, 100000)
	if l.H4BudgetFactor < 1 || l.H4BudgetFactor > 20 {
		c.bad("limits.h4_budget_multiplier", "%v out of range [1, 20]", l.H4BudgetFactor)
	}
	c.rangeInt("improve.min_interval_hours", r.Improve.MinIntervalHours, 1, 24*365)
	c.rangeInt("improve.weekly_cap", r.Improve.WeeklyCap, 0, 100)
	x := r.Resources
	c.rangeInt("resources.github_rest_per_hour", x.GitHubRestPerHour, 1, 1_000_000)
	c.rangeInt("resources.github_graphql_points_per_hour", x.GitHubGraphQLPerHour, 1, 1_000_000)
	c.rangeInt("resources.github_write_per_minute", x.GitHubWritePerMinute, 1, 10_000)
	c.rangeInt("resources.github_write_per_hour", x.GitHubWritePerHour, 1, 100_000)
	c.rangeInt("resources.github_merge_push_pr_reserve", x.GitHubMergeReserve, 0, 1_000_000)
	c.rangeInt("resources.l0_slots", x.L0Slots, 1, 64)
	c.rangeInt("resources.disk_floor_gib", x.DiskFloorGiB, 0, 1_000_000)
	c.rangeInt("resources.model_quota_floor_percent", x.QuotaFloorPercent, 0, 100)
	c.rangeInt("resources.model_quota_warn_percent", x.QuotaWarnPercent, 0, 100)
	if x.QuotaWarnPercent < x.QuotaFloorPercent {
		c.bad("resources.model_quota_warn_percent", "must not be below model_quota_floor_percent")
	}
	if x.GitHubWritePerMinute > x.GitHubWritePerHour {
		c.bad("resources.github_write_per_minute", "must not exceed github_write_per_hour")
	}
	return c.errs
}

func validateRoles(file string, roles map[string]Role) []error {
	c := &checker{file: file}
	for _, n := range RoleNames {
		r := roles[n]
		k := "role." + n + "."
		if !modelRE.MatchString(r.Model) {
			c.bad(k+"model", "invalid model name %q", r.Model)
		}
		if !efforts[r.Effort] {
			c.bad(k+"effort", "invalid effort %q (low, medium, high, xhigh, max or empty)", r.Effort)
		}
		c.rangeInt(k+"max_tokens", r.MaxTokens, 0, 100_000_000)
		if d := r.Timeout.D(); d < time.Minute || d > 24*time.Hour {
			c.bad(k+"timeout", "%s out of range [1m, 24h]", d)
		}
		if r.Advisor && !modelRE.MatchString(r.AdvisorModel) {
			c.bad(k+"advisor_model", "advisor is on but model %q is invalid", r.AdvisorModel)
		}
		if r.RetryUpgradeModel != "" && !modelRE.MatchString(r.RetryUpgradeModel) {
			c.bad(k+"retry_upgrade_model", "invalid model name %q", r.RetryUpgradeModel)
		}
	}
	return c.errs
}

func validateProject(file string, p *Project) []error {
	c := &checker{file: file}
	c.absPath("repo_path", p.RepoPath, true)
	if p.GitHubRepo != "" && !repoRE.MatchString(p.GitHubRepo) {
		c.bad("github_repo", "want owner/name, got %q", p.GitHubRepo)
	}
	if !branchRE.MatchString(p.DefaultBranch) || strings.Contains(p.DefaultBranch, "..") {
		c.bad("default_branch", "invalid branch name %q", p.DefaultBranch)
	}
	seen := map[string]bool{}
	for i, s := range p.Services {
		k := fmt.Sprintf("service[%d]", i)
		if !itemRE.MatchString(s.Name) {
			c.bad(k+".name", "invalid name %q", s.Name)
		} else if seen[s.Name] {
			c.bad(k+".name", "duplicate service %q", s.Name)
		}
		seen[s.Name] = true
		c.absPath(k+".log_path", s.LogPath, false)
	}
	seen = map[string]bool{}
	for i, d := range p.Databases {
		k := fmt.Sprintf("database[%d]", i)
		if !itemRE.MatchString(d.Name) {
			c.bad(k+".name", "invalid name %q", d.Name)
		} else if seen[d.Name] {
			c.bad(k+".name", "duplicate database %q", d.Name)
		}
		seen[d.Name] = true
		if !dbKinds[d.Kind] {
			c.bad(k+".kind", "invalid kind %q (sqlite, postgres, mysql)", d.Kind)
		}
		if d.Path == "" || strings.Contains(d.Path, "://") && strings.Contains(d.Path, "@") {
			c.bad(k+".path", "required, and must not embed credentials")
		}
	}
	for i, re := range p.PIIMask {
		if _, err := regexp.Compile(re); err != nil {
			c.bad(fmt.Sprintf("pii_mask[%d]", i), "invalid regular expression: %v", err)
		}
	}
	for i, argv := range p.L0 {
		if len(argv) == 0 || argv[0] == "" {
			c.bad(fmt.Sprintf("l0[%d]", i), "argv must have a program")
		}
	}
	b := p.L0Budget
	if d := b.Timeout.D(); d < time.Second || d > time.Hour {
		c.bad("l0_budget.timeout", "%s out of range [1s, 1h]", d)
	}
	c.rangeInt("l0_budget.gomaxprocs", b.GoMaxProcs, 1, 64)
	c.rangeInt("l0_budget.memory_mb", b.MemoryMB, 256, 1_048_576)
	for _, l := range []struct {
		k string
		v []string
	}{{"access.r1_declared", p.Access.R1Declared}, {"access.r2_approved", p.Access.R2Approved}} {
		for i, v := range l.v {
			if !itemRE.MatchString(v) {
				c.bad(fmt.Sprintf("%s[%d]", l.k, i), "invalid resource name %q", v)
			}
		}
	}
	c.rangeInt("prod.rows_max", p.Prod.RowsMax, 1, 10000)
	return c.errs
}
