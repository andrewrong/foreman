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
	type ref struct{ file, id string }
	var refs []ref

	for _, f := range files {
		file := f.rel
		seen := map[string]bool{}
		scan := func(s string) {
			s = runtimeIDRe.ReplaceAllString(s, "")
			for _, m := range idPattern.FindAllString(s, -1) {
				refs = append(refs, ref{file, m})
			}
			for _, m := range prefixPattern.FindAllStringSubmatch(s, -1) {
				if p := m[1]; !knownPrefixes[p] && !externalPrefixes[p] {
					refs = append(refs, ref{file, m[0]})
				}
			}
		}
		var walk func(v any, underCoverage bool)
		walk = func(v any, underCoverage bool) {
			switch x := v.(type) {
			case map[string]any:
				for _, k := range sortedKeys(x) {
					scan(k)
					if idPattern.FindString(k) == k && !underCoverage {
						defined[k] = true
					}
					if s, ok := x[k].(string); ok && k == "id" && s != "" && idPattern.FindString(s) == s {
						if seen[s] {
							out = append(out, Finding{"F1", file, s, "duplicate id in file"})
						}
						seen[s] = true
						defined[s] = true
					}
					walk(x[k], underCoverage || k == "coverage")
				}
			case []any:
				for _, e := range x {
					walk(e, underCoverage)
				}
			case string:
				scan(x)
			}
		}
		walk(f.root, false)
	}

	reported := map[ref]bool{}
	for _, r := range refs {
		if defined[r.id] || reported[r] {
			continue
		}
		reported[r] = true
		out = append(out, Finding{"F1", r.file, r.id, "reference does not resolve to a defined id"})
	}
	return out
}
