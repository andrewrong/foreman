// Package ears recognises the five EARS sentence forms of format-rules F2.
package ears

import (
	"regexp"
	"strings"
)

type Pattern string

const (
	Ubiquitous Pattern = "ubiquitous"
	Event      Pattern = "event"
	State      Pattern = "state"
	Unwanted   Pattern = "unwanted"
	Optional   Pattern = "optional"
)

// Whitespace around the <placeholder> is optional; the clause parts must
// be non-blank. Optional is tried before State so that "在具备…" is never
// read as a State sentence.
var (
	optionalRE   = regexp.MustCompile(`^在具备\s*\S.*?\s*的情况下，系统应当\s*\S.*$`)
	stateRE      = regexp.MustCompile(`^在\s*\S.*?\s*期间，系统应当\s*\S.*$`)
	unwantedRE   = regexp.MustCompile(`^如果\s*\S.*?\s*，那么系统应当\s*\S.*$`)
	eventRE      = regexp.MustCompile(`^当\s*\S.*?\s*时，系统应当\s*\S.*$`)
	ubiquitousRE = regexp.MustCompile(`^系统应当\s*\S.*$`)
)

// form pairs a pattern with its expression; forms lists them in the order they are tried.
type form struct {
	p  Pattern
	re *regexp.Regexp
}

func forms() []form {
	return []form{{Optional, optionalRE}, {State, stateRE}, {Unwanted, unwantedRE}, {Event, eventRE}, {Ubiquitous, ubiquitousRE}}
}

// Match reports which EARS form s is written in. Leading and trailing
// whitespace is ignored; the sentence must be a single line.
func Match(s string) (Pattern, bool) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", false
	}
	for _, f := range forms() {
		if f.re.MatchString(s) {
			return f.p, true
		}
	}
	return "", false
}
