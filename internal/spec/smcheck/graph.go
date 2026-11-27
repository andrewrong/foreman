package smcheck

import (
	"slices"
	"strings"
)

// Multi-step cycles: every strongly connected set of configs must contain an
// inc of a counter that no edge inside the set resets. Waits on external
// clocks (deps, quota, hold) are exempt.

type node struct{ state, stage, typ, reason string }

type edge struct {
	to node
	r  *row
}

type graph map[node][]edge

var (
	exemptReasons = []string{"deps", "quota", "hold"}
	// events that are the result of a Run (a counter reset "on any result" happens only on these)
	resultEvents = []string{"run_ok", "check_pass", "check_fail", "review_approve", "review_changes",
		"merge_back_conflict", "merged", "merge_conflict", "spec_conflict", "bad_result", "budget_exceeded",
		"access_request", "l1_pass", "l1_fail"}
)

// splitColon is ruby `e.split(':')[1]`.
func splitColon(e string) string {
	parts := strings.Split(e, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func resetsOf(r *row) []string {
	var out []string
	for _, e := range r.effects {
		if hasPrefixStr(e, "reset:") {
			for _, n := range strings.Split(strings.SplitN(e, ":", 2)[1], ",") {
				if n != "" {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// tarjan returns the strongly connected components of the nodes listed in
// order; with a non-nil allow only allowed nodes are followed.
func tarjan(order []node, allow map[node]bool, adj graph) [][]node {
	index := map[node]int{}
	low := map[node]int{}
	on := map[node]bool{}
	var stack []node
	var out [][]node
	i := 0
	var visit func(v node)
	visit = func(v node) {
		index[v], low[v] = i, i
		i++
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
			var comp []node
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
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
	var keys []node
	for _, cf := range c.configs {
		if cf.state != "queued" && cf.state != "running" && cf.state != "waiting" {
			continue
		}
		from := node{cf.state, cf.stage, cf.typ, cf.reason}
		for _, r := range c.xrows {
			if !c.rowMatches(r, cf) || slices.Contains(r.effects, "alert_starved") { // S09: starvation alarm timer, exit is dispatch
				continue
			}
			t := r.to
			ts := str(t.get("state"))
			if ts != "queued" && ts != "running" && ts != "waiting" {
				continue
			}
			stage := str(t.get("stage"))
			switch stage {
			case "", "same":
				stage = cf.stage
			case "first_stage":
				stage = c.firstStage(cf.typ)
			case "resume_stage":
				continue
			}
			if _, ok := edges[from]; !ok {
				keys = append(keys, from)
			}
			edges[from] = append(edges[from], edge{node{ts, stage, cf.typ, str(t.get("reason"))}, r})
		}
	}
	for _, comp := range tarjan(keys, nil, edges) {
		c.checkSCC(comp, edges)
	}
}

func (c *checker) counterResets() map[string][]string {
	out := map[string][]string{}
	counters := asMap(c.d.get("counters"), "counters")
	for _, k := range counters.keys {
		out[k] = listOrEmpty(asMap(counters.get(k), "counter").get("reset"))
	}
	return out
}

// Sound iterative rule: inside a strongly connected set, an edge that
// increments a counter which no edge of that set resets lies only on bounded
// cycles, so drop it and re-examine what remains (the remaining, smaller sets
// may have lost the edges that used to reset other counters).
func (c *checker) checkSCC(comp []node, adjIn graph) {
	set := map[node]bool{}
	for _, v := range comp {
		set[v] = true
	}
	var inner []*row
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
	var innerEvents, innerResets []string
	for _, r := range inner {
		innerEvents = append(innerEvents, r.event)
		innerResets = append(innerResets, resetsOf(r)...)
	}
	counterReset := c.counterResets()
	resetInside := func(cn string) bool {
		rs := counterReset[cn]
		for _, e := range rs {
			if slices.Contains(innerEvents, e) {
				return true
			}
		}
		if slices.Contains(innerResets, cn) {
			return true
		}
		if slices.Contains(rs, "any_valid_result") &&
			slices.ContainsFunc(innerEvents, func(e string) bool { return slices.Contains(resultEvents, e) && e != "bad_result" }) {
			return true
		}
		return slices.Contains(rs, "any_non_infra_result") &&
			slices.ContainsFunc(innerEvents, func(e string) bool { return slices.Contains(resultEvents, e) })
	}
	bounding := func(r *row) bool {
		for _, e := range incsOf(r.effects) {
			if !resetInside(splitColon(e)) {
				return true
			}
		}
		return false
	}
	adj := graph{}
	for _, v := range comp {
		for _, e := range adjIn[v] {
			if set[e.to] && !bounding(e.r) {
				adj[v] = append(adj[v], e)
			}
		}
	}
	live := map[node]bool{}
	var liveOrder []node
	for _, n := range comp {
		if n.state == "waiting" && slices.Contains(exemptReasons, n.reason) {
			continue
		}
		live[n] = true
		liveOrder = append(liveOrder, n)
	}
	kept := 0
	for _, v := range comp {
		for _, e := range adj[v] {
			if set[e.to] {
				kept++
			}
		}
	}
	removedAny := len(inner) != kept
	for _, sub := range tarjan(liveOrder, live, adj) {
		s2 := map[node]bool{}
		for _, x := range sub {
			s2[x] = true
		}
		var loopRows []string
		for _, v := range sub {
			for _, e := range adj[v] {
				if s2[e.to] {
					loopRows = append(loopRows, e.r.id)
				}
			}
		}
		loopRows = uniq(loopRows)
		if len(loopRows) == 0 {
			continue
		}
		if removedAny {
			c.checkSCC(sub, adj) // smaller set: re-evaluate resets with only its own edges
		} else {
			slices.Sort(loopRows)
			c.errorf("unbounded cycle: %s", strings.Join(loopRows, ","))
		}
	}
}
