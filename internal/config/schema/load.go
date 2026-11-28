package schema

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Dir is the configuration directory under FOREMAN_HOME.
const Dir = "config"

// File names under Dir.
const (
	RuntimeFile  = "runtime.toml"
	RolesFile    = "roles.toml"
	ProjectsDir  = "projects"
	projectExt   = ".toml"
	maxConfigLen = 1 << 20
)

// Kind classifies a config file by its path relative to Dir.
type Kind int

const (
	KindUnknown Kind = iota
	KindRuntime
	KindRoles
	KindProject
)

// Classify maps a slash-separated path relative to the config directory to
// its kind and, for projects, the project name.
func Classify(rel string) (Kind, string) {
	switch rel {
	case RuntimeFile:
		return KindRuntime, ""
	case RolesFile:
		return KindRoles, ""
	}
	if dir, base := filepath.Split(rel); dir == ProjectsDir+"/" && strings.HasSuffix(base, projectExt) {
		name := strings.TrimSuffix(base, projectExt)
		if validProjectName(name) {
			return KindProject, name
		}
	}
	return KindUnknown, ""
}

// ValidateFile validates the content of one config file (path relative to
// Dir, slash separated) against the schema, without touching disk.
func ValidateFile(rel string, data []byte) error {
	e := Defaults()
	err := applyFile(e, rel, data)
	kind, name := Classify(rel)
	return errors.Join(err, ownerKeysError(missingOwnerKeys(e, kind, rel, name)))
}

func applyFile(e *Effective, rel string, data []byte) error {
	kind, name := Classify(rel)
	var errs []error
	switch kind {
	case KindRuntime:
		errs = decodeStrict(rel, data, &e.Runtime)
		errs = append(errs, validateRuntime(rel, &e.Runtime)...)
	case KindRoles:
		errs = loadRoles(rel, data, e)
	case KindProject:
		p := DefaultProject(name)
		errs = decodeStrict(rel, data, &p)
		errs = append(errs, validateProject(rel, &p)...)
		e.Projects[name] = p
	default:
		return fmt.Errorf("%s: not a configuration file (expected %s, %s or %s/<name>%s)",
			rel, RuntimeFile, RolesFile, ProjectsDir, projectExt)
	}
	return errors.Join(errs...)
}

func decodeStrict(rel string, data []byte, v any) []error {
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return []error{fmt.Errorf("%s: %w", rel, err)}
	}
	return undecoded(rel, md)
}

func undecoded(rel string, md toml.MetaData) []error {
	var errs []error
	for _, k := range md.Undecoded() {
		errs = append(errs, fmt.Errorf("%s: unknown key %q", rel, k.String()))
	}
	return errs
}

func loadRoles(rel string, data []byte, e *Effective) []error {
	var raw struct {
		Role map[string]toml.Primitive `toml:"role"`
	}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return []error{fmt.Errorf("%s: %w", rel, err)}
	}
	var errs []error
	names := make([]string, 0, len(raw.Role))
	for n := range raw.Role {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		base, ok := e.Roles[n]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: unknown role %q (known: %s)", rel, n, strings.Join(RoleNames(), ", ")))
			continue
		}
		if err := md.PrimitiveDecode(raw.Role[n], &base); err != nil {
			errs = append(errs, fmt.Errorf("%s: role.%s: %w", rel, n, err))
			continue
		}
		e.Roles[n] = base
	}
	errs = append(errs, undecoded(rel, md)...)
	return append(errs, validateRoles(rel, e.Roles)...)
}

// Load reads FOREMAN_HOME/config and returns the effective configuration.
// Missing files mean defaults. Every problem in every file is reported,
// including, in one error, every owner-required key that is not set (D-074).
func Load(home string) (*Effective, error) {
	root := filepath.Join(home, Dir)
	e := Defaults()
	var errs []error
	read := func(rel string) {
		b, err := readLimited(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", rel, err))
		default:
			if err := applyFile(e, rel, b); err != nil {
				errs = append(errs, err)
			}
		}
	}
	read(RuntimeFile)
	read(RolesFile)
	ents, err := os.ReadDir(filepath.Join(root, ProjectsDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, ent := range ents {
		rel := ProjectsDir + "/" + ent.Name()
		if k, _ := Classify(rel); k != KindProject {
			errs = append(errs, fmt.Errorf("%s: not a project file (want <name>%s with a lowercase name)", rel, projectExt))
			continue
		}
		read(rel)
	}
	missing := missingOwnerKeys(e, KindRuntime, RuntimeFile, "")
	missing = append(missing, missingOwnerKeys(e, KindRoles, RolesFile, "")...)
	names := make([]string, 0, len(e.Projects))
	for n := range e.Projects {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		missing = append(missing, missingOwnerKeys(e, KindProject, ProjectsDir+"/"+n+projectExt, n)...)
	}
	if err := ownerKeysError(missing); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return e, nil
}

func readLimited(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxConfigLen {
		return nil, fmt.Errorf("file larger than %d bytes", maxConfigLen)
	}
	return os.ReadFile(path)
}
