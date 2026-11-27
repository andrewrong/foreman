package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRoleModelDefaults(t *testing.T) {
	e, err := Load(t.TempDir()) // no files at all
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"coder": "sonnet", "reviewer": "opus", "intake": "sonnet",
		"planner": "opus", "rescuer": "opus",
	}
	for role, model := range want {
		if got := e.Roles[role].Model; got != model {
			t.Errorf("role %s model = %q, want %q", role, got, model)
		}
	}
	if e.Roles["coder"].Model == e.Roles["reviewer"].Model {
		t.Error("coder and reviewer must default to different models (D-040)")
	}
	c := e.Roles["coder"]
	if !c.Advisor || c.AdvisorModel != "opus" || c.RetryUpgradeModel != "opus" {
		t.Errorf("coder advisor/upgrade defaults: %+v", c)
	}
	if got := e.Models()["coder"]; got != "sonnet" {
		t.Errorf("Models()[coder] = %q", got)
	}

	// An override changes only the named field; everything else keeps defaults.
	home := t.TempDir()
	write(t, home, RolesFile, "[role.reviewer]\nmodel = \"claude-opus-5-5\"\n\n[role.coder]\nadvisor = false\n")
	e, err = Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if e.Roles["reviewer"].Model != "claude-opus-5-5" || e.Roles["reviewer"].Timeout != Duration(30*time.Minute) {
		t.Errorf("reviewer = %+v", e.Roles["reviewer"])
	}
	if e.Roles["coder"].Advisor || e.Roles["coder"].Model != "sonnet" || e.Roles["coder"].AdvisorModel != "opus" {
		t.Errorf("coder = %+v", e.Roles["coder"])
	}
}

func TestConfigDefaultsMatchSpec(t *testing.T) {
	e := Defaults()
	l := e.Runtime.Limits
	if l.CheckFail != 3 || l.ReviewRounds != 2 || l.MergeConflict != 2 || l.BadResult != 2 || l.Infra != 6 || l.FixSteps != 3 {
		t.Errorf("limits = %+v", l)
	}
	p := DefaultProject("x")
	if p.L0Budget.Timeout.D() != 120*time.Second || p.L0Budget.GoMaxProcs != 2 || p.L0Budget.MemoryMB != 2048 {
		t.Errorf("l0 budget = %+v", p.L0Budget)
	}
	if err := validateRuntime("runtime.toml", &e.Runtime); err != nil {
		t.Errorf("default runtime invalid: %v", err)
	}
	if err := validateRoles("roles.toml", e.Roles); err != nil {
		t.Errorf("default roles invalid: %v", err)
	}
}

func TestConfigSchemaRejectsBadInput(t *testing.T) {
	cases := []struct {
		name, rel, body, wantSub string
	}{
		{"unknown runtime key", RuntimeFile, "workerz = 3\n", `unknown key "workerz"`},
		{"unknown nested key", RuntimeFile, "[limits]\ncheck_fial = 3\n", "limits.check_fial"},
		{"wrong type", RuntimeFile, "[parallel]\nworkers = \"many\"\n", "workers"},
		{"out of range", RuntimeFile, "[parallel]\nworkers = 0\n", "parallel.workers: 0 out of range"},
		{"relative secrets dir", RuntimeFile, "secrets_dir = \"secrets\"\n", "secrets_dir: must be an absolute path"},
		{"warn below floor", RuntimeFile, "[resources]\nmodel_quota_floor_percent = 50\nmodel_quota_warn_percent = 20\n", "must not be below"},
		{"toml syntax", RuntimeFile, "[parallel\n", "runtime.toml"},
		{"unknown role", RolesFile, "[role.wizard]\nmodel = \"opus\"\n", `unknown role "wizard"`},
		{"unknown role key", RolesFile, "[role.coder]\ntemperature = 1\n", "role.coder.temperature"},
		{"bad effort", RolesFile, "[role.coder]\neffort = \"turbo\"\n", "role.coder.effort"},
		{"bad model", RolesFile, "[role.coder]\nmodel = \"bad model!\"\n", "role.coder.model"},
		{"bad duration", RolesFile, "[role.coder]\ntimeout = \"soon\"\n", "invalid duration"},
		{"timeout too small", RolesFile, "[role.coder]\ntimeout = \"1s\"\n", "role.coder.timeout"},
		{"advisor without model", RolesFile, "[role.planner]\nadvisor = true\n", "role.planner.advisor_model"},
		{"project needs repo", "projects/p.toml", "default_branch = \"main\"\n", "repo_path: required"},
		{"project relative repo", "projects/p.toml", "repo_path = \"rel\"\n", "repo_path: must be an absolute"},
		{"project bad regex", "projects/p.toml", "repo_path = \"/r\"\npii_mask = [\"(\"]\n", "pii_mask[0]"},
		{"project bad repo slug", "projects/p.toml", "repo_path = \"/r\"\ngithub_repo = \"nope\"\n", "github_repo"},
		{"project unknown key", "projects/p.toml", "repo_path = \"/r\"\ntoken = \"abc\"\n", `unknown key "token"`},
		{"project bad db", "projects/p.toml", "repo_path = \"/r\"\n[[database]]\nname = \"a\"\nkind = \"oracle\"\npath = \"/x\"\n", "database[0].kind"},
		{"project db credentials", "projects/p.toml", "repo_path = \"/r\"\n[[database]]\nname = \"a\"\nkind = \"postgres\"\npath = \"postgres://u:pw@h/db\"\n", "database[0].path"},
		{"project dup service", "projects/p.toml", "repo_path = \"/r\"\n[[service]]\nname = \"a\"\n[[service]]\nname = \"a\"\n", "duplicate service"},
		{"project empty argv", "projects/p.toml", "repo_path = \"/r\"\nl0 = [[]]\n", "l0[0]"},
		{"project l0 budget", "projects/p.toml", "repo_path = \"/r\"\n[l0_budget]\ngomaxprocs = 0\n", "l0_budget.gomaxprocs"},
		{"project bad name", "projects/Bad Name.toml", "repo_path = \"/r\"\n", "not a configuration file"},
		{"stray file", "notes.toml", "", "not a configuration file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateFile(c.rel, []byte(c.body))
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, c.wantSub)
			}
			if !strings.Contains(err.Error(), strings.TrimPrefix(c.rel, "")) && c.name != "project bad name" {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

func TestConfigLoadProject(t *testing.T) {
	home := t.TempDir()
	write(t, home, "projects/demo.toml", `
repo_path = "/work/demo"
github_repo = "someone/demo"
pii_mask = ["\\b\\d{11}\\b"]
l0 = [["go", "vet", "./..."], ["go", "test", "./..."]]

[[service]]
name = "api"
log_path = "/var/log/demo/api.log"

[[database]]
name = "main"
kind = "sqlite"
path = "/data/demo.db"

[l0_budget]
timeout = "90s"

[access]
r1_declared = ["dev-db"]

[prod]
sql_ro = true
rows_max = 200
`)
	e, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	p := e.Projects["demo"]
	if p.Name != "demo" || p.DefaultBranch != "main" || len(p.L0) != 2 || len(p.Services) != 1 {
		t.Fatalf("project = %+v", p)
	}
	if p.L0Budget.Timeout.D() != 90*time.Second || p.L0Budget.GoMaxProcs != 2 || p.L0Budget.MemoryMB != 2048 {
		t.Errorf("budget = %+v (only timeout overridden)", p.L0Budget)
	}
	if !p.Prod.SQLReadOnly || p.Prod.RowsMax != 200 {
		t.Errorf("prod = %+v", p.Prod)
	}
}

func TestConfigLoadReportsAllProblems(t *testing.T) {
	home := t.TempDir()
	write(t, home, RuntimeFile, "bogus = 1\n")
	write(t, home, RolesFile, "[role.coder]\neffort = \"x\"\n")
	write(t, home, "projects/a.toml", "")
	write(t, home, "projects/README.md", "")
	_, err := Load(home)
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range []string{"runtime.toml", "roles.toml", "projects/a.toml", "projects/README.md"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error lacks %s: %v", s, err)
		}
	}
}
