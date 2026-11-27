package privguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rule ids follow spec/design/privguard.yaml.
const (
	RuleSecrets  = "P1"
	RuleIdentity = "P2"
	RuleFiles    = "P3"
	RuleCommit   = "P4"
)

// Finding is one problem. It never carries the matched text.
type Finding struct {
	Rule string // P1..P4
	Kind string // e.g. "abs-path", "email", "token", "binary"
	File string // name given to Scan
	Line int    // 1-based; 0 when the finding is about the whole file
}

func (f Finding) String() string {
	if f.Line == 0 {
		return fmt.Sprintf("%s: %s [%s]", f.File, f.Kind, f.Rule)
	}
	return fmt.Sprintf("%s:%d: %s [%s]", f.File, f.Line, f.Kind, f.Rule)
}

// Config holds the values a project or its owner supplies.
type Config struct {
	// Literals are owner-specific strings (user name, host names) matched
	// case-insensitively as whole words.
	Literals []string
	// ExtraPatterns are project-declared regular expressions (business
	// account shapes, chat id shapes).
	ExtraPatterns []string
	// AllowReasons are the registered reason ids a "privguard:allow <id>"
	// marker may use. Empty means no marker is honoured.
	AllowReasons []string
	// Fingerprints exempt one specific secret match: "<kind>:<sha256 hex of
	// the match>". Never a path.
	Fingerprints []string
}

type rule struct {
	rule   string // P1 or P2
	kind   string
	re     *regexp.Regexp
	secret bool // true: the inline marker cannot exempt it
	// keep decides on a candidate span [s,e) of line; nil keeps all.
	keep func(line string, s, e int) bool
}

// Scanner is a compiled Config.
type Scanner struct {
	rules   []rule
	reasons map[string]bool
	prints  map[string]bool
}

// New compiles cfg.
func New(cfg Config) (*Scanner, error) {
	sc := &Scanner{reasons: map[string]bool{}, prints: map[string]bool{}}
	sc.rules = append(sc.rules, builtinRules()...)
	for _, lit := range cfg.Literals {
		lit = strings.TrimSpace(lit)
		if lit == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)(?:^|[^\w])(` + regexp.QuoteMeta(lit) + `)(?:$|[^\w])`)
		sc.rules = append(sc.rules, rule{rule: RuleIdentity, kind: "owner-literal", re: re})
	}
	for _, p := range cfg.ExtraPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("privguard: extra pattern: %w", err)
		}
		sc.rules = append(sc.rules, rule{rule: RuleIdentity, kind: "project-pattern", re: re})
	}
	for _, r := range cfg.AllowReasons {
		sc.reasons[r] = true
	}
	for _, f := range cfg.Fingerprints {
		sc.prints[f] = true
	}
	return sc, nil
}

var defaultScanner = func() *Scanner {
	sc, err := New(Config{})
	if err != nil {
		panic(err)
	}
	return sc
}()

// Scan checks one file with the default rules: the P3 file rules, then the
// P1/P2 text rules on the content. name is used in findings and for the
// file-type rule only; it never exempts content.
func Scan(name string, content []byte) []Finding { return defaultScanner.Scan(name, content) }

// Redact replaces everything the text rules match with a [redacted:kind]
// marker, using the default (owner-independent) rules.
func Redact(text string) string { return defaultScanner.Redact(text) }

// Scan is Scan with this scanner's config.
func (sc *Scanner) Scan(name string, content []byte) []Finding {
	out := checkFile(name, content)
	for _, f := range out {
		if f.Kind == "binary" {
			return out // not text: nothing else to read
		}
	}
	return append(out, sc.ScanText(name, string(content))...)
}

var allowMarker = regexp.MustCompile(`privguard:allow\s+([A-Za-z0-9_-]+)`)

// ScanText applies the P1/P2 text rules only (commit messages, PR text).
func (sc *Scanner) ScanText(name, text string) []Finding {
	var out []Finding
	for i, line := range strings.Split(text, "\n") {
		allowed := false
		if m := allowMarker.FindStringSubmatch(line); m != nil && sc.reasons[m[1]] {
			allowed = true
		}
		for _, h := range sc.hits(line) {
			if allowed && !h.secret {
				continue
			}
			if h.secret && sc.prints[h.kind+":"+Fingerprint(line[h.s:h.e])] {
				continue
			}
			out = append(out, Finding{Rule: h.rule, Kind: h.kind, File: name, Line: i + 1})
		}
	}
	return out
}

// Redact is Redact with this scanner's config. Inline markers are ignored.
func (sc *Scanner) Redact(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		hs := sc.hits(line)
		if len(hs) == 0 {
			continue
		}
		var b strings.Builder
		pos := 0
		for _, h := range hs {
			b.WriteString(line[pos:h.s])
			b.WriteString("[redacted:" + h.kind + "]")
			pos = h.e
		}
		b.WriteString(line[pos:])
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

type hit struct {
	rule, kind string
	secret     bool
	s, e       int
}

// hits returns the non-overlapping matches in line, sorted by start. When two
// matches overlap they merge into one (the earlier start decides the kind).
func (sc *Scanner) hits(line string) []hit {
	var hs []hit
	for _, r := range sc.rules {
		for _, m := range r.re.FindAllStringSubmatchIndex(line, -1) {
			s, e := m[0], m[1]
			if len(m) >= 4 && m[2] >= 0 {
				s, e = m[2], m[3]
			}
			if r.keep != nil && !r.keep(line, s, e) {
				continue
			}
			hs = append(hs, hit{r.rule, r.kind, r.secret, s, e})
		}
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].s < hs[j].s })
	var out []hit
	for _, h := range hs {
		if n := len(out); n > 0 && h.s < out[n-1].e {
			if h.e > out[n-1].e {
				out[n-1].e = h.e
			}
			out[n-1].secret = out[n-1].secret || h.secret
			continue
		}
		out = append(out, h)
	}
	return out
}

// Fingerprint is the value to put in Config.Fingerprints after "<kind>:".
func Fingerprint(match string) string {
	sum := sha256.Sum256([]byte(match))
	return hex.EncodeToString(sum[:])
}
