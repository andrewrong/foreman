package smcheck

import "slices"

// Counter, self-loop and ignore-entry checks.

func (c *checker) counterIncremented() {
	var all []string
	for _, r := range c.xrows {
		all = append(all, r.effects...)
	}
	for _, r := range c.hrows {
		all = append(all, r.effects...)
	}
	for _, e := range maps(asMap(c.sm.get("req_hold"), "req_hold").get("rescue_or_owner"), "rescue_or_owner") {
		all = append(all, listOrEmpty(e.get("effects"))...)
	}
	for _, e := range c.esc {
		all = append(all, listOrEmpty(e.get("effects"))...)
	}
	names := slices.Clone(asMap(c.d.get("counters"), "counters").keys)
	names = append(names, asMap(c.d.get("req_counters"), "req_counters").keys...)
	for _, n := range names {
		if !slices.Contains(all, "inc:"+n) {
			c.errorf("counter never incremented: %s", n)
		}
	}
}

func (c *checker) selfLoops() {
	for _, r := range c.xrows {
		f, t := r.from, r.to
		ts := str(t.get("state"))
		if fs := strList(f.get("state")); len(fs) != 1 || fs[0] != ts || (ts != "waiting" && ts != "running") {
			continue
		}
		stage := str(t.get("stage"))
		if str(t.get("reason")) != str(f.get("reason")) || (stage != "" && stage != "same") {
			continue
		}
		if s, ok := f.get("stages").(string); ts == "running" && !(ok && s == "all") && len(strList(f.get("stages"))) != 1 {
			continue
		}
		if len(incsOf(r.effects)) == 0 {
			c.errorf("unbounded self-loop: %s", r.id)
		}
	}
}

func (c *checker) ignoredOverlap() {
	for _, cf := range c.configs {
		for _, ig := range c.ignored {
			if ig.get("states") == nil || !slices.Contains(strList(ig.get("states")), cf.state) {
				continue
			}
			evs := listOrEmpty(ig.get("events"))
			if s, ok := ig.get("events").(string); ok && s == "all" {
				evs = c.events
			}
			for _, ev := range evs {
				for _, r := range c.xrows {
					if r.event == ev && c.rowMatches(r, cf) {
						c.errorf("ignored overlaps row: %s %s event=%s", r.id, cf, ev)
						break
					}
				}
			}
		}
	}
}
