// Package smcheck is the exhaustive checker for spec/design/state-machine.yaml
// (C-02). state-machine.yaml is decoded once into Spec; the checks, the table
// generator and the exhaustive test all read that model.
package smcheck

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// strs is a YAML scalar or sequence read as a string list.
type strs []string

func (s *strs) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return n.Decode((*[]string)(s))
	}
	*s = strs{n.Value}
	return nil
}

type entry[T any] struct {
	Name string
	V    T
}

// ordered is a YAML mapping that keeps its key order.
type ordered[T any] []entry[T]

func (o *ordered[T]) UnmarshalYAML(n *yaml.Node) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		e := entry[T]{Name: n.Content[i].Value}
		if err := n.Content[i+1].Decode(&e.V); err != nil {
			return err
		}
		*o = append(*o, e)
	}
	return nil
}

// Guard is one flattened guard condition: Dim is "hold", "deps", "lease",
// "tier", "single_step", "budget", "decision", "answer", "counter:<name>" or
// "req_counter:<name>"; Val is the required value.
type Guard struct{ Dim, Val string }

// guards reads a guard mapping; counter and req_counter values are {name, is}.
type guards []Guard

func (g *guards) UnmarshalYAML(n *yaml.Node) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		if v.Kind != yaml.MappingNode {
			*g = append(*g, Guard{k, v.Value})
			continue
		}
		var c struct{ Name, Is string }
		if err := v.Decode(&c); err != nil {
			return err
		}
		*g = append(*g, Guard{k + ":" + c.Name, c.Is})
	}
	return nil
}

// Config is one (state, stage, type, reason) combination smcheck enumerates.
type Config struct{ State, Stage, Type, Reason string }

func (c Config) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", c.State, c.Stage, c.Type, c.Reason)
}

// Target is a row target as written (Stage may be same, first_stage or
// resume_stage; State may be the pseudo-target escalate).
type Target struct{ State, Stage, Reason string }

func (t Target) String() string { return fmt.Sprintf("%s/%s/%s", t.State, t.Stage, t.Reason) }

// Row is one step_table row. Stages is nil when the row applies to all stages;
// Reason is "" when the row does not constrain it; Types is group-expanded.
type Row struct {
	ID, Event string
	States    strs
	Reason    string
	Types     strs
	Stages    strs
	Guard     guards
	To        Target
	Effects   []string
}

func (r *Row) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		ID, Event string
		From      struct {
			State, Types, Stages strs
			Reason               string
		}
		Guard   guards
		To      Target
		Effects []string
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	f := raw.From
	*r = Row{raw.ID, raw.Event, f.State, f.Reason, f.Types, f.Stages, raw.Guard, raw.To, raw.Effects}
	return nil
}

// EscRow is one expansion of the escalate pseudo-target.
type EscRow struct {
	Guard   guards
	To      Target
	Effects []string
}

// Ignore is one ignored entry. AllEvents means events: all; Rule is set for
// the rule-based entries (waiting_foreign_exit, running_foreign_result).
type Ignore struct {
	States    strs
	Events    strs
	AllEvents bool `yaml:"-"`
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
	Types  strs
	Stages strs
}

// Counter is a counter with its cap and the events that reset it.
type Counter struct {
	Name  string
	Cap   int
	Reset []string
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
	From      strs
	Guard     guards
	To        string
	Effects   []string
}

// HoldEsc is one expansion of rescue_or_owner.
type HoldEsc struct {
	Guard   guards
	To      string
	Effects []string
}

// HoldIgnore is one req_hold.ignored entry.
type HoldIgnore struct{ From, Events strs }

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

	groups        map[string][]string
	xrows         []Row // Rows with the escalate pseudo-target expanded
	exitByReason  map[string][]string
	runningEvents []string
}

type rawDoc struct {
	Dims struct {
		States, Terminal []string
		Types            ordered[struct{ Stages []string }]
		TypeGroups       ordered[[]string]                     `yaml:"type_groups"`
		FirstStage       map[string]string                     `yaml:"first_stage"`
		RunningStages    map[string][]string                   `yaml:"running_stages"`
		WaitingReasons   ordered[struct{ Types, Stages strs }] `yaml:"waiting_reasons"`
		Counters         ordered[struct {
			Cap   int
			Reset []string
		}]
		GuardEnums  ordered[[]string]          `yaml:"guard_enums"`
		ReqCounters ordered[struct{ Cap int }] `yaml:"req_counters"`
	}
	StepTable []Row `yaml:"step_table"`
	Escalate  []EscRow
	Ignored   []Ignore
	ReqHold   struct {
		Values        []string
		Table         []HoldRow
		Ignored       []HoldIgnore
		RescueOrOwner []HoldEsc `yaml:"rescue_or_owner"`
	} `yaml:"req_hold"`
}

func uniq[T comparable](in []T) []T {
	var out []T
	for _, x := range in {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// allOrNil maps the "all" wildcard to nil.
func allOrNil(l strs) strs {
	if len(l) == 1 && l[0] == "all" {
		return nil
	}
	return l
}

func (s *Spec) expand(ts strs) strs {
	var out strs
	for _, t := range ts {
		if g, ok := s.groups[t]; ok {
			out = append(out, g...)
		} else {
			out = append(out, t)
		}
	}
	return out
}

// Parse decodes state-machine.yaml into a Spec, expands type groups and the
// escalate pseudo-target, and rejects guard dimensions without a value set.
func Parse(data []byte) (*Spec, error) {
	var d rawDoc
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("state-machine.yaml: %w", err)
	}
	s := &Spec{States: d.Dims.States, Terminal: d.Dims.Terminal, Rows: d.StepTable, Escalate: d.Escalate,
		Ignored: d.Ignored, HoldValues: d.ReqHold.Values, HoldRows: d.ReqHold.Table,
		HoldEscalate: d.ReqHold.RescueOrOwner, HoldIgnored: d.ReqHold.Ignored,
		groups: map[string][]string{}, exitByReason: map[string][]string{}}
	var names []string
	for _, t := range d.Dims.Types {
		names = append(names, t.Name)
	}
	for _, g := range d.Dims.TypeGroups {
		s.groups[g.Name] = g.V
	}
	s.groups["all"] = names
	for _, t := range d.Dims.Types {
		grp := "integrate"
		if slices.Contains(s.groups["work"], t.Name) {
			grp = "work"
		}
		s.Types = append(s.Types, TypeDef{t.Name, t.V.Stages, d.Dims.RunningStages[grp], d.Dims.FirstStage[grp]})
	}
	for _, w := range d.Dims.WaitingReasons {
		s.WaitingReasons = append(s.WaitingReasons, WaitingReason{w.Name, s.expand(w.V.Types), allOrNil(uniq(w.V.Stages))})
	}
	for _, c := range d.Dims.Counters {
		s.Counters = append(s.Counters, Counter{c.Name, c.V.Cap, c.V.Reset})
	}
	for _, c := range d.Dims.ReqCounters {
		s.ReqCounters = append(s.ReqCounters, Counter{Name: c.Name, Cap: c.V.Cap})
	}
	for _, e := range d.Dims.GuardEnums {
		s.GuardEnums = append(s.GuardEnums, Enum{e.Name, e.V})
	}
	s.normalize()
	if err := s.checkDims(); err != nil {
		return nil, err
	}
	s.buildConfigs()
	return s, nil
}

func (s *Spec) checkDims() error {
	bad := func(g guards, values func(string) []string) error {
		for _, d := range g {
			if values(d.Dim) == nil {
				return fmt.Errorf("guard dim without declared enum: %s", d.Dim)
			}
		}
		return nil
	}
	var errs []error
	for _, r := range s.Rows {
		errs = append(errs, bad(r.Guard, s.DimValues))
	}
	for _, e := range s.Escalate {
		errs = append(errs, bad(e.Guard, s.DimValues))
	}
	for _, r := range s.HoldRows {
		errs = append(errs, bad(r.Guard, s.HoldDimValues))
	}
	for _, e := range s.HoldEscalate {
		errs = append(errs, bad(e.Guard, s.HoldDimValues))
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// normalize expands types and wildcards and derives events, the escalate
// expansion and the lookups the ignore rules need.
func (s *Spec) normalize() {
	for i := range s.Rows {
		r := &s.Rows[i]
		r.Types, r.Stages = s.expand(r.Types), allOrNil(r.Stages)
		s.Events = append(s.Events, r.Event)
		if r.Reason != "" {
			s.exitByReason[r.Reason] = append(s.exitByReason[r.Reason], r.Event)
		}
		if slices.Contains(r.States, "running") {
			s.runningEvents = append(s.runningEvents, r.Event)
		}
	}
	for i := range s.Ignored {
		ig := &s.Ignored[i]
		if len(ig.Events) == 1 && ig.Events[0] == "all" {
			ig.AllEvents, ig.Events = true, nil
		}
		s.Events = append(s.Events, ig.Events...)
	}
	s.Events, s.runningEvents = uniq(s.Events), uniq(s.runningEvents)
	for _, r := range s.Rows {
		if r.To.State != "escalate" {
			s.xrows = append(s.xrows, r)
			continue
		}
		for _, e := range s.Escalate {
			g := slices.Clone(r.Guard)
			for _, x := range e.Guard {
				if i := slices.IndexFunc(g, func(y Guard) bool { return y.Dim == x.Dim }); i >= 0 {
					g[i] = x
				} else {
					g = append(g, x)
				}
			}
			x := r
			x.ID, x.Guard, x.To, x.Effects = r.ID+"+esc", g, e.To, append(slices.Clone(r.Effects), e.Effects...)
			s.xrows = append(s.xrows, x)
		}
	}
}

func (s *Spec) buildConfigs() {
	for _, t := range s.Types {
		for _, st := range t.Stages {
			s.Configs = append(s.Configs, Config{"queued", st, t.Name, ""})
		}
		for _, st := range t.RunningStages {
			s.Configs = append(s.Configs, Config{"running", st, t.Name, ""})
		}
		for _, w := range s.WaitingReasons {
			if !slices.Contains(w.Types, t.Name) {
				continue
			}
			stages := t.Stages
			if w.Stages != nil {
				stages = slices.DeleteFunc(slices.Clone(w.Stages), func(st string) bool { return !slices.Contains(t.Stages, st) })
			}
			for _, st := range stages {
				s.Configs = append(s.Configs, Config{"waiting", st, t.Name, w.Name})
			}
		}
		s.Configs = append(s.Configs, Config{"done", "", t.Name, ""}, Config{"dropped", "", t.Name, ""})
	}
}

func (s *Spec) enum(name string) []string {
	for _, e := range s.GuardEnums {
		if e.Name == name {
			return e.Values
		}
	}
	return nil
}

func fixedDims() map[string][]string {
	return map[string][]string{
		"hold": {"set", "none"}, "deps": {"done", "pending"}, "lease": {"ok", "wait"},
		"tier": {"R1", "R2", "R3"}, "single_step": {"one", "many"}, "budget": {"ok", "over"},
	}
}

// DimValues is smcheck's value set for a step-table guard dimension; nil when
// the dimension has none.
func (s *Spec) DimValues(dim string) []string {
	if strings.HasPrefix(dim, "counter:") || strings.HasPrefix(dim, "req_counter:") {
		return []string{"below", "at_cap"}
	}
	if dim == "decision" {
		return s.enum("step_decision")
	}
	return fixedDims()[dim]
}

// HoldDimValues is smcheck's value set for a req_hold guard dimension.
func (s *Spec) HoldDimValues(dim string) []string {
	switch dim {
	case "answer":
		return s.enum("answer")
	case "decision":
		return s.enum("req_decision")
	}
	return s.DimValues(dim)
}

func matches(g []Guard, assign map[string]string) bool {
	for _, d := range g {
		if assign[d.Dim] != d.Val {
			return false
		}
	}
	return true
}

// rowMatches reports whether the row's from-part covers the config.
func rowMatches(r *Row, cf Config) bool {
	return slices.Contains(r.States, cf.State) && (r.Reason == "" || r.Reason == cf.Reason) &&
		slices.Contains(r.Types, cf.Type) && (cf.Stage == "" || r.Stages == nil || slices.Contains(r.Stages, cf.Stage))
}

func (s *Spec) isIgnored(cf Config, ev string) bool {
	for _, ig := range s.Ignored {
		if ig.States != nil {
			if slices.Contains(ig.States, cf.State) && (ig.AllEvents || slices.Contains(ig.Events, ev)) {
				return true
			}
			continue
		}
		switch ig.Rule {
		case "waiting_foreign_exit":
			if cf.State != "waiting" || slices.Contains(s.exitByReason[cf.Reason], ev) {
				continue
			}
			for r, evs := range s.exitByReason {
				if r != cf.Reason && slices.Contains(evs, ev) {
					return true
				}
			}
		case "running_foreign_result":
			if cf.State == "running" && slices.Contains(s.runningEvents, ev) {
				return true
			}
		}
	}
	return false
}

// StepOracle answers what smcheck's expansion says for (cf, ev, assign): the
// id of the first expanded row that matches ("<id>+esc" for escalate
// expansions), or ignored=true when no row matches the config and event and
// the combination is ignored. id=="" && !ignored is a gap.
func (s *Spec) StepOracle(cf Config, ev string, assign map[string]string) (id string, ignored bool) {
	matched := false
	for i := range s.xrows {
		r := &s.xrows[i]
		if r.Event != ev || !rowMatches(r, cf) {
			continue
		}
		matched = true
		if matches(r.Guard, assign) {
			return r.ID, false
		}
	}
	return "", !matched && s.isIgnored(cf, ev)
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
		if matches(r.Guard, assign) {
			return r.ID, false
		}
	}
	if matched {
		return "", false
	}
	return "", slices.ContainsFunc(s.HoldIgnored, func(i HoldIgnore) bool {
		return slices.Contains(i.From, hold) && slices.Contains(i.Events, ev)
	})
}
