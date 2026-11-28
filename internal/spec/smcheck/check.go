package smcheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Result carries the model counts plus every distinct error in discovery order.
type Result struct {
	Configs  int
	Events   int
	Rows     int
	Expanded int
	Errors   []string
}

// Summary is a one-line count of what was checked.
func (r Result) Summary() string {
	return fmt.Sprintf("configs=%d events=%d rows=%d (expanded %d)", r.Configs, r.Events, r.Rows, r.Expanded)
}

type checker struct {
	*Spec
	errs []string
}

func (c *checker) errorf(format string, a ...any) {
	if s := fmt.Sprintf(format, a...); !slices.Contains(c.errs, s) {
		c.errs = append(c.errs, s)
	}
}

// Check runs every check. A non-nil error means the input is unusable (parse
// failure or a guard dimension with no value set); spec violations are
// reported in Result.Errors.
func Check(stateMachineYAML, protocolRunYAML []byte) (Result, error) {
	s, err := Parse(stateMachineYAML)
	if err != nil {
		return Result{}, err
	}
	var pr struct {
		Files []struct {
			ID     string
			Fields []struct{ Name, Type string }
		}
	}
	if err := yaml.Unmarshal(protocolRunYAML, &pr); err != nil {
		return Result{}, fmt.Errorf("protocol-run.yaml: %w", err)
	}
	c := &checker{Spec: s}
	c.coverage()
	c.uncappedInc()
	c.reqHold()
	c.counterIncremented()
	c.selfLoops()
	c.ignoredOverlap()
	c.reqIgnoredOverlap()
	c.cycles()
	c.reqCoverage()
	var dec string
	for _, f := range pr.Files {
		if f.ID == "PF-RESULT" {
			for _, fl := range f.Fields {
				if fl.Name == "decision" {
					dec = fl.Type
					break
				}
			}
			break
		}
	}
	c.enumMatch("step_decision", `step 级：enum ([a-z_|]+)`, dec)
	c.enumMatch("req_decision", `需求级：enum ([a-z_|]+)`, dec)
	return Result{len(s.Configs), len(s.Events), len(s.Rows), len(s.xrows), c.errs}, nil
}

// enumMatch cross-checks a protocol-run decision enum against guard_enums.
func (c *checker) enumMatch(name, re, decision string) {
	var got []string
	if m := regexp.MustCompile(re).FindStringSubmatch(decision); m != nil {
		got = strings.FieldsFunc(m[1], func(r rune) bool { return r == '|' })
	}
	want := c.enum(name)
	if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		c.errorf("protocol %s mismatch: %v vs %v", name, got, want)
	}
}

func (s *Spec) typeDef(name string) TypeDef {
	for _, t := range s.Types {
		if t.Name == name {
			return t
		}
	}
	return TypeDef{}
}

func active(state string) bool { return state == "queued" || state == "running" || state == "waiting" }

// gaps enumerates every value combination of the dimensions named by the guard
// sets gs and reports, for each, the index of the first set it satisfies (-1
// when none does).
func gaps(gs []guards, values func(string) []string, fn func(label string, hit int)) {
	var dims []string
	for _, g := range gs {
		for _, d := range g {
			dims = append(dims, d.Dim)
		}
	}
	dims = uniq(dims)
	combos := [][]string{{}}
	for _, d := range dims {
		var next [][]string
		for _, c := range combos {
			for _, v := range values(d) {
				next = append(next, append(slices.Clone(c), v))
			}
		}
		combos = next
	}
	for _, combo := range combos {
		assign := map[string]string{}
		var parts []string
		for i, d := range dims {
			assign[d] = combo[i]
			parts = append(parts, d+"="+combo[i])
		}
		fn("{"+strings.Join(parts, ", ")+"}", slices.IndexFunc(gs, func(g guards) bool { return matches(g, assign) }))
	}
}

func (s *Spec) validTarget(cf Config, to Target) bool {
	td := s.typeDef(cf.Type)
	stage := to.Stage
	if stage == "same" {
		stage = cf.Stage
	}
	switch to.State {
	case "done", "dropped":
		return true
	case "queued":
		if stage == "first_stage" {
			stage = td.FirstStage
		}
		return stage == "resume_stage" || slices.Contains(td.Stages, stage)
	case "running":
		return slices.Contains(td.RunningStages, stage)
	case "waiting":
		for _, w := range s.WaitingReasons {
			if w.Name == to.Reason {
				return slices.Contains(w.Types, cf.Type) && (w.Stages == nil || slices.Contains(w.Stages, cf.Stage))
			}
		}
	}
	return false
}

func (c *checker) coverage() {
	for _, cf := range c.Configs {
		for _, ev := range c.Events {
			var m []*Row
			for i := range c.xrows {
				if r := &c.xrows[i]; r.Event == ev && rowMatches(r, cf) {
					m = append(m, r)
				}
			}
			if len(m) == 0 {
				if !c.isIgnored(cf, ev) {
					c.errorf("uncovered: %s event=%s", cf, ev)
				}
				continue
			}
			gs := make([]guards, len(m))
			for i, r := range m {
				gs[i] = r.Guard
			}
			gaps(gs, c.DimValues, func(label string, hit int) {
				if hit < 0 {
					c.errorf("guard gap: %s event=%s %s", cf, ev, label)
				} else if !c.validTarget(cf, m[hit].To) {
					c.errorf("invalid target: %s from %s -> %s", m[hit].ID, cf, m[hit].To)
				}
			})
		}
		if !active(cf.State) {
			continue
		}
		exits := 0
		for i := range c.xrows {
			r := &c.xrows[i]
			self := r.To.State == cf.State && (r.To.Stage == "" || r.To.Stage == "same") && r.To.Reason == cf.Reason
			if rowMatches(r, cf) && !self {
				exits++
			}
		}
		if exits == 0 {
			c.errorf("no exit: %s", cf)
		}
	}
}

// incName returns the counter of an inc:<counter> effect.
func incName(effect string) (string, bool) {
	n, ok := strings.CutPrefix(effect, "inc:")
	n, _, _ = strings.Cut(n, ":")
	return n, ok
}

func hasInc(effects []string) bool {
	return slices.ContainsFunc(effects, func(e string) bool { _, ok := incName(e); return ok })
}

// uncappedInc: every inc of a counter must sit under a below-guard of it.
func (c *checker) uncappedInc() {
	for _, r := range c.xrows {
		for _, e := range r.Effects {
			name, ok := incName(e)
			if !ok || name == "access_denied" {
				continue
			}
			if !slices.ContainsFunc(r.Guard, func(g Guard) bool {
				return g.Val == "below" && (g.Dim == "counter:"+name || g.Dim == "req_counter:"+name)
			}) {
				c.errorf("unguarded inc: %s %s", r.ID, e)
			}
		}
	}
}

func (c *checker) counterIncremented() {
	var all []string
	for _, r := range c.xrows {
		all = append(all, r.Effects...)
	}
	for _, r := range c.HoldRows {
		all = append(all, r.Effects...)
	}
	for _, e := range c.HoldEscalate {
		all = append(all, e.Effects...)
	}
	for _, e := range c.Escalate {
		all = append(all, e.Effects...)
	}
	for _, n := range slices.Concat(c.Counters, c.ReqCounters) {
		if !slices.Contains(all, "inc:"+n.Name) {
			c.errorf("counter never incremented: %s", n.Name)
		}
	}
}

func (c *checker) selfLoops() {
	for _, r := range c.xrows {
		t := r.To
		if len(r.States) != 1 || r.States[0] != t.State || (t.State != "waiting" && t.State != "running") {
			continue
		}
		if t.Reason != r.Reason || (t.Stage != "" && t.Stage != "same") {
			continue
		}
		if t.State == "running" && r.Stages != nil && len(r.Stages) != 1 {
			continue
		}
		if !hasInc(r.Effects) {
			c.errorf("unbounded self-loop: %s", r.ID)
		}
	}
}

func (c *checker) ignoredOverlap() {
	for _, cf := range c.Configs {
		for _, ig := range c.Ignored {
			if ig.States == nil || !slices.Contains(ig.States, cf.State) {
				continue
			}
			evs := ig.Events
			if ig.AllEvents {
				evs = c.Events
			}
			for _, ev := range evs {
				if i := slices.IndexFunc(c.xrows, func(r Row) bool { return r.Event == ev && rowMatches(&r, cf) }); i >= 0 {
					c.errorf("ignored overlaps row: %s %s event=%s", c.xrows[i].ID, cf, ev)
				}
			}
		}
	}
}

// holdHas reports whether a table B row leaves hold v on event ev.
func (c *checker) holdHas(v, ev string) bool {
	return slices.ContainsFunc(c.HoldRows, func(r HoldRow) bool { return r.Event == ev && slices.Contains(r.From, v) })
}

func (c *checker) reqHold() {
	var events []string
	for _, r := range c.HoldRows {
		events = append(events, r.Event)
	}
	events = uniq(events)
	for _, v := range c.HoldValues {
		for _, ev := range events {
			var gs []guards
			for _, r := range c.HoldRows {
				if r.Event == ev && slices.Contains(r.From, v) {
					gs = append(gs, r.Guard)
				}
			}
			if gs != nil {
				gaps(gs, c.HoldDimValues, func(label string, hit int) {
					if hit < 0 {
						c.errorf("req guard gap: %s %s %s", v, ev, label)
					}
				})
			}
		}
		if v != "none" && !slices.ContainsFunc(c.HoldRows, func(r HoldRow) bool { return slices.Contains(r.From, v) && r.To != v }) {
			c.errorf("req hold no exit: %s", v)
		}
	}
	for _, ev := range []string{"replan_trigger", "rescue_trigger", "owner_trigger"} {
		for _, v := range c.HoldValues {
			if v != "owner" && v != "parked" && !c.holdHas(v, ev) && !(v == "rescue" && ev == "replan_trigger") {
				c.errorf("req trigger unhandled: %s %s (precedence note covers only lower-priority)", v, ev)
			}
		}
	}
}

func (c *checker) reqCoverage() {
	var events []string
	for _, r := range c.HoldRows {
		events = append(events, r.Event)
	}
	for _, i := range c.HoldIgnored {
		events = append(events, i.Events...)
	}
	for _, v := range c.HoldValues {
		for _, ev := range uniq(events) {
			if !c.holdHas(v, ev) && !c.holdIgnored(v, ev) {
				c.errorf("req uncovered: hold=%s event=%s", v, ev)
			}
		}
	}
}

func (c *checker) holdIgnored(v, ev string) bool {
	return slices.ContainsFunc(c.HoldIgnored, func(i HoldIgnore) bool {
		return slices.Contains(i.From, v) && slices.Contains(i.Events, ev)
	})
}

func (c *checker) reqIgnoredOverlap() {
	for _, i := range c.HoldIgnored {
		for _, v := range i.From {
			for _, ev := range i.Events {
				if j := slices.IndexFunc(c.HoldRows, func(r HoldRow) bool { return r.Event == ev && slices.Contains(r.From, v) }); j >= 0 {
					c.errorf("req ignored overlaps row: %s hold=%s event=%s", c.HoldRows[j].ID, v, ev)
				}
			}
		}
	}
}
