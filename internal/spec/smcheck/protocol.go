package smcheck

import (
	"regexp"
	"slices"
)

// Cross-check: the rescuer decision enums in protocol-run must equal the guard enums.

var (
	stepEnumRe = regexp.MustCompile(`step 级：enum ([a-z_|]+)`)
	reqEnumRe  = regexp.MustCompile(`需求级：enum ([a-z_|]+)`)
)

func enumOf(re *regexp.Regexp, s string) []string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return []string{}
	}
	return append([]string{}, splitPipe(m[1])...)
}

// splitPipe is ruby String#split('|'): trailing empty fields are dropped.
func splitPipe(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '|' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	out = append(out, cur)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func (c *checker) protocolCross(pr *omap) {
	var dec string
	for _, f := range maps(pr.get("files"), "files") {
		if str(f.get("id")) != "PF-RESULT" {
			continue
		}
		for _, fl := range maps(f.get("fields"), "fields") {
			if str(fl.get("name")) == "decision" {
				dec = str(fl.get("type"))
				break
			}
		}
		break
	}
	ge := asMap(c.d.get("guard_enums"), "guard_enums")
	stepEnum, reqEnum := enumOf(stepEnumRe, dec), enumOf(reqEnumRe, dec)
	if !sameSet(stepEnum, strList(ge.get("step_decision"))) {
		c.errorf("protocol step_decision mismatch: %s vs %s", inspect(stepEnum), inspect(strList(ge.get("step_decision"))))
	}
	if !sameSet(reqEnum, strList(ge.get("req_decision"))) {
		c.errorf("protocol req_decision mismatch: %s vs %s", inspect(reqEnum), inspect(strList(ge.get("req_decision"))))
	}
}
