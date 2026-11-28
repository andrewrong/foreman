package lint

import "path"

const (
	phaseV2   = "v2"
	testsFile = "tests.yaml"
)

// checkPhase implements F10: a phase tag on an item (a map that has an id)
// or on a whole file must be v2, and the generated v1 test list must not
// contain a test that exists only for phase: v2 items.
func checkPhase(files []specFile) []Finding {
	var out []Finding
	v2 := map[string]bool{} // ids tagged v2, bare and as "<file>:<id>"
	for _, f := range files {
		base := path.Base(f.rel)
		base = base[:len(base)-len(path.Ext(base))]
		fileV2 := false
		if root := asMap(f.root); root != nil {
			if p, ok := root["phase"]; ok {
				if str(p) != phaseV2 {
					out = append(out, Finding{"F10", f.rel, "", "file phase must be v2"})
				} else {
					fileV2 = true
				}
			}
		}
		w := &phaseWalker{file: f.rel, base: base, v2: v2}
		w.walk(f.root, fileV2)
		out = append(out, w.out...)
		if fileV2 {
			v2[base] = true
		}
	}
	return append(out, checkV2Tests(files, v2)...)
}

// phaseWalker collects v2-tagged ids of one spec file.
type phaseWalker struct {
	file string
	base string
	v2   map[string]bool
	out  []Finding
}

func (w *phaseWalker) walk(v any, parentV2 bool) {
	switch x := v.(type) {
	case map[string]any:
		id := str(x["id"])
		tagged := parentV2
		if p, ok := x["phase"]; ok && id != "" {
			if str(p) != phaseV2 {
				w.out = append(w.out, Finding{"F10", w.file, id, "phase must be v2"})
			} else {
				tagged = true
			}
		}
		if tagged && id != "" {
			w.v2[id] = true
			w.v2[w.base+":"+id] = true
		}
		for _, k := range sortedKeys(x) {
			w.walk(x[k], tagged)
		}
	case []any:
		for _, e := range x {
			w.walk(e, parentV2)
		}
	}
}

// checkV2Tests reports tests that cover only phase: v2 items.
func checkV2Tests(files []specFile, v2 map[string]bool) []Finding {
	var out []Finding
	for _, t := range items(files, testsFile, "tests") {
		covers := strList(t["covers"])
		if len(covers) == 0 {
			continue
		}
		onlyV2 := true
		for _, c := range covers {
			if !v2[c] && !v2[fileOf(c)] {
				onlyV2 = false
			}
		}
		if onlyV2 {
			out = append(out, Finding{"F10", testsFile, str(t["name"]), "test covers only phase: v2 items but is in the v1 test list"})
		}
	}
	return out
}

// fileOf returns the "<file>" part of a "<file>:<id>" cover entry.
func fileOf(c string) string {
	for i := 0; i < len(c); i++ {
		if c[i] == ':' {
			return c[:i]
		}
	}
	return ""
}
