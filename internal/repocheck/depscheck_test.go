package repocheck

import (
	"fmt"
	"sort"
	"strings"
)

// impEdge is one import of a package; ext marks imports of external test
// packages (package foo_test), which form their own node in the cycle graph.
type impEdge struct {
	path string
	ext  bool
}

// depCheck holds the state of one depViolations run.
type depCheck struct {
	modPath  string
	pkgMod   map[string]*moduleDef
	out      []string
	pkgGraph map[string][]string
	modGraph map[string][]string
}

func (c *depCheck) addf(format string, a ...any) { c.out = append(c.out, fmt.Sprintf(format, a...)) }

// depViolations implements C-04 / C-10 and MD-1..MD-12 for the module rooted
// at root, from `go list` output: Imports (non-test files), TestImports and
// XTestImports. External test packages may import foo, and foo_test -> bar ->
// foo is not a cycle; their imports still count for the layer, edge, uses and
// sibling rules under foo. MD-12 counts only non-test imports.
func depViolations(root string) ([]string, error) {
	mods, err := loadModules(root)
	if err != nil {
		return nil, err
	}
	modPath, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	listed, err := goList(root)
	if err != nil {
		return nil, err
	}
	c := &depCheck{modPath: modPath, pkgMod: map[string]*moduleDef{}, pkgGraph: map[string][]string{}, modGraph: map[string][]string{}}
	for i := range mods.Modules {
		for _, p := range mods.Modules[i].Packages {
			c.pkgMod[p] = &mods.Modules[i]
		}
	}
	dirs := make([]string, 0, len(listed))
	for d := range listed {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, from := range dirs {
		c.checkPackage(from, listed[from])
	}
	if cyc := findCycle(c.pkgGraph); cyc != nil {
		c.out = append(c.out, "package import cycle: "+strings.Join(cyc, " -> "))
	}
	if cyc := findCycle(c.modGraph); cyc != nil {
		c.out = append(c.out, "module import cycle: "+strings.Join(cyc, " -> "))
	}
	return c.out, nil
}

func sortedEdges(lp listedPkg) []impEdge {
	seen := map[impEdge]bool{}
	var imps []impEdge
	add := func(list []string, ext bool) {
		for _, i := range list {
			if e := (impEdge{i, ext}); !seen[e] {
				seen[e] = true
				imps = append(imps, e)
			}
		}
	}
	add(lp.Imports, false)
	add(lp.TestImports, false)
	add(lp.XTestImports, true)
	sort.Slice(imps, func(a, b int) bool {
		if imps[a].path != imps[b].path {
			return imps[a].path < imps[b].path
		}
		return !imps[a].ext && imps[b].ext
	})
	return imps
}

func (c *depCheck) checkPackage(from string, lp listedPkg) {
	fromMod := c.pkgMod[from]
	if fromMod == nil && from != repocheckPkg {
		c.addf("%s: package not listed in %s", from, modulesPath)
	}
	for _, ie := range sortedEdges(lp) {
		c.checkImport(from, fromMod, ie)
	}
	if n := len(fanOut(c.modPath, lp.Imports)); n > md12MaxFanOut && fromMod != nil && fromMod.Module != "cli" && fromMod.Module != "repocheck" {
		c.addf("%s imports %d repository packages, more than %d (MD-12)", from, n, md12MaxFanOut)
	}
}

func (c *depCheck) checkImport(from string, fromMod *moduleDef, ie impEdge) {
	imp := ie.path
	node := from
	if ie.ext {
		node = from + "_test"
	}
	if isGithubClientImport(imp) && !inPrefix(from, "internal/forge") {
		c.addf("%s imports %s: only internal/forge/* may (MD-2, C-10)", from, imp)
	}
	rel, ok := strings.CutPrefix(imp, c.modPath+"/")
	if !ok || !(inPrefix(rel, "internal") || inPrefix(rel, "cmd")) {
		return
	}
	if rel == repocheckPkg {
		c.addf("%s imports %s: repocheck must not be imported", from, rel)
		return
	}
	if from == repocheckPkg {
		return
	}
	toMod := c.pkgMod[rel]
	if toMod == nil {
		c.addf("%s imports %s: package not listed in %s", from, rel, modulesPath)
		return
	}
	c.pkgGraph[node] = append(c.pkgGraph[node], rel)
	if fromMod == nil {
		return
	}
	if fromMod != toMod {
		c.modGraph[fromMod.Module] = append(c.modGraph[fromMod.Module], toMod.Module)
	}
	if msg := edgeViolation(from, rel, fromMod, toMod); msg != "" {
		c.out = append(c.out, msg)
	}
	if msg := usesViolation(from, rel, fromMod, toMod); msg != "" {
		c.out = append(c.out, msg)
	}
}

// fanOut returns the repository packages among the import paths.
func fanOut(modPath string, imps []string) []string {
	out := repoImports(modPath, imps)
	sort.Strings(out)
	return out
}
