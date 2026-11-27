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
		var walk func(v any, parentV2 bool)
		walk = func(v any, parentV2 bool) {
			switch x := v.(type) {
			case map[string]any:
				id := str(x["id"])
				tagged := parentV2
				if p, ok := x["phase"]; ok && id != "" {
					if str(p) != phaseV2 {
						out = append(out, Finding{"F10", f.rel, id, "phase must be v2"})
					} else {
						tagged = true
					}
				}
				if tagged && id != "" {
					v2[id] = true
					v2[base+":"+id] = true
				}
				for _, k := range sortedKeys(x) {
					walk(x[k], tagged)
				}
			case []any:
				for _, e := range x {
					walk(e, parentV2)
				}
			}
		}
		walk(f.root, fileV2)
		if fileV2 {
			v2[base] = true
		}
	}

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
