package schema

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

//go:generate go run ./configgen

// RoleNames lists the roles a roles.toml may configure (skills.yaml).
func RoleNames() []string {
	return []string{"intake", "planner", "coder", "reviewer", "rescuer", "diagnoser", "improver"}
}

// Defaults are not written in code (D-074). keyTable (defaults_gen.go) is
// generated from the keys table of design/config.yaml; the functions below
// copy each row's default into the matching struct field, found by walking
// the toml tags with reflection. A default of "unset" or "owner" leaves the
// zero value: "unset" means the feature is off or unbounded, "owner" means
// the owner must supply the value (see missingOwnerKeys).
const (
	defUnset = "unset"
	defOwner = "owner"
)

// DefaultRoles returns the per-role defaults. A role.*.<key> row applies to
// every role in RoleNames; a role.<name>.<key> row overrides it for one role.
func DefaultRoles() map[string]Role {
	roles := make(map[string]Role, len(RoleNames()))
	for _, n := range RoleNames() {
		roles[n] = Role{}
	}
	for _, wildcard := range []bool{true, false} {
		for _, r := range keyTable {
			parts := strings.SplitN(r.Key, ".", 3)
			if r.File != RolesFile || len(parts) != 3 || parts[0] != "role" || (parts[1] == "*") != wildcard {
				continue
			}
			for _, n := range RoleNames() {
				if parts[1] != "*" && parts[1] != n {
					continue
				}
				role := roles[n]
				applyDefault(reflect.ValueOf(&role).Elem(), parts[2], r)
				roles[n] = role
			}
		}
	}
	return roles
}

// DefaultRuntime returns runtime.toml defaults.
func DefaultRuntime() Runtime {
	var rt Runtime
	applyFileDefaults(reflect.ValueOf(&rt).Elem(), RuntimeFile)
	return rt
}

// DefaultProject returns projects/<name>.toml defaults.
func DefaultProject(name string) Project {
	p := Project{Name: name}
	applyFileDefaults(reflect.ValueOf(&p).Elem(), projectFileKey)
	return p
}

// projectFileKey is the file column of project rows in the keys table.
const projectFileKey = "projects/<p>.toml"

// Defaults returns the effective configuration with no files present.
func Defaults() *Effective {
	return &Effective{Runtime: DefaultRuntime(), Roles: DefaultRoles(), Projects: map[string]Project{}}
}

func applyFileDefaults(v reflect.Value, file string) {
	for _, r := range keyTable {
		if r.File == file {
			applyDefault(v, r.Key, r)
		}
	}
}

// applyDefault sets the field of struct v named by the dotted key. A row that
// names no field, or whose default does not parse, is a spec/schema mismatch
// and panics: TestConfigKeysMatchSchema runs every row through here.
func applyDefault(v reflect.Value, key string, r keyRow) {
	if r.Default == defUnset || r.Default == defOwner {
		return
	}
	f, ok := fieldByKey(v, key)
	if !ok {
		panic(fmt.Sprintf("schema: key %q of %s has no field", r.Key, r.File))
	}
	if err := setFromString(f, r.Default); err != nil {
		panic(fmt.Sprintf("schema: default of %q in %s: %v", r.Key, r.File, err))
	}
}

// tomlName returns the toml key of a struct field, or "" when it has none.
func tomlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if name == "-" {
		return ""
	}
	return name
}

// fieldByKey finds the field of struct v addressed by a dotted toml key.
func fieldByKey(v reflect.Value, key string) (reflect.Value, bool) {
	head, rest, more := strings.Cut(key, ".")
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if tomlName(t.Field(i)) != head {
			continue
		}
		f := v.Field(i)
		if !more {
			return f, true
		}
		if f.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		return fieldByKey(f, rest)
	}
	return reflect.Value{}, false
}

func durationType() reflect.Type {
	return reflect.TypeOf(Duration(0))
}

func setFromString(f reflect.Value, s string) error {
	switch {
	case f.Type() == durationType():
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		f.SetInt(int64(d))
	case f.Kind() == reflect.String:
		f.SetString(s)
	case f.Kind() == reflect.Int:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		f.SetInt(n)
	case f.Kind() == reflect.Float64:
		x, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		f.SetFloat(x)
	case f.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		f.SetBool(b)
	default:
		return fmt.Errorf("cannot set a %s field from a string default", f.Kind())
	}
	return nil
}
