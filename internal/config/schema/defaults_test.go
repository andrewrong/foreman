package schema

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// leafKeys lists the toml leaf keys of struct type t. A slice of tables
// (service, database) is one key; Duration is a leaf, not a table.
func leafKeys(t reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		name := tomlName(t.Field(i))
		if name == "" {
			continue
		}
		ft := t.Field(i).Type
		if ft.Kind() == reflect.Struct {
			leafKeys(ft, prefix+name+".", out)
			continue
		}
		out[prefix+name] = true
	}
}

func sortedDiff(a, b map[string]bool) []string {
	var d []string
	for k := range a {
		if !b[k] {
			d = append(d, k)
		}
	}
	sort.Strings(d)
	return d
}

func TestConfigKeysMatchSchema(t *testing.T) {
	schemaKeys := map[string]map[string]bool{RuntimeFile: {}, RolesFile: {}, projectFileKey: {}}
	leafKeys(reflect.TypeOf(Runtime{}), "", schemaKeys[RuntimeFile])
	leafKeys(reflect.TypeOf(Role{}), "", schemaKeys[RolesFile])
	leafKeys(reflect.TypeOf(Project{}), "", schemaKeys[projectFileKey])

	// Roles: role.* and role.<name> rows both name a key of Role; the table
	// need not list every role x key pair (unlisted pairs are unset).
	tableKeys := map[string]map[string]bool{RuntimeFile: {}, RolesFile: {}, projectFileKey: {}}
	known := map[string]bool{}
	for _, n := range RoleNames() {
		known[n] = true
	}
	for _, r := range keyTable {
		set, ok := tableKeys[r.File]
		if !ok {
			t.Fatalf("row %q: unknown file %q", r.Key, r.File)
		}
		key := r.Key
		if r.File == RolesFile {
			parts := strings.SplitN(r.Key, ".", 3)
			if len(parts) != 3 || parts[0] != "role" || (parts[1] != "*" && !known[parts[1]]) {
				t.Fatalf("roles.toml row %q is not role.*.<key> or role.<known role>.<key>", r.Key)
			}
			key = parts[2]
		}
		set[key] = true
	}
	for file, sk := range schemaKeys {
		if d := sortedDiff(sk, tableKeys[file]); len(d) > 0 {
			t.Errorf("%s: schema keys missing from the keys table: %v", file, d)
		}
		if d := sortedDiff(tableKeys[file], sk); len(d) > 0 {
			t.Errorf("%s: keys table rows with no schema field: %v", file, d)
		}
	}
	// Every row's default parses into its field (applyDefault panics otherwise).
	Defaults()
	DefaultProject("x")
}

func TestConfigOwnerKeysRequired(t *testing.T) {
	// No files at all: every non-v2 owner key is listed, in one error.
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Load without owner keys must fail")
	}
	want := []string{"runtime.toml: secrets_dir", "runtime.toml: parallel.workers"}
	for _, n := range RoleNames() {
		want = append(want, "roles.toml: role."+n+".timeout")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not list %q:\n%v", w, err)
		}
	}
	if strings.Contains(err.Error(), "improve.") {
		t.Errorf("phase v2 keys must not be required:\n%v", err)
	}
	if strings.Contains(err.Error(), "projects/") {
		t.Errorf("project keys are required only for existing projects:\n%v", err)
	}

	// A project that exists must set its owner keys.
	home := t.TempDir()
	writeOwner(t, home, nil)
	write(t, home, "projects/demo.toml", "pii_mask = []\n")
	_, err = Load(home)
	if err == nil {
		t.Fatal("project without owner keys must fail")
	}
	for _, k := range []string{"repo_path", "github_repo", "default_branch", "l0"} {
		if !strings.Contains(err.Error(), "projects/demo.toml: "+k) {
			t.Errorf("error does not list project key %q:\n%v", k, err)
		}
	}

	// Setting them passes.
	write(t, home, "projects/demo.toml", "repo_path = \"/srv/example/demo\"\ngithub_repo = \"example/demo\"\ndefault_branch = \"main\"\nl0 = [[\"go\", \"test\", \"./...\"]]\n")
	e, err := Load(home)
	if err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
	if e.Runtime.Parallel.Workers != 2 || e.Runtime.Parallel.StepsPerRequirement != 1 {
		t.Errorf("parallel = %+v", e.Runtime.Parallel)
	}
}
