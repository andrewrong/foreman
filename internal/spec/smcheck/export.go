package smcheck

import (
	"fmt"
	"slices"
)

// Export surface: a typed view of state-machine.yaml for code generators and
// for tests that need smcheck's expansion as an independent oracle. It reuses
// the checker's parsing and expansion (type groups, "all", escalate rows) and
// adds no semantics of its own.

// Config is one (state, stage, type, reason) combination smcheck enumerates.
type Config struct{ State, Stage, Type, Reason string }

// Guard is one flattened guard condition: Dim is "hold", "deps", "lease",
// "tier", "single_step", "budget", "decision", "answer", "counter:<name>" or
// "req_counter:<name>"; Val is the required value.
type Guard struct{ Dim, Val string }

// Target is a row target as written (Stage may be same, first_stage or
// resume_stage; State may be the pseudo-target escalate).
type Target struct{ State, Stage, Reason string }

// Row is one step_table row. Stages is nil when the row applies to all stages;
// Reason is "" when the row does not constrain it.
type Row struct {
	ID, Event string
	States    []string
	Reason    string
	Types     []string
	Stages    []string
	Guard     []Guard
	To        Target
	Effects   []string
}

// EscRow is one expansion of the escalate pseudo-target.
type EscRow struct {
	Guard   []Guard
	To      Target
	Effects []string
}

// Ignore is one ignored entry. AllEvents means events: all; Rule is set for
// the rule-based entries (waiting_foreign_exit, running_foreign_result).
type Ignore struct {
	States    []string
	Events    []string
	AllEvents bool
	Rule      string
}

// TypeDef describes one step type.
type TypeDef struct {
	Name          string
	Stages        []string
	RunningStages []string
	FirstStage    string
}

// WaitingReason is one dims.waiting_reasons entry; Stages is nil for "all".
type WaitingReason struct {
	Name   string
	Types  []string
	Stages []string
}

// Counter is a counter with its cap.
type Counter struct {
	Name string
	Cap  int
}

// Enum is one guard_enums entry.
type Enum struct {
	Name   string
	Values []string
}

// HoldRow is one req_hold table row; To may be the pseudo-target
// rescue_or_owner.
type HoldRow struct {
	ID, Event string
	From      []string
	Guard     []Guard
	To        string
	Effects   []string
}

// HoldEsc is one expansion of rescue_or_owner.
type HoldEsc struct {
	Guard   []Guard
	To      string
	Effects []string
}

// HoldIgnore is one req_hold.ignored entry.
type HoldIgnore struct{ From, Events []string }

// Spec is the parsed state machine.
type Spec struct {
	States, Terminal []string
	Types            []TypeDef
	WaitingReasons   []WaitingReason
	Counters         []Counter
	ReqCounters      []Counter
	GuardEnums       []Enum
	Configs          []Config
	Events           []string
	Rows             []Row
	Escalate         []EscRow
	Ignored          []Ignore

	HoldValues   []string
	HoldRows     []HoldRow
	HoldEscalate []HoldEsc
	HoldIgnored  []HoldIgnore

	chk *checker
}

func toGuards(g []kv) []Guard {
	var out []Guard
	for _, d := range guardDims(g) {
		out = append(out, Guard{d.k, d.v})
	}
	return out
}

func toTarget(m *omap) Target {
	return Target{str(m.get("state")), str(m.get("stage")), str(m.get("reason"))}
}

func counters(m *omap) []Counter {
	var out []Counter
	for _, k := range m.keys {
		cp, ok := asMap(m.get(k), "counter").get("cap").(int)
		if !ok {
			fail("counter %s: cap is not an integer", k)
		}
		out = append(out, Counter{k, cp})
	}
	return out
}

// Parse parses state-machine.yaml into a Spec.
func Parse(stateMachineYAML []byte) (spec *Spec, err error) {
	sm, err := parseDoc(stateMachineYAML)
	if err != nil {
		return nil, fmt.Errorf("state-machine.yaml: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			spec = nil
			if se, ok := p.(specError); ok {
				err = fmt.Errorf("%s", string(se))
			} else {
				err = fmt.Errorf("malformed spec: %v", p)
			}
		}
	}()
	c := newChecker(sm)
	c.buildConfigs()
	c.loadTables()
	rh, vals := c.holdTable()
	s := &Spec{chk: c, Events: c.events, HoldValues: vals,
		States: strList(c.d.get("states")), Terminal: strList(c.d.get("terminal"))}
	for _, cf := range c.configs {
		s.Configs = append(s.Configs, Config{cf.state, cf.stage, cf.typ, cf.reason})
	}
	for _, ty := range c.types {
		s.Types = append(s.Types, TypeDef{ty, c.stageOf[ty], c.runningStages(ty), c.firstStage(ty)})
	}
	wr := asMap(c.d.get("waiting_reasons"), "waiting_reasons")
	for _, k := range wr.keys {
		sp := asMap(wr.get(k), "waiting reason")
		w := WaitingReason{Name: k, Types: c.expandTypes(sp.get("types"))}
		if st, ok := sp.get("stages").(string); !ok || st != "all" {
			w.Stages = uniq(strList(sp.get("stages")))
		}
		s.WaitingReasons = append(s.WaitingReasons, w)
	}
	s.Counters = counters(asMap(c.d.get("counters"), "counters"))
	s.ReqCounters = counters(asMap(c.d.get("req_counters"), "req_counters"))
	ge := asMap(c.d.get("guard_enums"), "guard_enums")
	for _, k := range ge.keys {
		s.GuardEnums = append(s.GuardEnums, Enum{k, strList(ge.get(k))})
	}
	for _, r := range c.rows {
		row := Row{ID: r.id, Event: r.event, States: strList(r.from.get("state")),
			Reason: str(r.from.get("reason")), Types: c.expandTypes(r.from.get("types")),
			Guard: toGuards(r.guard), To: toTarget(r.to), Effects: r.effects}
		if st, ok := r.from.get("stages").(string); (!ok || st != "all") && r.from.get("stages") != nil {
			row.Stages = strList(r.from.get("stages"))
		}
		s.Rows = append(s.Rows, row)
	}
	for _, e := range c.esc {
		s.Escalate = append(s.Escalate, EscRow{toGuards(guardKVs(e.get("guard"))),
			toTarget(asMap(e.get("to"), "escalate to")), listOrEmpty(e.get("effects"))})
	}
	for _, ig := range c.ignored {
		x := Ignore{Rule: str(ig.get("rule"))}
		if ig.get("states") != nil {
			x.States = strList(ig.get("states"))
		}
		if e, ok := ig.get("events").(string); ok && e == "all" {
			x.AllEvents = true
		} else if ig.get("events") != nil {
			x.Events = strList(ig.get("events"))
		}
		s.Ignored = append(s.Ignored, x)
	}
	for _, r := range c.hrows {
		s.HoldRows = append(s.HoldRows, HoldRow{r.id, r.event, r.from, toGuards(r.guard), str(r.to), r.effects})
	}
	for _, e := range maps(rh.get("rescue_or_owner"), "rescue_or_owner") {
		s.HoldEscalate = append(s.HoldEscalate, HoldEsc{toGuards(guardKVs(e.get("guard"))),
			str(e.get("to")), listOrEmpty(e.get("effects"))})
	}
	if rh.get("ignored") != nil {
		for _, i := range maps(rh.get("ignored"), "req_hold.ignored") {
			s.HoldIgnored = append(s.HoldIgnored, HoldIgnore{strList(i.get("from")), strList(i.get("events"))})
		}
	}
	return s, nil
}

// DimValues is smcheck's value set for a step-table guard dimension.
func (s *Spec) DimValues(dim string) []string { return s.chk.dimValues(dim) }

// HoldDimValues is smcheck's value set for a req_hold guard dimension.
func (s *Spec) HoldDimValues(dim string) []string { return s.chk.holdEnum(dim) }

func assignMatch(g []Guard, assign map[string]string) bool {
	for _, d := range g {
		if assign[d.Dim] != d.Val {
			return false
		}
	}
	return true
}

// StepOracle answers what smcheck's expansion says for (cf, ev, assign): the
// id of the first expanded row that matches ("<id>+esc" for escalate
// expansions), or ignored=true when no row matches the config and event and
// the combination is ignored. id=="" && !ignored is a gap.
func (s *Spec) StepOracle(cf Config, ev string, assign map[string]string) (id string, ignored bool) {
	c := s.chk
	conf := config{cf.State, cf.Stage, cf.Type, cf.Reason}
	matched := false
	for _, r := range c.xrows {
		if r.event != ev || !c.rowMatches(r, conf) {
			continue
		}
		matched = true
		if dimsMatch(guardDims(r.guard), assign) {
			return r.id, false
		}
	}
	if matched {
		return "", false
	}
	return "", c.isIgnored(conf, ev)
}

// ReqOracle is the table B counterpart: first row whose from/event/guards
// match, else the ignored verdict.
func (s *Spec) ReqOracle(hold, ev string, assign map[string]string) (id string, ignored bool) {
	matched := false
	for _, r := range s.HoldRows {
		if r.Event != ev || !slices.Contains(r.From, hold) {
			continue
		}
		matched = true
		if assignMatch(r.Guard, assign) {
			return r.ID, false
		}
	}
	if matched {
		return "", false
	}
	for _, i := range s.HoldIgnored {
		if slices.Contains(i.From, hold) && slices.Contains(i.Events, ev) {
			return "", true
		}
	}
	return "", false
}
