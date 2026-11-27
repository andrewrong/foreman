package smcheck

import "slices"

// hrow is one row of the requirement hold table (table B).
type hrow struct {
	id, event string
	from      []string
	guard     []kv
	to        any
	effects   []string
}

func (r *hrow) fromHas(v string) bool { return slices.Contains(r.from, v) }

func (c *checker) holdTable() (*omap, []string) {
	rh := asMap(c.sm.get("req_hold"), "req_hold")
	if c.hrows == nil {
		for _, m := range maps(rh.get("table"), "req_hold.table") {
			c.hrows = append(c.hrows, &hrow{id: str(m.get("id")), event: str(m.get("event")),
				from: strList(m.get("from")), guard: guardKVs(m.get("guard")), to: m.get("to"),
				effects: listOrEmpty(m.get("effects"))})
		}
	}
	return rh, strList(rh.get("values"))
}

func (c *checker) holdEnum(dim string) []string {
	ge := asMap(c.d.get("guard_enums"), "guard_enums")
	switch dim {
	case "answer":
		return strList(ge.get("answer"))
	case "decision":
		return strList(ge.get("req_decision"))
	}
	return c.dimValues(dim)
}

func (c *checker) reqHold() {
	_, vals := c.holdTable()
	var events []string
	for _, r := range c.hrows {
		events = append(events, r.event)
	}
	events = uniq(events)
	for _, v := range vals {
		for _, ev := range events {
			var m []*hrow
			for _, r := range c.hrows {
				if r.fromHas(v) && r.event == ev {
					m = append(m, r)
				}
			}
			if len(m) == 0 {
				continue
			}
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
				space[i] = c.holdEnum(d)
			}
			for _, combo := range product(space) {
				assign := map[string]string{}
				for i, d := range dims {
					assign[d] = combo[i]
				}
				covered := false
				for _, rd := range rds {
					if dimsMatch(rd, assign) {
						covered = true
						break
					}
				}
				if !covered {
					c.errorf("req guard gap: %s %s %s", v, ev, assignInspect(dims, combo))
				}
			}
		}
		if v == "none" {
			continue
		}
		exit := false
		for _, r := range c.hrows {
			if r.fromHas(v) && str(r.to) != v {
				exit = true
			}
		}
		if !exit {
			c.errorf("req hold no exit: %s", v)
		}
	}
	for _, ev := range []string{"replan_trigger", "rescue_trigger", "owner_trigger"} {
		for _, v := range vals {
			if v == "owner" || v == "parked" {
				continue
			}
			has := false
			for _, r := range c.hrows {
				if r.fromHas(v) && r.event == ev {
					has = true
				}
			}
			if !has && !(v == "rescue" && ev == "replan_trigger") {
				c.errorf("req trigger unhandled: %s %s (precedence note covers only lower-priority)", v, ev)
			}
		}
	}
}

func (c *checker) reqCoverage() {
	rh, vals := c.holdTable()
	var ign []*omap
	if rh.get("ignored") != nil {
		ign = maps(rh.get("ignored"), "req_hold.ignored")
	}
	var events []string
	for _, r := range c.hrows {
		events = append(events, r.event)
	}
	for _, i := range ign {
		events = append(events, strList(i.get("events"))...)
	}
	events = uniq(events)
	for _, v := range vals {
		for _, ev := range events {
			if slices.ContainsFunc(c.hrows, func(r *hrow) bool { return r.fromHas(v) && r.event == ev }) {
				continue
			}
			if slices.ContainsFunc(ign, func(i *omap) bool {
				return slices.Contains(strList(i.get("from")), v) && slices.Contains(strList(i.get("events")), ev)
			}) {
				continue
			}
			c.errorf("req uncovered: hold=%s event=%s", v, ev)
		}
	}
}

func (c *checker) reqIgnoredOverlap() {
	rh, _ := c.holdTable()
	var ign []*omap
	if rh.get("ignored") != nil {
		ign = maps(rh.get("ignored"), "req_hold.ignored")
	}
	for _, i := range ign {
		for _, v := range strList(i.get("from")) {
			for _, ev := range strList(i.get("events")) {
				for _, r := range c.hrows {
					if r.fromHas(v) && r.event == ev {
						c.errorf("req ignored overlaps row: %s hold=%s event=%s", r.id, v, ev)
						break
					}
				}
			}
		}
	}
}
