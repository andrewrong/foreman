package repocheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// depEntry is one row of the modules.yaml `deps:` registry (C-14, D-076).
type depEntry struct {
	Module   string   `yaml:"module"`
	UsedBy   []string `yaml:"used_by"`
	Required bool     `yaml:"required"`
	Status   string   `yaml:"status"`
	Kind     string   `yaml:"kind"` // "tool": pinned by a go.mod tool directive, never imported
}

type depsFile struct {
	Deps []depEntry `yaml:"deps"`
}

// directRequires returns the module paths go.mod requires without
// "// indirect", in both the block and the single-line form.
func directRequires(goMod []byte) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(goMod), "\n") {
		line = strings.TrimSpace(line)
		indirect := strings.Contains(line, "// indirect")
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "" && !indirect:
			out = append(out, strings.Fields(line)[0])
		case strings.HasPrefix(line, "require ") && !indirect && !strings.HasSuffix(line, "("):
			if f := strings.Fields(line); len(f) >= 2 {
				out = append(out, f[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

var majorSuffix = regexp.MustCompile(`^v[0-9]+$`)

// registeredAs reports whether go.mod path mod is the registered module reg:
// equal, or reg plus a major-version suffix (go-github registered as
// github.com/google/go-github matches .../go-github/v93).
func registeredAs(mod, reg string) bool {
	if mod == reg {
		return true
	}
	rest, ok := strings.CutPrefix(mod, reg+"/")
	return ok && majorSuffix.MatchString(rest)
}

func importsModule(imp, mod string) bool { return imp == mod || strings.HasPrefix(imp, mod+"/") }

// moduleImporters returns importers[import path] = set of module names whose
// non-test files import it.
func moduleImporters(root string, mods *modulesFile) (map[string]map[string]bool, error) {
	pkgMod := map[string]string{} // package dir -> module name
	for _, m := range mods.Modules {
		for _, p := range m.Packages {
			pkgMod[p] = m.Module
		}
	}
	importers := map[string]map[string]bool{}
	err := eachGoFile(root, func(rel string) bool { return !isTestFile(rel) }, func(rel string, _ *token.FileSet, f *ast.File) {
		mod := pkgMod[path.Dir(rel)]
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if importers[p] == nil {
				importers[p] = map[string]bool{}
			}
			importers[p][mod] = true
		}
	})
	return importers, err
}

// toolPackages returns the package paths of the go.mod tool directives, in
// both the block and the single-line form.
func toolPackages(goMod []byte) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(goMod), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "tool (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			out = append(out, strings.Fields(line)[0])
		case strings.HasPrefix(line, "tool ") && !strings.HasSuffix(line, "("):
			out = append(out, strings.Fields(line)[1])
		}
	}
	sort.Strings(out)
	return out
}

// unregisteredTools reports each go.mod tool directive with no kind: tool entry.
func unregisteredTools(tools []string, deps []depEntry) []string {
	var problems []string
	for _, t := range tools {
		found := false
		for _, d := range deps {
			if d.Kind == "tool" && importsModule(t, d.Module) {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("go.mod has tool %s but it is not registered in %s deps with kind: tool (D-076, D-078)", t, modulesPath))
		}
	}
	return problems
}

// unregisteredDirect reports each direct go.mod requirement with no deps entry.
func unregisteredDirect(direct []string, deps []depEntry) []string {
	var problems []string
	for _, mod := range direct {
		found := false
		for _, d := range deps {
			if registeredAs(mod, d.Module) {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("go.mod requires %s directly but it is not registered in %s deps (D-076)", mod, modulesPath))
		}
	}
	return problems
}

// usedByModules reports whether any of gomod's packages is imported by one of
// the modules in usedBy.
func usedByModules(gomod, usedBy []string, importers map[string]map[string]bool) bool {
	for _, mod := range gomod {
		for imp, by := range importers {
			if !importsModule(imp, mod) {
				continue
			}
			for _, m := range usedBy {
				if by[m] {
					return true
				}
			}
		}
	}
	return false
}

// checkDepEntry validates one registry row; problem and planned are empty when
// the row is fine.
func checkDepEntry(d depEntry, direct, tools []string, importers map[string]map[string]bool) (problem, planned string) {
	var gomod []string
	for _, mod := range direct {
		if registeredAs(mod, d.Module) {
			gomod = append(gomod, mod)
		}
	}
	switch d.Status {
	case "planned":
		return "", fmt.Sprintf("%s (used_by %s): planned, not enforced", d.Module, strings.Join(d.UsedBy, ","))
	case "active":
	default:
		return fmt.Sprintf("deps entry %s: status %q must be active or planned", d.Module, d.Status), ""
	}
	if d.Kind == "tool" {
		for _, t := range tools {
			if importsModule(t, d.Module) {
				return "", ""
			}
		}
		return fmt.Sprintf("deps entry %s is a tool but go.mod has no tool directive for it", d.Module), ""
	}
	if len(gomod) == 0 {
		return fmt.Sprintf("deps entry %s is active but go.mod has no direct requirement for it", d.Module), ""
	}
	if d.Required && !usedByModules(gomod, d.UsedBy, importers) {
		return fmt.Sprintf("deps entry %s is required and active but no package of %s imports it", d.Module, strings.Join(d.UsedBy, ",")), ""
	}
	return "", ""
}

// depsRegistry implements check:deps-registry. problems fail the check;
// planned are registered-but-not-yet-wired entries, reported only.
func depsRegistry(root string) (problems, planned []string, err error) {
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(modulesPath)))
	if err != nil {
		return nil, nil, err
	}
	var reg depsFile
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", modulesPath, err)
	}
	mods, err := loadModules(root)
	if err != nil {
		return nil, nil, err
	}
	importers, err := moduleImporters(root, mods)
	if err != nil {
		return nil, nil, err
	}
	direct := directRequires(goMod)
	tools := toolPackages(goMod)
	problems = append(unregisteredDirect(direct, reg.Deps), unregisteredTools(tools, reg.Deps)...)
	for _, d := range reg.Deps {
		prob, plan := checkDepEntry(d, direct, tools, importers)
		if prob != "" {
			problems = append(problems, prob)
		}
		if plan != "" {
			planned = append(planned, plan)
		}
	}
	return problems, planned, nil
}

func TestDepsRegistry(t *testing.T) {
	problems, planned, err := depsRegistry(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range planned {
		t.Log(s)
	}
	for _, s := range problems {
		t.Error(s)
	}
}

func TestDepsRegistryDetectsViolations(t *testing.T) {
	const modules = `
modules:
  - { module: store,  layer: 1, packages: [internal/store/sqlite] }
  - { module: forge,  layer: 2, packages: [internal/forge/client] }
  - { module: config, layer: 1, packages: [internal/config/schema] }
`
	goMod := func(reqs ...string) string {
		return "module example.com/x\n\ngo 1.27\n\nrequire (\n\t" + strings.Join(reqs, "\n\t") + "\n)\n"
	}
	src := func(imp string) string { return "package p\nimport _ \"" + imp + "\"\n" }
	cases := []struct {
		name  string
		files map[string]string
		deps  string
		want  string // substring of a problem; empty means no problem
		plan  string // substring of a planned report
	}{
		{"registered and imported", map[string]string{
			"go.mod": goMod("github.com/a/lib/v7 v7.1.0"), "internal/forge/client/p.go": src("github.com/a/lib/v7/sub"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: active }`, "", ""},
		{"unregistered direct dependency", map[string]string{
			"go.mod": goMod("github.com/b/other v1.0.0"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: planned }`, "github.com/b/other", "github.com/a/lib"},
		{"indirect dependency is not checked", map[string]string{
			"go.mod": goMod("github.com/b/other v1.0.0 // indirect"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: planned }`, "", ""},
		{"required active but not imported", map[string]string{
			"go.mod": goMod("github.com/a/lib v1.0.0"), "internal/forge/client/p.go": "package p\n",
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: active }`, "no package of forge imports", ""},
		{"imported only by the wrong module", map[string]string{
			"go.mod": goMod("github.com/a/lib v1.0.0"), "internal/store/sqlite/p.go": src("github.com/a/lib"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: active }`, "no package of forge imports", ""},
		{"imported only by a test does not count", map[string]string{
			"go.mod": goMod("github.com/a/lib v1.0.0"), "internal/forge/client/p_test.go": src("github.com/a/lib"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: active }`, "no package of forge imports", ""},
		{"active but absent from go.mod", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: active }
  - { module: github.com/c/x, used_by: [store], required: false, status: active }`, "github.com/a/lib is active but go.mod", ""},
		{"planned is reported not failed", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0"), "internal/store/sqlite/p.go": src("github.com/c/x"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: true, status: planned }
  - { module: github.com/c/x, used_by: [store], required: true, status: active }`, "", "github.com/a/lib"},
		{"unknown status", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0"),
		}, `{ module: github.com/c/x, used_by: [store], required: true, status: maybe }`, `status "maybe"`, ""},
		{"tool registered and pinned", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0") + "\ntool github.com/a/lib/cmd/lint\n",
		}, `{ module: github.com/a/lib, kind: tool, used_by: [store], required: true, status: active }
  - { module: github.com/c/x, used_by: [store], required: false, status: active }`, "", ""},
		{"tool directive without an entry", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0") + "\ntool (\n\tgithub.com/a/lib/cmd/lint\n)\n",
		}, `{ module: github.com/c/x, used_by: [store], required: false, status: active }`, "go.mod has tool github.com/a/lib/cmd/lint", ""},
		{"tool entry without a directive", map[string]string{
			"go.mod": goMod("github.com/c/x v1.0.0"),
		}, `{ module: github.com/a/lib, kind: tool, used_by: [store], required: true, status: active }
  - { module: github.com/c/x, used_by: [store], required: false, status: active }`, "no tool directive", ""},
		{"prefix is not a major suffix", map[string]string{
			"go.mod": goMod("github.com/a/lib-extra v1.0.0"),
		}, `{ module: github.com/a/lib, used_by: [forge], required: false, status: planned }`, "github.com/a/lib-extra", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"spec/design/modules.yaml": modules + "deps:\n  - " + c.deps + "\n"}
			for k, v := range c.files {
				files[k] = v
			}
			writeFiles(t, root, files)
			problems, planned, err := depsRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			got, gotPlan := strings.Join(problems, "\n"), strings.Join(planned, "\n")
			if c.want == "" && got != "" {
				t.Errorf("unexpected problems:\n%s", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("want problem containing %q, got:\n%s", c.want, got)
			}
			if c.plan != "" && !strings.Contains(gotPlan, c.plan) {
				t.Errorf("want planned report containing %q, got:\n%s", c.plan, gotPlan)
			}
		})
	}
}

func TestToolPackages(t *testing.T) {
	got := toolPackages([]byte("module m\n\ntool a/b/cmd\n\ntool (\n\tc/d/cmd // x\n)\n"))
	if strings.Join(got, ",") != "a/b/cmd,c/d/cmd" {
		t.Errorf("toolPackages = %v", got)
	}
}

func TestDirectRequires(t *testing.T) {
	got := directRequires([]byte("module m\n\nrequire a/b v1.0.0\n\nrequire (\n\tc/d v2.0.0\n\te/f v1.0.0 // indirect\n)\n"))
	if strings.Join(got, ",") != "a/b,c/d" {
		t.Errorf("directRequires = %v", got)
	}
}
