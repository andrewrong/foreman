package lint

import "regexp"

// runtimeIDRe matches runtime instance ids used in protocol examples, such
// as R-foreman-12/S2 (requirement R-foreman-12, step S2); they are data,
// not spec ids.
var runtimeIDRe = regexp.MustCompile(`\bR-[A-Za-z0-9]+-\d+(?:/[A-Za-z0-9._-]+)*`)

// checkIDs implements F1: ids are unique within a file and every id
// mentioned anywhere in spec/ resolves to a definition somewhere in spec/.
// A definition is the value of an `id` field, or a map key that is an id
// (e.g. the H1..H6 params of D-005). Only values that are spec ids take part
// in the uniqueness check; example payloads reuse local ids such as T1. Keys of a `coverage` map are
// references, not definitions.
func checkIDs(files []specFile) []Finding {
	var out []Finding
	defined := map[string]bool{}
	var refs []idRef

	for _, f := range files {
		w := &idWalker{file: f.rel, seen: map[string]bool{}, defined: defined}
		w.walk(f.root, false)
		refs = append(refs, w.refs...)
		out = append(out, w.out...)
	}

	reported := map[idRef]bool{}
	for _, r := range refs {
		if defined[r.id] || reported[r] {
			continue
		}
		reported[r] = true
		out = append(out, Finding{"F1", r.file, r.id, "reference does not resolve to a defined id"})
	}
	return out
}

// idRef is one id mention found in a file.
type idRef struct{ file, id string }

// idWalker scans one spec file for id definitions and references.
type idWalker struct {
	file    string
	seen    map[string]bool
	defined map[string]bool
	refs    []idRef
	out     []Finding
}

func (w *idWalker) scan(s string) {
	s = runtimeIDRe.ReplaceAllString(s, "")
	for _, m := range idPattern.FindAllString(s, -1) {
		w.refs = append(w.refs, idRef{w.file, m})
	}
	for _, m := range prefixPattern.FindAllStringSubmatch(s, -1) {
		if p := m[1]; !knownPrefixes()[p] && !externalPrefixes()[p] {
			w.refs = append(w.refs, idRef{w.file, m[0]})
		}
	}
}

func (w *idWalker) walk(v any, underCoverage bool) {
	switch x := v.(type) {
	case map[string]any:
		w.walkMap(x, underCoverage)
	case []any:
		for _, e := range x {
			w.walk(e, underCoverage)
		}
	case string:
		w.scan(x)
	}
}

func (w *idWalker) walkMap(x map[string]any, underCoverage bool) {
	for _, k := range sortedKeys(x) {
		w.scan(k)
		if idPattern.FindString(k) == k && !underCoverage {
			w.defined[k] = true
		}
		if s, ok := x[k].(string); ok && k == "id" && s != "" && idPattern.FindString(s) == s {
			if w.seen[s] {
				w.out = append(w.out, Finding{"F1", w.file, s, "duplicate id in file"})
			}
			w.seen[s] = true
			w.defined[s] = true
		}
		w.walk(x[k], underCoverage || k == "coverage")
	}
}
