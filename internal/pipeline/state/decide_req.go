package state

import (
	"fmt"
	"slices"
)

// xholdRows is req_hold.table with every to: rescue_or_owner row replaced by
// its expansions (ids get a "+roo" suffix).
var xholdRows = expandHoldRows()

func expandHoldRows() []holdRow {
	var out []holdRow
	for _, r := range holdRows {
		if r.To != "rescue_or_owner" {
			out = append(out, r)
			continue
		}
		for _, e := range holdEscRows {
			x := r
			x.ID = r.ID + "+roo"
			x.Guard = expandGuards(r.Guard, e.Guard)
			x.To = e.To
			x.Effects = append(slices.Clone(r.Effects), e.Effects...)
			out = append(out, x)
		}
	}
	return out
}

func holdIgnored(hold, ev string) bool {
	for _, ig := range holdIgnores {
		if slices.Contains(ig.From, hold) && slices.Contains(ig.Events, ev) {
			return true
		}
	}
	return false
}

// DecideReq computes the requirement hold transition (table B) for one
// event. Semantics mirror Decide: unique matching row, rescue_or_owner
// expanded, ignored (hold, event) pairs yield Ignored=true, anything else is
// an error.
func DecideReq(req ReqView, ev Event) (ReqOutcome, error) {
	hold := req.Hold
	if hold == "" {
		hold = HoldNone
	}
	var cands, hits []*holdRow
	for i := range xholdRows {
		r := &xholdRows[i]
		if r.Event == ev.Name && slices.Contains(r.From, hold) {
			cands = append(cands, r)
			if guardsHold(r.Guard, nil, req, ev) {
				hits = append(hits, r)
			}
		}
	}
	if len(cands) == 0 {
		if holdIgnored(hold, ev.Name) {
			return ReqOutcome{Ignored: true}, nil
		}
		return ReqOutcome{}, fmt.Errorf("state: no req_hold row and not ignored: hold=%s event=%s", hold, ev.Name)
	}
	if len(hits) != 1 {
		return ReqOutcome{}, fmt.Errorf("state: %d req_hold rows match hold=%s event=%s (want exactly 1)",
			len(hits), hold, ev.Name)
	}
	r := hits[0]
	return ReqOutcome{RowID: r.ID, To: r.To, Effects: slices.Clone(r.Effects)}, nil
}
