package model

import "strings"

// Guardrail is one entry of spec/guardrails.yaml (D-063): a path glob whose
// match in a PR's real diff needs the owner's approval (H6). The daemon's
// config module loads the list; stages receive it as a value.
type Guardrail struct {
	ID   string
	Glob string
}

// MatchGlob reports whether the slash-separated path matches pattern. "*"
// matches within one path element, "**" matches any number of elements, and
// "?" one character. A trailing "/**" matches the directory's children, never
// the bare directory name.
func MatchGlob(pattern, path string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchParts(pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return len(path) > 0
			}
			for i := 0; i <= len(path); i++ {
				if matchParts(pat[1:], path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 || !matchElem(pat[0], path[0]) {
			return false
		}
		pat, path = pat[1:], path[1:]
	}
	return len(path) == 0
}

func matchElem(pat, s string) bool {
	if pat == "" {
		return s == ""
	}
	switch pat[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if matchElem(pat[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && matchElem(pat[1:], s[1:])
	}
	return s != "" && s[0] == pat[0] && matchElem(pat[1:], s[1:])
}

// Hits returns the guardrails matched by at least one of the files, in list
// order, each as "<id> <first matching file>".
func Hits(rules []Guardrail, files []string) []string {
	var out []string
	for _, r := range rules {
		for _, f := range files {
			if MatchGlob(r.Glob, f) {
				out = append(out, r.ID+" "+f)
				break
			}
		}
	}
	return out
}
