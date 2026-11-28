// Package schema loads and validates the owner's configuration files under
// FOREMAN_HOME/config (config.yaml CF-01..CF-12). Defaults live here, in code
// (CD-05); a config file lists only overrides. Unknown keys and bad values
// are rejected with errors that name the file and key.
//
// Files: runtime.toml (CF-04, CF-05, CF-09, CF-11, secrets_dir), roles.toml
// (CF-02) and projects/<name>.toml (CF-01, CF-06, CF-07, CF-12). Secrets are
// never configuration: only the secrets_dir path appears here (D-026).
package schema

import (
	"fmt"
	"time"
)

// Duration is a TOML string such as "120s" or "45m".
type Duration time.Duration

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("invalid duration %q (want e.g. \"120s\")", string(b))
	}
	*d = Duration(v)
	return nil
}

// MarshalText renders the duration as a Go duration string.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// D converts to time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Effective is the validated configuration with defaults applied.
type Effective struct {
	Runtime  Runtime
	Roles    map[string]Role
	Projects map[string]Project
}

// Models returns role -> model name, the shape cmd/foreman Config.Models uses.
func (e *Effective) Models() map[string]string {
	m := make(map[string]string, len(e.Roles))
	for name, r := range e.Roles {
		m[name] = r.Model
	}
	return m
}

// Runtime is runtime.toml.
type Runtime struct {
	// SecretsDir is the directory holding foreman.<name> secret files
	// (D-026). Empty means not configured; readers then refuse.
	SecretsDir string      `toml:"secrets_dir"`
	Parallel   Parallel    `toml:"parallel"`    // CF-04
	Limits     Limits      `toml:"limits"`      // CF-05
	Improve    Improve     `toml:"improve"`     // CF-09
	Resources  ResourceCfg `toml:"resources"`   // CF-11
	Sandbox    Sandbox     `toml:"sandbox"`     // D-080, D-081
	SessionGit SessionGit  `toml:"session_git"` // D-082
}

// Sandbox holds the owner-supplied paths of the read allowlist (D-080, D-081).
// Nothing here is inferred by the program.
type Sandbox struct {
	SrtPath   string   `toml:"srt_path"`   // srt executable
	NodeDir   string   `toml:"node_dir"`   // real node directory, not a mise shim
	ReadPaths []string `toml:"read_paths"` // extra readable paths (toolchains)
	DenyRoots []string `toml:"deny_roots"` // owner home, data volume roots
}

// SessionGit is the identity of the generated session .gitconfig (D-082).
type SessionGit struct {
	Name  string `toml:"name"`
	Email string `toml:"email"` // a noreply address (privguard P4)
}

// Parallel is CF-04.
type Parallel struct {
	Workers             int `toml:"workers"`               // global session workers
	StepsPerRequirement int `toml:"steps_per_requirement"` // parallel steps per requirement
}

// Limits is CF-05: retry caps (D-053 ladder), review rounds, PR split, H4.
type Limits struct {
	CheckFail      int     `toml:"check_fail"`
	ReviewRounds   int     `toml:"review_rounds"`
	MergeConflict  int     `toml:"merge_conflict"`
	BadResult      int     `toml:"bad_result"`
	Infra          int     `toml:"infra"`
	Replans        int     `toml:"replans"`
	PlannerFails   int     `toml:"planner_failures"`
	ReqRescues     int     `toml:"req_rescues"`
	FixSteps       int     `toml:"fix_steps"`
	PRSplitLines   int     `toml:"pr_split_lines"`
	H4BudgetFactor float64 `toml:"h4_budget_multiplier"`
}

// Improve is CF-09 (the improve stage is phase v2).
type Improve struct {
	MinIntervalHours int `toml:"min_interval_hours"`
	WeeklyCap        int `toml:"weekly_cap"`
}

// ResourceCfg is CF-11 (design/resources.yaml numbers).
type ResourceCfg struct {
	GitHubRestPerHour    int `toml:"github_rest_per_hour"`
	GitHubGraphQLPerHour int `toml:"github_graphql_points_per_hour"`
	GitHubWritePerMinute int `toml:"github_write_per_minute"`
	GitHubWritePerHour   int `toml:"github_write_per_hour"`
	GitHubMergeReserve   int `toml:"github_merge_push_pr_reserve"`
	L0Slots              int `toml:"l0_slots"`
	DiskFloorGiB         int `toml:"disk_floor_gib"`
	QuotaFloorPercent    int `toml:"model_quota_floor_percent"`
	QuotaWarnPercent     int `toml:"model_quota_warn_percent"`
}

// Role is one entry of roles.toml (CF-02).
type Role struct {
	Model     string   `toml:"model"`
	Effort    string   `toml:"effort"`     // "", low, medium, high, xhigh, max
	MaxTokens int      `toml:"max_tokens"` // 0 = bounded by max_turns and timeout only (PP-5)
	Timeout   Duration `toml:"timeout"`
	// Advisor: whether a session may consult the advisor model (D-044 ②).
	Advisor      bool   `toml:"advisor"`
	AdvisorModel string `toml:"advisor_model"`
	// RetryUpgradeModel is the model of the Run after the retry cap − 1
	// (D-044 ①); only meaningful for coder.
	RetryUpgradeModel string `toml:"retry_upgrade_model"`
}

// Project is projects/<name>.toml. Name comes from the file name.
type Project struct {
	Name          string     `toml:"-"`
	RepoPath      string     `toml:"repo_path"`   // CF-01
	GitHubRepo    string     `toml:"github_repo"` // owner/name
	DefaultBranch string     `toml:"default_branch"`
	Services      []Service  `toml:"service"`
	Databases     []Database `toml:"database"`
	PIIMask       []string   `toml:"pii_mask"`  // regular expressions
	L0            [][]string `toml:"l0"`        // argv lists
	L0Budget      L0Budget   `toml:"l0_budget"` // CF-12
	Access        Access     `toml:"access"`    // CF-06
	Prod          Prod       `toml:"prod"`      // CF-07
}

// Service declares a service of the project and where its logs are.
type Service struct {
	Name    string `toml:"name"`
	LogPath string `toml:"log_path"`
}

// Database declares a database of the project (read-only tooling only).
type Database struct {
	Name string `toml:"name"`
	Kind string `toml:"kind"` // sqlite, postgres, mysql
	Path string `toml:"path"` // file path or DSN-free locator; no credentials
}

// L0Budget is CF-12 (default 120s / 2 / 2GB, D-049 ④).
type L0Budget struct {
	Timeout    Duration `toml:"timeout"`
	GoMaxProcs int      `toml:"gomaxprocs"`
	MemoryMB   int      `toml:"memory_mb"`
}

// Access is CF-06: resource grants. Tier definitions are code (access.yaml).
type Access struct {
	R1Declared []string `toml:"r1_declared"`
	R2Approved []string `toml:"r2_approved"`
}

// Prod is CF-07: read-only production tools.
type Prod struct {
	SQLReadOnly bool `toml:"sql_ro"`
	RowsMax     int  `toml:"rows_max"`
}
