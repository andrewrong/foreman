package repocheck

import (
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const repocheckPkg = "internal/repocheck"

// crossEdge is an allowed same-layer, cross-module import edge (a reading of
// the prose MD-* rules in modules.yaml allowed_deps). Empty fromPkgs/toPkgs
// mean "any package of the module".
type crossEdge struct {
	id       string
	from, to string // module names
	fromPkgs []string
	toPkgs   []string
}

// allowedCrossEdges: same-layer cross-module edges. Everything else on the same
// layer is forbidden (modules.yaml header). Cross-layer downward imports are
// always allowed, except where noted in the restriction functions below.
var allowedCrossEdges = []crossEdge{
	// MD-6: layer 2 modules may not import each other, except forge and harness
	// may import resource/gate and resource/meter.
	{"MD-6", "forge", "resource", nil, []string{"internal/resource/gate", "internal/resource/meter"}},
	{"MD-6", "harness", "resource", nil, []string{"internal/resource/gate", "internal/resource/meter"}},
	// MD-7: layer 3 cross-module edges: pipeline -> stage, role (called from
	// sched and recover); role -> stage only internal/stage/context.
	// stage, role, prod never import pipeline (no edge listed).
	{"MD-7", "pipeline", "stage", []string{"internal/pipeline/sched", "internal/pipeline/recover"}, nil},
	{"MD-7", "pipeline", "role", []string{"internal/pipeline/sched", "internal/pipeline/recover"}, nil},
	{"MD-7", "role", "stage", nil, []string{"internal/stage/context"}},
	// MD-9: layer 1 cross-module edge: config -> store (config/revision writes global.db).
	{"MD-9", "config", "store", []string{"internal/config/revision"}, nil},
}

// MD-1: internal/web may import only internal/api/client and layer 0 packages.
const (
	md1Module = "web"
	md1Allow  = "internal/api/client"
)

// MD-8: internal/pipeline/state imports no other package of the pipeline module.
const md8Pkg = "internal/pipeline/state"

// MD-2: go-github / githubv4 may be imported only by internal/forge/*.
func isGithubClientImport(imp string) bool {
	return strings.Contains(imp, "/go-github") || strings.Contains(imp, "githubv4")
}

func inPrefix(pkg, prefix string) bool { return pkg == prefix || strings.HasPrefix(pkg, prefix+"/") }

func matches(pkg string, list []string) bool {
	if len(list) == 0 {
		return true
	}
	for _, p := range list {
		if pkg == p {
			return true
		}
	}
	return false
}

// MD-3, MD-4, MD-5 concern types, writers and file access and cannot be
// decided from imports; they are not checked here.

// findCycle returns one cycle in g as a node path, or nil.
func findCycle(g map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		next := append([]string(nil), g[n]...)
		sort.Strings(next)
		for _, m := range next {
			switch color[m] {
			case grey:
				i := len(stack) - 1
				for stack[i] != m {
					i--
				}
				cycle = append(append([]string(nil), stack[i:]...), m)
				return true
			case white:
				if visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if color[k] == white && visit(k) {
			return cycle
		}
	}
	return nil
}

// depViolations implements C-04 / C-10 for the module rooted at root.
func depViolations(root string) ([]string, error) {
	mods, err := loadModules(root)
	if err != nil {
		return nil, err
	}
	modPath, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	pkgMod := map[string]*moduleDef{}
	for i := range mods.Modules {
		for _, p := range mods.Modules[i].Packages {
			pkgMod[p] = &mods.Modules[i]
		}
	}
	// imports[pkg] = set of import paths used by any file (tests included).
	// External test packages (package foo_test) form their own node in the
	// cycle graph: they may import foo, and foo_test -> bar -> foo is not a
	// cycle. Their imports still count for layer and edge rules under foo.
	imports := map[string]map[string]bool{}
	extImports := map[string]map[string]bool{}
	err = eachGoFile(root, func(rel string) bool {
		return inPrefix(rel, "internal") || inPrefix(rel, "cmd")
	}, func(rel string, _ *token.FileSet, f *ast.File) {
		dir := path.Dir(rel)
		set := imports
		if isTestFile(rel) && strings.HasSuffix(f.Name.Name, "_test") {
			set = extImports
		}
		if imports[dir] == nil {
			imports[dir] = map[string]bool{} // keeps the package known even if only external tests exist
		}
		if set[dir] == nil {
			set[dir] = map[string]bool{}
		}
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				set[dir][p] = true
			}
		}
	})
	if err != nil {
		return nil, err
	}

	var out []string
	pkgGraph := map[string][]string{}
	modGraph := map[string][]string{}
	pkgs := make([]string, 0, len(imports))
	for p := range imports {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, from := range pkgs {
		fromMod := pkgMod[from]
		exempt := from == repocheckPkg
		if fromMod == nil && !exempt {
			out = append(out, fmt.Sprintf("%s: package not listed in %s", from, modulesPath))
		}
		type impEdge struct {
			path string
			ext  bool
		}
		var imps []impEdge
		for i := range imports[from] {
			imps = append(imps, impEdge{i, false})
		}
		for i := range extImports[from] {
			imps = append(imps, impEdge{i, true})
		}
		sort.Slice(imps, func(a, b int) bool {
			if imps[a].path != imps[b].path {
				return imps[a].path < imps[b].path
			}
			return !imps[a].ext && imps[b].ext
		})
		for _, ie := range imps {
			imp := ie.path
			node := from
			if ie.ext {
				node = from + "_test"
			}
			if isGithubClientImport(imp) && !inPrefix(from, "internal/forge") {
				out = append(out, fmt.Sprintf("%s imports %s: only internal/forge/* may (MD-2, C-10)", from, imp))
			}
			rel, ok := strings.CutPrefix(imp, modPath+"/")
			if !ok || !(inPrefix(rel, "internal") || inPrefix(rel, "cmd")) {
				continue
			}
			if rel == repocheckPkg {
				out = append(out, fmt.Sprintf("%s imports %s: repocheck must not be imported", from, rel))
				continue
			}
			if exempt {
				continue
			}
			toMod := pkgMod[rel]
			if toMod == nil {
				out = append(out, fmt.Sprintf("%s imports %s: package not listed in %s", from, rel, modulesPath))
				continue
			}
			pkgGraph[node] = append(pkgGraph[node], rel)
			if fromMod == nil {
				continue
			}
			if fromMod != toMod {
				modGraph[fromMod.Module] = append(modGraph[fromMod.Module], toMod.Module)
			}
			if msg := edgeViolation(from, rel, fromMod, toMod); msg != "" {
				out = append(out, msg)
			}
		}
	}
	if c := findCycle(pkgGraph); c != nil {
		out = append(out, "package import cycle: "+strings.Join(c, " -> "))
	}
	if c := findCycle(modGraph); c != nil {
		out = append(out, "module import cycle: "+strings.Join(c, " -> "))
	}
	return out, nil
}

// edgeViolation checks layer direction and the MD-* edge rules for one import.
func edgeViolation(from, to string, fm, tm *moduleDef) string {
	edge := fmt.Sprintf("%s (%s, layer %d) imports %s (%s, layer %d)", from, fm.Module, fm.Layer, to, tm.Module, tm.Layer)
	if fm == tm {
		if from == md8Pkg && to != from && fm.Module == "pipeline" {
			return edge + ": pipeline/state must not import other pipeline packages (MD-8)"
		}
		return ""
	}
	if fm.Layer < tm.Layer {
		return edge + ": lower layer imports higher layer (C-04)"
	}
	if fm.Layer == tm.Layer {
		allowed := false
		for _, e := range allowedCrossEdges {
			if e.from == fm.Module && e.to == tm.Module && matches(from, e.fromPkgs) && matches(to, e.toPkgs) {
				allowed = true
			}
		}
		if !allowed {
			return edge + ": same-layer cross-module edge not allowed by MD rules"
		}
	}
	if fm.Module == md1Module && to != md1Allow && tm.Layer != 0 {
		return edge + ": web may import only internal/api/client and layer 0 (MD-1)"
	}
	return ""
}

func TestModuleDeps(t *testing.T) {
	v, err := depViolations(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		t.Error(s)
	}
}

const fixtureModules = `
modules:
  - { module: clock,    layer: 0, packages: [internal/clock] }
  - { module: spec,     layer: 0, packages: [internal/spec/model] }
  - { module: store,    layer: 1, packages: [internal/store/sqlite] }
  - { module: config,   layer: 1, packages: [internal/config/revision, internal/config/schema] }
  - { module: resource, layer: 2, packages: [internal/resource/gate, internal/resource/ledger] }
  - { module: forge,    layer: 2, packages: [internal/forge/client] }
  - { module: harness,  layer: 2, packages: [internal/harness/api] }
  - { module: pipeline, layer: 3, packages: [internal/pipeline/state, internal/pipeline/sched] }
  - { module: stage,    layer: 3, packages: [internal/stage/code] }
  - { module: api,      layer: 4, packages: [internal/api/client, internal/api/server] }
  - { module: web,      layer: 5, packages: [internal/web] }
  - { module: cli,      layer: 5, packages: [cmd/foreman] }
package_limits: { non_test_lines: 3000, test_lines: 4500, file_lines: 600 }
`

const fixtureMod = "example.com/x"

// depFixture builds a repo where each package imports the listed packages
// (module-relative paths) and returns the violations found.
func depFixture(t *testing.T, imports map[string][]string, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                   "module " + fixtureMod + "\n\ngo 1.27\n",
		"spec/design/modules.yaml": fixtureModules,
	}
	for pkg, imps := range imports {
		var b strings.Builder
		b.WriteString("package p\n")
		for _, i := range imps {
			if strings.Contains(i, ".") && !strings.HasPrefix(i, fixtureMod) {
				fmt.Fprintf(&b, "import _ %q\n", i)
			} else {
				fmt.Fprintf(&b, "import _ %q\n", fixtureMod+"/"+i)
			}
		}
		files[pkg+"/p.go"] = b.String()
	}
	for k, v := range extra {
		files[k] = v
	}
	writeFiles(t, root, files)
	v, err := depViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(v, "\n")
}

func TestModuleDepsDetectsViolations(t *testing.T) {
	cases := []struct {
		name    string
		imports map[string][]string
		extra   map[string]string
		want    string // substring; empty means no violation expected
	}{
		{"clean", map[string][]string{
			"cmd/foreman":              {"internal/api/server", "internal/pipeline/sched", "internal/clock"},
			"internal/pipeline/sched":  {"internal/stage/code", "internal/resource/gate", "internal/pipeline/state"},
			"internal/forge/client":    {"internal/resource/gate"},
			"internal/config/revision": {"internal/store/sqlite", "internal/config/schema"},
			"internal/web":             {"internal/api/client", "internal/clock"},
			"internal/stage/code":      {"internal/spec/model"},
		}, nil, ""},
		{"unlisted package", map[string][]string{"internal/mystery": nil}, nil, "internal/mystery: package not listed"},
		{"import of unlisted", map[string][]string{"internal/clock": nil, "cmd/foreman": {"internal/mystery"}}, nil, "imports internal/mystery: package not listed"},
		{"upward import", map[string][]string{"internal/clock": {"internal/store/sqlite"}}, nil, "lower layer imports higher layer"},
		{"same-layer layer 0", map[string][]string{"internal/clock": {"internal/spec/model"}}, nil, "same-layer cross-module edge"},
		{"MD-6 forge to harness", map[string][]string{"internal/forge/client": {"internal/harness/api"}}, nil, "same-layer cross-module edge"},
		{"MD-6 forge to ledger", map[string][]string{"internal/forge/client": {"internal/resource/ledger"}}, nil, "same-layer cross-module edge"},
		{"MD-7 stage to pipeline", map[string][]string{"internal/stage/code": {"internal/pipeline/state"}}, nil, "same-layer cross-module edge"},
		{"MD-7 state to stage", map[string][]string{"internal/pipeline/state": {"internal/stage/code"}}, nil, "same-layer cross-module edge"},
		{"MD-8 state to sched", map[string][]string{"internal/pipeline/state": {"internal/pipeline/sched"}}, nil, "MD-8"},
		{"MD-9 schema to store", map[string][]string{"internal/config/schema": {"internal/store/sqlite"}}, nil, "same-layer cross-module edge"},
		{"MD-1 web to server", map[string][]string{"internal/web": {"internal/api/server"}}, nil, "MD-1"},
		{"MD-1 web to forge", map[string][]string{"internal/web": {"internal/forge/client"}}, nil, "MD-1"},
		{"MD-2 go-github", map[string][]string{"internal/harness/api": {"github.com/google/go-github/v70/github"}}, nil, "MD-2"},
		{"repocheck imported", map[string][]string{"cmd/foreman": {"internal/repocheck"}, "internal/repocheck": nil}, nil, "must not be imported"},
		{"package cycle in module", map[string][]string{
			"internal/pipeline/sched": {"internal/pipeline/state"},
			"internal/pipeline/state": {"internal/pipeline/sched"},
		}, nil, "package import cycle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := depFixture(t, c.imports, c.extra)
			if c.want == "" && got != "" {
				t.Errorf("unexpected violations:\n%s", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("want violation containing %q, got:\n%s", c.want, got)
			}
		})
	}
	t.Run("repocheck exempt as importer", func(t *testing.T) {
		got := depFixture(t, map[string][]string{"internal/repocheck": {"internal/store/sqlite", "internal/web"}}, nil)
		if got != "" {
			t.Errorf("unexpected violations:\n%s", got)
		}
	})
}

func TestModuleDepsExternalTestPackage(t *testing.T) {
	ext := func(pkg, imp string) string {
		return "package p_test\nimport _ \"" + fixtureMod + "/" + imp + "\"\n"
	}
	// package foo_test in dir foo importing foo is not a cycle.
	got := depFixture(t, map[string][]string{"internal/pipeline/state": nil}, map[string]string{
		"internal/pipeline/state/x_test.go": ext("state", "internal/pipeline/state"),
	})
	if got != "" {
		t.Errorf("external test package importing its own package reported: %s", got)
	}
	// foo_test -> bar -> foo is not a cycle either.
	got = depFixture(t, map[string][]string{
		"internal/pipeline/state": nil,
		"internal/pipeline/sched": {"internal/pipeline/state"},
	}, map[string]string{
		"internal/pipeline/state/x_test.go": ext("state", "internal/pipeline/sched"),
	})
	if strings.Contains(got, "cycle") {
		t.Errorf("foo_test -> bar -> foo reported as a cycle: %s", got)
	}
	// A real cycle is still found.
	got = depFixture(t, map[string][]string{
		"internal/pipeline/state": {"internal/pipeline/sched"},
		"internal/pipeline/sched": {"internal/pipeline/state"},
	}, map[string]string{
		"internal/pipeline/state/x_test.go": ext("state", "internal/pipeline/state"),
	})
	if !strings.Contains(got, "package import cycle") {
		t.Errorf("real cycle not reported: %s", got)
	}
}

func TestFindCycle(t *testing.T) {
	if c := findCycle(map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}); strings.Join(c, ">") != "a>b>c>a" {
		t.Errorf("cycle = %v", c)
	}
	if c := findCycle(map[string][]string{"a": {"b", "c"}, "b": {"c"}}); c != nil {
		t.Errorf("acyclic graph reported cycle %v", c)
	}
}
