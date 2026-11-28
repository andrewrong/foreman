package lint

import (
	"regexp"
	"strings"
)

const (
	decisionsFile       = "decisions.yaml"
	constitutionFile    = "constitution.yaml"
	questionsFile       = "open-questions.yaml"
	maxConstitutionNone = 3
)

var (
	nameRe      = regexp.MustCompile(`\b(?:test|check):[a-z0-9][a-z0-9-]*`)
	parenRe     = regexp.MustCompile(`（[^）]*）|\([^)]*\)`)
	enforcePart = regexp.MustCompile(`^(?:none|sandbox|program:[a-z0-9/_-]+|(?:sandbox \+ )?(?:test|check):[a-z0-9][a-z0-9-]*)$`)
)

func decisionKeys() map[string]bool {
	return map[string]bool{
		"id": true, "date": true, "statement": true, "rationale": true,
		"params": true, "enforced_by": true, "status": true, "coverage": true,
		"supersedes": true, "amends": true, "resolves": true,
	}
}

// enforcement is the parsed form of an enforced_by value plus coverage.
type enforcement struct {
	valid    bool // every ；/;-separated part has an allowed form
	hasNames bool // at least one test:/check: name
	sandbox  bool // starts with sandbox (testtrace treats as covered)
	program  bool // names program:<module>
	deferred bool // coverage says "none: ..." or "phase: v2"
}

func parseEnforced(s, cov string) enforcement {
	e := enforcement{valid: true}
	clean := parenRe.ReplaceAllString(s, "")
	for _, part := range strings.FieldsFunc(clean, func(r rune) bool { return r == '；' || r == ';' }) {
		part = strings.TrimSpace(part)
		if !enforcePart.MatchString(part) {
			e.valid = false
		}
		if strings.HasPrefix(part, "program:") {
			e.program = true
		}
	}
	e.hasNames = nameRe.MatchString(clean) || nameRe.MatchString(cov)
	e.sandbox = strings.HasPrefix(strings.TrimSpace(s), "sandbox")
	c := strings.TrimSpace(cov)
	e.deferred = strings.HasPrefix(c, "none:") || strings.HasPrefix(c, "phase: v2")
	return e
}

func (e enforcement) covered() bool { return e.hasNames || e.sandbox || e.deferred }

func isSuperseded(it map[string]any) bool { return str(it["status"]) == "superseded" }

// coverageMap merges the coverage addenda of every active decision.
func coverageMap(decs []map[string]any) map[string]string {
	cov := map[string]string{}
	for _, d := range decs {
		if isSuperseded(d) {
			continue
		}
		for k, v := range asMap(d["coverage"]) {
			cov[k] = str(v)
		}
	}
	return cov
}

// checkDecisions implements F3/F4 (decisions part), F7 and F8.
func checkDecisions(files []specFile) []Finding {
	decs := items(files, decisionsFile, "decisions")
	if decs == nil {
		return nil
	}
	var out []Finding
	cov := coverageMap(decs)
	byID := map[string]map[string]any{}
	for _, d := range decs {
		byID[str(d["id"])] = d
	}
	qids := questionIDs(files)

	supersededBy := map[string]int{}
	for _, d := range decs {
		id := str(d["id"])
		out = append(out, checkDecisionLinks(d, byID, qids, supersededBy)...)
		if isSuperseded(d) {
			continue
		}
		out = append(out, checkDecisionEnforced(id, d, cov[id])...)
	}

	for _, d := range decs {
		id := str(d["id"])
		if isSuperseded(d) && supersededBy[id] != 1 {
			out = append(out, Finding{"F7", decisionsFile, id, "superseded entry must be targeted by exactly one supersedes"})
		}
	}
	return append(out, checkAmends(decs, byID)...)
}

// questionIDs collects the ids of open and resolved questions.
func questionIDs(files []specFile) map[string]bool {
	qids := map[string]bool{}
	for _, q := range items(files, questionsFile, "questions") {
		qids[str(q["id"])] = true
	}
	for _, q := range items(files, questionsFile, "resolved") {
		qids[str(q["id"])] = true
	}
	return qids
}

// checkDecisionLinks implements F7 for one decision: only the three link
// fields; targets exist. It counts supersedes targets in supersededBy.
func checkDecisionLinks(d map[string]any, byID map[string]map[string]any, qids map[string]bool, supersededBy map[string]int) []Finding {
	var out []Finding
	id := str(d["id"])
	for _, k := range sortedKeys(d) {
		if !decisionKeys()[k] {
			out = append(out, Finding{"F7", decisionsFile, id, "unknown field " + k + " (links are only supersedes, amends, resolves)"})
		}
	}
	if s := str(d["status"]); s != "active" && s != "superseded" {
		out = append(out, Finding{"F7", decisionsFile, id, "status must be active or superseded"})
	}
	for _, t := range strList(d["supersedes"]) {
		supersededBy[t]++
		if byID[t] == nil {
			out = append(out, Finding{"F7", decisionsFile, id, "supersedes target " + t + " does not exist"})
		} else if !isSuperseded(byID[t]) {
			out = append(out, Finding{"F7", decisionsFile, id, "supersedes target " + t + " does not have status superseded"})
		}
	}
	for _, t := range strList(d["amends"]) {
		if byID[t] == nil {
			out = append(out, Finding{"F7", decisionsFile, id, "amends target " + t + " does not exist"})
		}
	}
	for _, t := range strList(d["resolves"]) {
		if !qids[t] {
			out = append(out, Finding{"F7", decisionsFile, id, "resolves target " + t + " does not exist in open-questions"})
		}
	}
	return out
}

// checkDecisionEnforced implements F3/F4 for one active decision; c is its
// coverage addendum.
func checkDecisionEnforced(id string, d map[string]any, c string) []Finding {
	var out []Finding
	eb := str(d["enforced_by"])
	if strings.TrimSpace(eb) == "" {
		if c == "" {
			out = append(out, Finding{"F3", decisionsFile, id, "missing enforced_by (and no coverage entry)"})
		}
		return out
	}
	e := parseEnforced(eb, c)
	if !e.valid {
		out = append(out, Finding{"F3", decisionsFile, id, "enforced_by has a form that is not test:<name> | check:<name> | sandbox + test:<name> | none"})
	}
	if e.program && !e.covered() {
		out = append(out, Finding{"F4", decisionsFile, id, "enforced_by names only program:<module>; counts as none"})
	}
	return out
}

// checkAmends implements F8 over active entries.
func checkAmends(decs []map[string]any, byID map[string]map[string]any) []Finding {
	var out []Finding
	amenders := map[string][]string{}
	for _, d := range decs {
		if isSuperseded(d) {
			continue
		}
		id := str(d["id"])
		for _, t := range strList(d["amends"]) {
			target := byID[t]
			if target == nil || isSuperseded(target) {
				continue // historic or dangling (F7 reports dangling)
			}
			amenders[t] = append(amenders[t], id)
			if len(strList(target["amends"])) > 0 {
				out = append(out, Finding{"F8", decisionsFile, id, "amends " + t + " which is itself an amends entry"})
			}
		}
	}
	for _, d := range decs {
		id := str(d["id"])
		if len(amenders[id]) > 1 {
			out = append(out, Finding{"F8", decisionsFile, id, "amended by more than one active entry: " + strings.Join(amenders[id], ", ")})
		}
	}
	return out
}

// checkConstitution implements F3/F4 for constitution articles.
func checkConstitution(files []specFile) []Finding {
	arts := items(files, constitutionFile, "articles")
	if arts == nil {
		return nil
	}
	cov := coverageMap(items(files, decisionsFile, "decisions"))
	var out []Finding
	none := 0
	for _, a := range arts {
		if isSuperseded(a) {
			continue
		}
		id := str(a["id"])
		eb := str(a["enforced_by"])
		c := cov[id]
		if strings.TrimSpace(eb) == "" {
			if c == "" {
				out = append(out, Finding{"F3", constitutionFile, id, "missing enforced_by (and no coverage entry)"})
			}
			continue
		}
		e := parseEnforced(eb, c)
		if !e.valid {
			out = append(out, Finding{"F3", constitutionFile, id, "enforced_by has a form that is not test:<name> | check:<name> | sandbox + test:<name> | none"})
		}
		if !e.covered() {
			none++
		}
	}
	if none > maxConstitutionNone {
		out = append(out, Finding{"F4", constitutionFile, "constitution", "more than 3 articles count as enforced_by none"})
	}
	return out
}
