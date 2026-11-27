package gate

import (
	"math"
	"time"

	"github.com/andrewrong/foreman/internal/resource/meter"
)

const day = 24 * time.Hour

type paceState struct {
	resetsAt time.Time
	days     int64
	baseline int64 // used + settled + reserved when this day bucket was first seen
}

// eval checks one unit. Reasons block the demand; warnings do not.
func (g *Gate) eval(u unit, d Demand, now time.Time) (reasons, warns []Reason) {
	k := u.kind
	if until, ok := g.o.Meters.ExhaustedUntil(u.res, now); ok {
		return []Reason{{"exhausted", u.res, until}}, nil
	}
	if len(k.Counters) > 0 {
		return g.evalCounters(u, now), nil
	}

	var capacity, used int64
	var rd meter.Reading
	since := time.Time{} // consumption settled after this is not yet in the reading
	if k.SelfCounted {
		capacity = k.DefaultCap
	} else {
		var have bool
		rd, have = g.o.Meters.Get(u.res)
		if !have {
			if k.Passive {
				// the first session produces the first reading
				return nil, []Reason{{"no_reading", u.res, time.Time{}}}
			}
			return unmeasured(u, d, "unmeasured")
		}
		if now.Sub(rd.MeasuredAt) > g.o.Config.StaleAfter {
			if !k.Passive {
				return unmeasured(u, d, "stale")
			}
			warns = append(warns, Reason{"estimated", u.res, time.Time{}})
		}
		capacity, used, since = rd.Capacity, rd.Used, rd.MeasuredAt
		if k.Passive && !rd.ResetsAt.IsZero() && !now.Before(rd.ResetsAt) {
			used, since = 0, rd.ResetsAt // window rolled over since the reading
		}
	}

	if k.Binary {
		if capacity < 1 {
			code := "unavailable"
			if k.ID == "RES-BASE" {
				code = "red_base"
			}
			return []Reason{{code, u.res, time.Time{}}}, warns
		}
		return nil, warns
	}

	var settledAfter int64
	if !k.SelfCounted {
		settledAfter, _ = g.o.Ledger.SettledSince(u.res, since)
	}
	reserved := g.o.Ledger.Reserved(u.res)
	avail := capacity - used - reserved - settledAfter
	var until time.Time
	if rd.ResetsAt.After(now) {
		until = rd.ResetsAt
	}

	need := u.amount + k.Floor.Of(capacity)
	if !d.Essential {
		need += k.EssentialReserve
	}
	if avail < need {
		code := "insufficient"
		switch {
		case k.SelfCounted:
			code, until = "slot_full", time.Time{}
		case !k.Floor.None():
			code = "below_floor"
		}
		reasons = append(reasons, Reason{code, u.res, until})
	}
	if k.PriorityGateBelowPct > 0 && avail*100 < capacity*k.PriorityGateBelowPct && d.Priority > k.PriorityGateMax {
		reasons = append(reasons, Reason{"priority_gate", u.res, until})
	}
	if k.Pacing {
		if r, over := g.evalPacing(u, d, rd, capacity, used+settledAfter+reserved, now); over {
			reasons = append(reasons, r)
		}
	}
	if len(reasons) == 0 && !k.Warn.None() && avail-u.amount < k.Warn.Of(capacity) {
		warns = append(warns, Reason{"below_warn", u.res, until})
	}
	return reasons, warns
}

// unmeasured handles a resource that cannot be measured now: only P0 passes.
func unmeasured(u unit, d Demand, code string) ([]Reason, []Reason) {
	r := Reason{code, u.res, time.Time{}}
	if d.Priority == P0 {
		return nil, []Reason{r}
	}
	return []Reason{r}, nil
}

func (g *Gate) evalCounters(u unit, now time.Time) []Reason {
	var out []Reason
	reserved := g.o.Ledger.Reserved(u.res)
	for _, c := range u.kind.Counters {
		used, oldest := g.o.Ledger.SettledSince(u.res, now.Add(-c.Window).Add(time.Nanosecond))
		if used+reserved+u.amount > c.Capacity {
			var until time.Time
			if !oldest.IsZero() {
				until = oldest.Add(c.Window)
			}
			out = append(out, Reason{"counter_full", u.res, until})
		}
	}
	return out
}

// evalPacing spreads the remaining capacity of the pacing window over the
// remaining days (RES-MODEL pacing): today's budget is
// (capacity - amount at the start of today) / remaining days; beyond it only
// P0 passes. The day bucket is anchored to the window's resetsAt, and its
// baseline is the first state this gate sees in the bucket.
func (g *Gate) evalPacing(u unit, d Demand, rd meter.Reading, capacity, cur int64, now time.Time) (Reason, bool) {
	harness, _, win, ok := meter.ModelParts(u.res)
	if !ok || win != g.o.Config.PacingWindowOf(harness) || rd.ResetsAt.IsZero() || !rd.ResetsAt.After(now) {
		return Reason{}, false
	}
	n := int64(math.Ceil(float64(rd.ResetsAt.Sub(now)) / float64(day)))
	st, ok := g.pace[u.res]
	if !ok || !st.resetsAt.Equal(rd.ResetsAt) || st.days != n {
		st = paceState{rd.ResetsAt, n, cur}
		g.pace[u.res] = st
	}
	budget := (capacity - st.baseline) / n
	if d.Priority != P0 && cur-st.baseline+u.amount > budget {
		return Reason{"over_daily_pace", u.res, rd.ResetsAt.Add(-time.Duration(n-1) * day)}, true
	}
	return Reason{}, false
}
