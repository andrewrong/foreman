package lint

import (
	"regexp"
	"strings"
)

// bannedWords is the F5 vague-word list (spec/format-rules.yaml F5).
var bannedWords = []string{"适当", "合理", "尽量", "必要时", "等等", "相关的", "大约"}

// quotedRe matches quoted spans whose quote characters are part of the
// string content; verbatim quotation is exempt from F5.
var quotedRe = regexp.MustCompile(`『[^』]*』|“[^”]*”|"[^"]*"`)

// checkWords implements F5 over every string value in every spec file.
func checkWords(files []specFile) []Finding {
	var out []Finding
	for _, f := range files {
		seen := map[string]bool{}
		var walk func(v any, owner string)
		walk = func(v any, owner string) {
			switch x := v.(type) {
			case map[string]any:
				if s, ok := x["id"].(string); ok {
					owner = s
				}
				for _, k := range sortedKeys(x) {
					walk(x[k], owner)
				}
			case []any:
				for _, e := range x {
					walk(e, owner)
				}
			case string:
				text := quotedRe.ReplaceAllString(x, "")
				for _, w := range bannedWords {
					if strings.Contains(text, w) && !seen[owner+"|"+w] {
						seen[owner+"|"+w] = true
						out = append(out, Finding{"F5", f.rel, owner, "banned vague word " + w})
					}
				}
			}
		}
		walk(f.root, "")
	}
	return out
}
