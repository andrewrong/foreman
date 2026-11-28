package schema

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Keys whose default is "owner" in design/config.yaml have no value in the
// spec (D-074). Their zero value means "not set"; Load refuses to return a
// configuration while one of them (other than a phase v2 key) is missing.

// missingOwnerKeys lists "file: key" for each owner-required key of one file
// that is still at its zero value in e. name is the project name for a
// project file.
func missingOwnerKeys(e *Effective, kind Kind, rel, name string) []string {
	var missing []string
	check := func(v reflect.Value, key, label string) {
		f, ok := fieldByKey(v, key)
		if !ok {
			panic(fmt.Sprintf("schema: owner key %q of %s has no field", key, rel))
		}
		if (f.Kind() == reflect.Slice && f.Len() == 0) || (f.Kind() != reflect.Slice && f.IsZero()) {
			missing = append(missing, rel+": "+label+": required")
		}
	}
	for _, r := range keyTable {
		if r.Default != defOwner || r.Phase == "v2" {
			continue
		}
		switch {
		case kind == KindRuntime && r.File == RuntimeFile:
			rt := e.Runtime
			check(reflect.ValueOf(&rt).Elem(), r.Key, r.Key)
		case kind == KindProject && r.File == projectFileKey:
			p := e.Projects[name]
			check(reflect.ValueOf(&p).Elem(), r.Key, r.Key)
		case kind == KindRoles && r.File == RolesFile:
			parts := strings.SplitN(r.Key, ".", 3)
			for _, n := range RoleNames {
				if parts[1] == "*" || parts[1] == n {
					role := e.Roles[n]
					check(reflect.ValueOf(&role).Elem(), parts[2], "role."+n+"."+parts[2])
				}
			}
		}
	}
	return missing
}

// ownerKeysError joins missing keys into one error, or returns nil.
func ownerKeysError(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return errors.New("missing keys that have no default and must be set by the owner:\n  " + strings.Join(missing, "\n  "))
}
