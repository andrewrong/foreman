package smcheck

import (
	"slices"
	"strings"
)

// Multi-step cycles: every strongly connected set of configs must contain an
// inc of a counter that no edge inside the set resets. Waits on external
// clocks (deps, quota, hold) are exempt.

type edge struct {
	to Config
	r  *Row
}

type graph map[Config][]edge

var (
	exemptReasons = []string{"deps", "quota", "hold"}
	// events that are the result of a Run (a counter reset "on any result" happens only on these)
	resultEvents = []string{"run_ok", "check_pass", "check_fail", "review_approve", "review_changes",
		"merge_back_conflict", "merged", "merge_conflict", "spec_conflict", "bad_result", "budget_exceeded",
		"access_request", "l1_pass", "l1_fail"}
)

// tarjan returns the strongly connected components of the nodes listed in
// order; with a non-nil allow only allowed nodes are followed.
func tarjan(order []Config, allow map[Config]bool, adj graph) [][]Config {
	index, low, on := map[Config]int{}, map[Config]int{}, map[Config]bool{}
	var stack []Config
	var out [][]Config
	var visit func(v Config)
	visit = func(v Config) {
		index[v] = len(index)
		low[v] = index[v]
		stack = append(stack, v)
		on[v] = true
		for _, e := range adj[v] {
			w := e.to
			if allow != nil && !allow[w] {
				continue
			}
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var comp []Config
			for w := (Config{}); w != v; {
				w = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp = append(comp, w)
			}
			out = append(out, comp)
		}
	}
	for _, v := range order {
		if _, seen := index[v]; !seen {
			visit(v)
		}
	}
	return out
}

func (c *checker) cycles() {
	edges := graph{}
	var keys []Config
	for _, from := range c.Configs {
		if !active(from.State) {
			continue
		}
		for i := range c.xrows {
			r := &c.xrows[i]
			if !rowMatches(r, from) || slices.Contains(r.Effects, "alert_starved") { // S09: starvation alarm timer, exit is dispatch
				continue
			}
			t, stage := r.To, r.To.Stage
			switch stage {
			case "", "same":
				stage = from.Stage
			case "first_stage":
				stage = c.typeDef(from.Type).FirstStage
			}
			if !active(t.State) || t.Stage == "resume_stage" {
				continue
			}
			if _, ok := edges[from]; !ok {
				keys = append(keys, from)
			}
			edges[from] = append(edges[from], edge{Config{t.State, stage, from.Type, t.Reason}, r})
		}
	}
	for _, comp := range tarjan(keys, nil, edges) {
		c.checkSCC(comp, edges)
	}
}

// resetsOf lists the counters a reset:<a,b> effect clears.
func resetsOf(r *Row) []string {
	var out []string
	for _, e := range r.Effects {
		if names, ok := strings.CutPrefix(e, "reset:"); ok {
			out = append(out, strings.FieldsFunc(names, func(r rune) bool { return r == ',' })...)
		}
	}
	return out
}

// Sound iterative rule: inside a strongly connected set, an edge that
// increments a counter which no edge of that set resets lies only on bounded
// cycles, so drop it and re-examine what remains (the remaining, smaller sets
// may have lost the edges that used to reset other counters).
func (c *checker) checkSCC(comp []Config, adjIn graph) {
	set := map[Config]bool{}
	for _, v := range comp {
		set[v] = true
	}
	var inner []*Row
	for _, v := range comp {
		for _, e := range adjIn[v] {
			if set[e.to] {
				inner = append(inner, e.r)
			}
		}
	}
	if len(inner) == 0 {
		return
	}
	var events, resets []string
	for _, r := range inner {
		events = append(events, r.Event)
		resets = append(resets, resetsOf(r)...)
	}
	anyResult := func(except string) bool {
		return slices.ContainsFunc(events, func(e string) bool { return slices.Contains(resultEvents, e) && e != except })
	}
	resetInside := func(cn string) bool {
		for _, k := range c.Counters {
			if k.Name != cn {
				continue
			}
			return slices.ContainsFunc(k.Reset, func(e string) bool { return slices.Contains(events, e) }) ||
				slices.Contains(resets, cn) ||
				slices.Contains(k.Reset, "any_valid_result") && anyResult("bad_result") ||
				slices.Contains(k.Reset, "any_non_infra_result") && anyResult("")
		}
		return slices.Contains(resets, cn)
	}
	adj, kept := graph{}, 0
	for _, v := range comp {
		for _, e := range adjIn[v] {
			bounding := slices.ContainsFunc(e.r.Effects, func(f string) bool {
				n, ok := incName(f)
				return ok && !resetInside(n)
			})
			if set[e.to] && !bounding {
				adj[v] = append(adj[v], e)
				kept++
			}
		}
	}
	live := map[Config]bool{}
	var liveOrder []Config
	for _, n := range comp {
		if n.State != "waiting" || !slices.Contains(exemptReasons, n.Reason) {
			live[n] = true
			liveOrder = append(liveOrder, n)
		}
	}
	for _, sub := range tarjan(liveOrder, live, adj) {
		in := map[Config]bool{}
		for _, x := range sub {
			in[x] = true
		}
		var loopRows []string
		for _, v := range sub {
			for _, e := range adj[v] {
				if in[e.to] {
					loopRows = append(loopRows, e.r.ID)
				}
			}
		}
		switch loopRows = uniq(loopRows); {
		case len(loopRows) == 0:
		case len(inner) != kept:
			c.checkSCC(sub, adj) // smaller set: re-evaluate resets with only its own edges
		default:
			slices.Sort(loopRows)
			c.errorf("unbounded cycle: %s", strings.Join(loopRows, ","))
		}
	}
}
