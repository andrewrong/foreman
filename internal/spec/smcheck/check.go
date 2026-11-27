// Package smcheck is the exhaustive checker for spec/design/state-machine.yaml
// (C-02). It is a port of tools/smcheck.rb and keeps its error wording, so the
// two tools stay comparable.
package smcheck

import (
	"fmt"
	"slices"
)

// Result carries the counts the ruby script prints plus every distinct error
// in discovery order.
type Result struct {
	Configs  int
	Events   int
	Rows     int
	Expanded int
	Errors   []string
}

// Summary is the first line the ruby script prints.
func (r Result) Summary() string {
	return fmt.Sprintf("configs=%d events=%d rows=%d (expanded %d)", r.Configs, r.Events, r.Rows, r.Expanded)
}

// Check runs every check on the two YAML documents. A non-nil error means the
// input is unusable (parse failure, malformed structure, or a guard dimension
// with no declared enum, where the ruby script raises); spec violations are
// reported in Result.Errors.
func Check(stateMachineYAML, protocolRunYAML []byte) (res Result, err error) {
	sm, err := parseDoc(stateMachineYAML)
	if err != nil {
		return res, fmt.Errorf("state-machine.yaml: %w", err)
	}
	pr, err := parseDoc(protocolRunYAML)
	if err != nil {
		return res, fmt.Errorf("protocol-run.yaml: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			if se, ok := p.(specError); ok {
				err = fmt.Errorf("%s", string(se))
			} else {
				err = fmt.Errorf("malformed spec: %v", p)
			}
		}
	}()
	c := newChecker(sm)
	c.run(pr)
	return Result{Configs: len(c.configs), Events: len(c.events), Rows: len(c.rows),
		Expanded: len(c.xrows), Errors: c.errs}, nil
}

type config struct{ state, stage, typ, reason string }

// String is the ruby Hash#inspect of the config.
func (c config) String() string {
	stage := "nil"
	if c.stage != "" {
		stage = inspectStr(c.stage)
	}
	s := fmt.Sprintf(":state=>%s, :stage=>%s, :type=>%s", inspectStr(c.state), stage, inspectStr(c.typ))
	if c.state == "waiting" {
		s += ", :reason=>" + inspectStr(c.reason)
	}
	return "{" + s + "}"
}

type kv struct {
	k string
	v any
}

type row struct {
	id, event string
	from, to  *omap
	guard     []kv
	effects   []string
}

func guardKVs(v any) []kv {
	if v == nil {
		return nil
	}
	g := asMap(v, "guard")
	out := make([]kv, 0, len(g.keys))
	for _, k := range g.keys {
		out = append(out, kv{k, g.m[k]})
	}
	return out
}

func parseRow(m *omap) *row {
	return &row{id: str(m.get("id")), event: str(m.get("event")),
		from: asMap(m.get("from"), "row from"), to: asMap(m.get("to"), "row to"),
		guard: guardKVs(m.get("guard")), effects: listOrEmpty(m.get("effects"))}
}

// dimKV is one flattened guard dimension.
type dimKV struct{ k, v string }

func guardDims(g []kv) []dimKV {
	out := make([]dimKV, 0, len(g))
	for _, e := range g {
		switch e.k {
		case "counter", "req_counter":
			m := asMap(e.v, e.k)
			out = append(out, dimKV{e.k + ":" + str(m.get("name")), str(m.get("is"))})
		default:
			out = append(out, dimKV{e.k, str(e.v)})
		}
	}
	return out
}

func dimsMatch(r []dimKV, assign map[string]string) bool {
	for _, d := range r {
		if assign[d.k] != d.v {
			return false
		}
	}
	return true
}

func assignInspect(dims []string, combo []string) string {
	if len(dims) == 0 {
		return "{}"
	}
	s := "{"
	for i, d := range dims {
		if i > 0 {
			s += ", "
		}
		s += inspectStr(d) + "=>" + inspectStr(combo[i])
	}
	return s + "}"
}

func product(space [][]string) [][]string {
	combos := [][]string{{}}
	for _, vals := range space {
		var next [][]string
		for _, c := range combos {
			for _, v := range vals {
				next = append(next, append(slices.Clone(c), v))
			}
		}
		combos = next
	}
	return combos
}

func hasPrefixStr(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func incsOf(effects []string) []string {
	var out []string
	for _, e := range effects {
		if hasPrefixStr(e, "inc:") {
			out = append(out, e)
		}
	}
	return out
}

type checker struct {
	sm, d         *omap
	groups        map[string][]string
	types         []string
	stageOf       map[string][]string
	rows, xrows   []*row
	esc           []*omap
	configs       []config
	events        []string
	exitByReason  map[string][]string
	runningEvents []string
	ignored       []*omap
	stepEnum      []string
	errs          []string
	seen          map[string]bool
	hrows         []*hrow
}

func (c *checker) errorf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if !c.seen[s] {
		c.seen[s] = true
		c.errs = append(c.errs, s)
	}
}

func (c *checker) expandTypes(t any) []string {
	if s, ok := t.(string); ok {
		if s == "all" {
			return c.types
		}
		if g, ok := c.groups[s]; ok {
			return g
		}
		return []string{s}
	}
	var out []string
	for _, x := range asSlice(t, "types") {
		if g, ok := c.groups[str(x)]; ok {
			out = append(out, g...)
		} else {
			out = append(out, str(x))
		}
	}
	return out
}

func (c *checker) isWork(ty string) bool { return slices.Contains(c.groups["work"], ty) }

func (c *checker) runningStages(ty string) []string {
	rs := asMap(c.d.get("running_stages"), "running_stages")
	if c.isWork(ty) {
		return strList(rs.get("work"))
	}
	return strList(rs.get("integrate"))
}

func (c *checker) firstStage(ty string) string {
	fs := asMap(c.d.get("first_stage"), "first_stage")
	if c.isWork(ty) {
		return str(fs.get("work"))
	}
	return str(fs.get("integrate"))
}

func newChecker(sm *omap) *checker {
	c := &checker{sm: sm, seen: map[string]bool{}, groups: map[string][]string{},
		stageOf: map[string][]string{}, exitByReason: map[string][]string{}}
	c.d = asMap(sm.get("dims"), "dims")
	types := asMap(c.d.get("types"), "dims.types")
	c.types = types.keys
	g := asMap(c.d.get("type_groups"), "dims.type_groups")
	for _, k := range g.keys {
		c.groups[k] = strList(g.get(k))
	}
	for _, ty := range c.types {
		c.stageOf[ty] = strList(asMap(types.get(ty), "type").get("stages"))
	}
	c.stepEnum = strList(asMap(c.d.get("guard_enums"), "guard_enums").get("step_decision"))
	return c
}

func (c *checker) buildConfigs() {
	wr := asMap(c.d.get("waiting_reasons"), "waiting_reasons")
	for _, ty := range c.types {
		stages := c.stageOf[ty]
		for _, st := range stages {
			c.configs = append(c.configs, config{"queued", st, ty, ""})
		}
		for _, st := range c.runningStages(ty) {
			c.configs = append(c.configs, config{"running", st, ty, ""})
		}
		for _, r := range wr.keys {
			spec := asMap(wr.get(r), "waiting reason")
			if !slices.Contains(c.expandTypes(spec.get("types")), ty) {
				continue
			}
			var rst []string
			if s, ok := spec.get("stages").(string); ok && s == "all" {
				rst = stages
			} else {
				for _, st := range uniq(strList(spec.get("stages"))) {
					if slices.Contains(stages, st) {
						rst = append(rst, st)
					}
				}
			}
			for _, st := range rst {
				c.configs = append(c.configs, config{"waiting", st, ty, r})
			}
		}
		for _, s := range []string{"done", "dropped"} {
			c.configs = append(c.configs, config{s, "", ty, ""})
		}
	}
}

func (c *checker) rowMatches(r *row, cf config) bool {
	f := r.from
	if !slices.Contains(strList(f.get("state")), cf.state) {
		return false
	}
	if rs := f.get("reason"); rs != nil && str(rs) != cf.reason {
		return false
	}
	if !slices.Contains(c.expandTypes(f.get("types")), cf.typ) {
		return false
	}
	if cf.stage != "" {
		st := f.get("stages")
		if s, ok := st.(string); !(ok && s == "all") && st != nil && !slices.Contains(strList(st), cf.stage) {
			return false
		}
	}
	return true
}

func (c *checker) loadTables() {
	for _, m := range maps(c.sm.get("step_table"), "step_table") {
		c.rows = append(c.rows, parseRow(m))
	}
	c.ignored = maps(c.sm.get("ignored"), "ignored")
	c.esc = maps(c.sm.get("escalate"), "escalate")
	var evs []string
	for _, r := range c.rows {
		evs = append(evs, r.event)
	}
	for _, ig := range c.ignored {
		if l, ok := ig.get("events").([]any); ok {
			evs = append(evs, strList(l)...)
		}
	}
	c.events = uniq(evs)
	for _, r := range c.rows {
		if rs := r.from.get("reason"); rs != nil {
			c.exitByReason[str(rs)] = append(c.exitByReason[str(rs)], r.event)
		}
		if slices.Contains(strList(r.from.get("state")), "running") {
			c.runningEvents = append(c.runningEvents, r.event)
		}
	}
	c.runningEvents = uniq(c.runningEvents)
	for _, r := range c.rows {
		c.xrows = append(c.xrows, c.expandRow(r)...)
	}
}

func (c *checker) expandRow(r *row) []*row {
	if str(r.to.get("state")) != "escalate" {
		return []*row{r}
	}
	var out []*row
	for _, e := range c.esc {
		g := slices.Clone(r.guard)
		for _, x := range guardKVs(e.get("guard")) {
			if i := slices.IndexFunc(g, func(y kv) bool { return y.k == x.k }); i >= 0 {
				g[i].v = x.v
			} else {
				g = append(g, x)
			}
		}
		out = append(out, &row{id: r.id + "+esc", event: r.event, from: r.from,
			to: asMap(e.get("to"), "escalate to"), guard: g,
			effects: append(slices.Clone(r.effects), listOrEmpty(e.get("effects"))...)})
	}
	return out
}

func (c *checker) isIgnored(cf config, ev string) bool {
	for _, ig := range c.ignored {
		if ig.get("states") != nil {
			if !slices.Contains(strList(ig.get("states")), cf.state) {
				continue
			}
			if s, ok := ig.get("events").(string); ok && s == "all" {
				return true
			}
			if l, ok := ig.get("events").([]any); ok && slices.Contains(strList(l), ev) {
				return true
			}
			continue
		}
		switch str(ig.get("rule")) {
		case "waiting_foreign_exit":
			if cf.state != "waiting" || slices.Contains(c.exitByReason[cf.reason], ev) {
				continue
			}
			for r, evs := range c.exitByReason {
				if r != cf.reason && slices.Contains(evs, ev) {
					return true
				}
			}
		case "running_foreign_result":
			if cf.state == "running" && slices.Contains(c.runningEvents, ev) {
				return true
			}
		}
	}
	return false
}

func (c *checker) dimValues(dim string) []string {
	if hasPrefixStr(dim, "counter:") || hasPrefixStr(dim, "req_counter:") {
		return []string{"below", "at_cap"}
	}
	switch dim {
	case "hold":
		return []string{"set", "none"}
	case "deps":
		return []string{"done", "pending"}
	case "lease":
		return []string{"ok", "wait"}
	case "tier":
		return []string{"R1", "R2", "R3"}
	case "single_step":
		return []string{"one", "many"}
	case "budget":
		return []string{"ok", "over"}
	case "decision":
		return c.stepEnum
	}
	fail("guard dim without declared enum: %s", dim)
	return nil
}

func (c *checker) validTarget(cf config, to *omap) bool {
	switch str(to.get("state")) {
	case "done", "dropped":
		return true
	case "queued":
		st := str(to.get("stage"))
		switch st {
		case "same":
			st = cf.stage
		case "first_stage":
			st = c.firstStage(cf.typ)
		case "resume_stage":
			return true
		}
		return slices.Contains(c.stageOf[cf.typ], st)
	case "running":
		st := str(to.get("stage"))
		if st == "same" {
			st = cf.stage
		}
		return slices.Contains(c.runningStages(cf.typ), st)
	case "waiting":
		wr := asMap(c.d.get("waiting_reasons"), "waiting_reasons")
		sp, ok := wr.get(str(to.get("reason"))).(*omap)
		if !ok {
			return false
		}
		if !slices.Contains(c.expandTypes(sp.get("types")), cf.typ) {
			return false
		}
		if s, ok := sp.get("stages").(string); ok && s == "all" {
			return true
		}
		return slices.Contains(strList(sp.get("stages")), cf.stage)
	}
	return false
}

func (c *checker) coverage() {
	for _, cf := range c.configs {
		for _, ev := range c.events {
			var m []*row
			for _, r := range c.xrows {
				if r.event == ev && c.rowMatches(r, cf) {
					m = append(m, r)
				}
			}
			if len(m) == 0 {
				if !c.isIgnored(cf, ev) {
					c.errorf("uncovered: %s event=%s", cf, ev)
				}
				continue
			}
			c.guardSpace(cf, ev, m)
		}
		if cf.state != "queued" && cf.state != "running" && cf.state != "waiting" {
			continue
		}
		exits := 0
		for _, r := range c.xrows {
			if !c.rowMatches(r, cf) {
				continue
			}
			stage := str(r.to.get("stage"))
			if str(r.to.get("state")) == cf.state && (stage == "" || stage == "same") && str(r.to.get("reason")) == cf.reason {
				continue
			}
			exits++
		}
		if exits == 0 {
			c.errorf("no exit: %s", cf)
		}
	}
}

func (c *checker) guardSpace(cf config, ev string, m []*row) {
	rds := make([][]dimKV, len(m))
	var dims []string
	for i, r := range m {
		rds[i] = guardDims(r.guard)
		for _, d := range rds[i] {
			dims = append(dims, d.k)
		}
	}
	dims = uniq(dims)
	space := make([][]string, len(dims))
	for i, d := range dims {
		space[i] = c.dimValues(d)
	}
	for _, combo := range product(space) {
		assign := map[string]string{}
		for i, d := range dims {
			assign[d] = combo[i]
		}
		var hit *row
		for i, r := range m {
			if dimsMatch(rds[i], assign) {
				hit = r
				break
			}
		}
		if hit == nil {
			c.errorf("guard gap: %s event=%s %s", cf, ev, assignInspect(dims, combo))
			continue
		}
		if !c.validTarget(cf, hit.to) {
			c.errorf("invalid target: %s from %s -> %s", hit.id, cf, inspect(hit.to))
		}
	}
}

func (c *checker) uncappedInc() {
	for _, r := range c.xrows {
		for _, e := range incsOf(r.effects) {
			name := splitColon(e)
			if name == "access_denied" {
				continue
			}
			ok := false
			for _, d := range guardDims(r.guard) {
				if (d.k == "counter:"+name || d.k == "req_counter:"+name) && d.v == "below" {
					ok = true
				}
			}
			if !ok {
				c.errorf("unguarded inc: %s %s", r.id, e)
			}
		}
	}
}

func (c *checker) run(pr *omap) {
	c.buildConfigs()
	c.loadTables()
	c.coverage()
	c.uncappedInc()
	c.reqHold()
	c.counterIncremented()
	c.selfLoops()
	c.ignoredOverlap()
	c.reqIgnoredOverlap()
	c.cycles()
	c.reqCoverage()
	c.protocolCross(pr)
}
