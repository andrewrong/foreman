// Package lint checks the spec/ directory against spec/format-rules.yaml
// (F1, F3, F4, F5, F7, F8, F10). F2 lives in internal/spec/ears; F6 and F9
// are checked elsewhere.
package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Finding is one lint violation.
type Finding struct {
	Rule    string // "F1", "F3", ...
	File    string // path relative to the spec dir, slash separated
	ID      string // offending item or reference id; may be empty
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s %s %s: %s", f.Rule, f.File, f.ID, f.Message)
}

// idPattern matches every referable id found in spec/. Single-letter
// families that are not ids in the spec (P0 priorities, L0..L2 levels, R1
// access levels, A1/N1/T1 examples) are deliberately absent.
var idPattern = regexp.MustCompile(`\b(?:` +
	`(?:D|C|Q|G)-\d+|S\d+b?|H\d+|F\d+|` +
	`(?:OP|CF|MD|PV|PI|GR|FG|DT|M|PP|AC|RT|PA|CD|RC|GHP|TX|PR|LIB|DI|HOLD|DO)-\d+|` +
	`E-[A-Z]+|PF-[A-Z]+|RES(?:-[A-Z]+)+` +
	`)\b`)

// prefixPattern finds any "<UPPER>-<digits>" token, used to notice id
// families that are neither known nor external.
var prefixPattern = regexp.MustCompile(`\b([A-Z]{1,5})-\d+\b`)

// externalPrefixes is the single list of id prefixes that name review
// findings kept outside spec/ (docs/reviews/); they never need to resolve.
func externalPrefixes() map[string]bool {
	return map[string]bool{
		"ACR": true, // Opus review findings
		"GEM": true, // Gemini review findings
	}
}

// knownPrefixes are the hyphenated prefixes of idPattern (plus tokens that
// merely look like ids and are not).
func knownPrefixes() map[string]bool {
	return map[string]bool{
		"D": true, "C": true, "Q": true, "G": true, "E": true, "M": true,
		"OP": true, "CF": true, "MD": true, "PV": true, "PI": true, "GR": true,
		"FG": true, "DT": true, "PP": true, "AC": true, "RT": true, "PA": true,
		"CD": true, "RC": true, "GHP": true, "TX": true, "PR": true, "LIB": true,
		"DI": true, "HOLD": true, "DO": true, "PF": true, "RES": true,
		"SHA": true, "UTF": true, // not ids
	}
}

type specFile struct {
	rel  string
	root any
}

// Lint runs all rules over specDir and returns the findings sorted by file,
// rule, id.
func Lint(specDir string) []Finding {
	var out []Finding
	files, errs := load(specDir)
	out = append(out, errs...)
	out = append(out, checkIDs(files)...)
	out = append(out, checkDecisions(files)...)
	out = append(out, checkConstitution(files)...)
	out = append(out, checkWords(files)...)
	out = append(out, checkPhase(files)...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.ID < b.ID
	})
	return out
}

func load(specDir string) ([]specFile, []Finding) {
	var files []specFile
	var errs []Finding
	walkErr := filepath.WalkDir(specDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		rel, _ := filepath.Rel(specDir, p)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, Finding{Rule: "F1", File: rel, Message: err.Error()})
			return nil
		}
		var root any
		if err := yaml.Unmarshal(b, &root); err != nil {
			errs = append(errs, Finding{Rule: "F1", File: rel, Message: "yaml: " + err.Error()})
			return nil
		}
		files = append(files, specFile{rel: rel, root: root})
		return nil
	})
	if walkErr != nil {
		errs = append(errs, Finding{Rule: "F1", File: ".", Message: walkErr.Error()})
	}
	return files, errs
}

// sortedKeys returns map keys in order, so findings are deterministic.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// strList accepts a scalar or a list and returns its string elements.
func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// items returns the maps in the list under key of the named file.
func items(files []specFile, rel, key string) []map[string]any {
	for _, f := range files {
		if f.rel != rel {
			continue
		}
		var out []map[string]any
		for _, e := range asList(asMap(f.root)[key]) {
			if m := asMap(e); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
