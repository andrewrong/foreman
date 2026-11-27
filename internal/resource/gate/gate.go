// Package gate is the single admission point for resources (resources.yaml
// RC-3): Acquire(Demand) returns a Lease or a Wait, with the reasons as data.
// harness and forge clients require a *Lease (MD-3).
package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Aliases so callers need one import for the common vocabulary.
type (
	Priority = meter.Priority
	Item     = ledger.Item
)

// Priorities.
const (
	P0 = meter.P0
	P1 = meter.P1
	P2 = meter.P2
	P3 = meter.P3
)

// Caller attributes a Demand (RC-3 demand_fields.caller).
type Caller struct {
	Project, RequirementID, StepID, Stage, Role string
}

// Demand is one admission request. All items must be satisfiable, otherwise
// nothing is held. Model items ("model/<harness>/<account>[/<window>]") are
// checked and reserved against every window reported for that account.
// A merge slot item ("local/slots/merge/<repo>") also checks
// "github/base-health/<repo>" (RES-BASE).
type Demand struct {
	ID       string // optional lease id (e.g. a Run id); generated when empty
	Items    []Item
	Priority Priority
	Caller   Caller
	// Essential marks merge/push/PR traffic that may use the amount
	// reserved_for it (RES-GH-REST, RES-GH-GRAPHQL).
	Essential bool
	// BaseFix marks the merge of the requirement created for the current red
	// base (RES-BASE, D-067): it is the one merge a red base must not block.
	BaseFix bool
}

// Reason is one data-form cause of a Wait (or a Warning on an Allow).
type Reason struct {
	Code     string // below_floor, priority_gate, over_daily_pace, exhausted, red_base, slot_full, ...
	Resource string
	Until    time.Time // zero when unknown
}

// String renders "code:resource" (the Wait.reason format).
func (r Reason) String() string { return r.Code + ":" + r.Resource }

// Wait is the "not now" outcome. Task goes to waiting(reason=quota).
type Wait struct {
	Until   time.Time // latest known time all reasons clear; zero if none known
	Reason  string    // first reason, "code:resource"
	Reasons []Reason
}

// Decision is the result of Acquire: exactly one of Lease and Wait is set.
type Decision struct {
	Lease    *Lease
	Wait     *Wait
	Warnings []Reason // below_warn, estimated, no_reading, unmeasured (P0)
}

// Allowed reports whether a Lease was granted.
func (d Decision) Allowed() bool { return d.Lease != nil }

// Recorder receives settled consumption for hourly history (RC-4).
// *history.History satisfies it.
type Recorder interface {
	Record(ctx context.Context, at time.Time, resource, stage, project string, consumed, capacity int64) error
}

// Options configures a Gate.
type Options struct {
	Clock    clock.Clock
	Config   meter.Config
	Meters   *meter.Store
	Ledger   *ledger.Ledger
	Recorder Recorder // optional
}

// Errors.
var (
	ErrInvalidDemand = errors.New("gate: invalid demand")
	ErrClosed        = errors.New("gate: lease already settled or released")
)

const settleRetention = 25 * time.Hour

// Gate admits Demands. Safe for concurrent use.
type Gate struct {
	o    Options
	mu   sync.Mutex // serialises check-and-reserve
	seq  int
	pace map[string]paceState
}

// New returns a Gate.
func New(o Options) *Gate {
	return &Gate{o: o, pace: map[string]paceState{}}
}

type unit struct {
	res    string
	amount int64
	kind   meter.Kind
}

// Acquire checks the Demand against every involved resource and, if all are
// satisfied, reserves them atomically and returns a Lease. It returns an error
// only for an invalid Demand or a duplicate lease id; shortage is a Wait.
func (g *Gate) Acquire(ctx context.Context, d Demand) (Decision, error) {
	if len(d.Items) == 0 || !d.Priority.Valid() {
		return Decision{}, fmt.Errorf("%w: need items and priority P0..P3", ErrInvalidDemand)
	}
	for _, it := range d.Items {
		if it.Amount <= 0 || it.Resource == "" {
			return Decision{}, fmt.Errorf("%w: bad item %+v", ErrInvalidDemand, it)
		}
	}
	units, unknown := g.expand(d)
	if len(unknown) > 0 {
		rs := make([]Reason, len(unknown))
		for i, r := range unknown {
			rs[i] = Reason{Code: "unknown_resource", Resource: r}
		}
		return Decision{Wait: mkWait(rs)}, nil
	}
	g.refreshStale(ctx, units)

	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.o.Clock.Now()
	g.o.Ledger.Prune(now.Add(-settleRetention))
	var reasons, warns []Reason
	for _, u := range units {
		rs, ws := g.eval(u, d, now)
		reasons = append(reasons, rs...)
		warns = append(warns, ws...)
	}
	if len(reasons) > 0 {
		return Decision{Wait: mkWait(reasons)}, nil
	}
	id := d.ID
	if id == "" {
		g.seq++
		id = fmt.Sprintf("lease-%d", g.seq)
	}
	var held []Item
	for _, u := range units {
		if !u.kind.Binary {
			held = append(held, Item{Resource: u.res, Amount: u.amount})
		}
	}
	if err := g.o.Ledger.Reserve(ledger.Reservation{ID: id, Items: held, Tag: d.Caller.Project, At: now}); err != nil {
		return Decision{}, err
	}
	return Decision{Lease: &Lease{g: g, id: id, items: held, caller: d.Caller}, Warnings: warns}, nil
}

// Adopt re-creates a Lease for a reservation recovered at foremand start from
// a running Run's reserved amounts (RT-5). Use it instead of Ledger.Rebuild
// when recovering run by run.
func (g *Gate) Adopt(r ledger.Reservation, caller Caller) (*Lease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.o.Ledger.Reserve(r); err != nil {
		return nil, err
	}
	return &Lease{g: g, id: r.ID, items: append([]Item(nil), r.Items...), caller: caller}, nil
}

// expand turns demand items into per-resource units: model items fan out to
// all known windows of the account, merge slots pull in base health.
func (g *Gate) expand(d Demand) (units []unit, unknown []string) {
	amounts := map[string]int64{}
	var order []string
	add := func(res string, n int64) {
		if _, ok := amounts[res]; !ok {
			order = append(order, res)
		}
		amounts[res] += n
	}
	for _, it := range d.Items {
		if h, a, w, ok := meter.ModelParts(it.Resource); ok {
			for _, win := range g.o.Meters.Windows(h, a) {
				add("model/"+h+"/"+a+"/"+win, it.Amount)
			}
			if w != "" {
				if _, seen := amounts[it.Resource]; !seen {
					add(it.Resource, it.Amount)
				}
			}
			continue
		}
		add(it.Resource, it.Amount)
		if repo, ok := strings.CutPrefix(it.Resource, "local/slots/merge/"); ok {
			if _, seen := amounts["github/base-health/"+repo]; !seen {
				add("github/base-health/"+repo, 1)
			}
		}
	}
	for _, res := range order {
		k, ok := g.o.Config.Resolve(res)
		if !ok {
			unknown = append(unknown, res)
			continue
		}
		units = append(units, unit{res, amounts[res], k})
	}
	return units, unknown
}

// refreshStale re-measures actively measurable resources whose reading is
// missing or older than StaleAfter (RT-7). Failures are ignored here; eval
// then treats the resource as unmeasured.
func (g *Gate) refreshStale(ctx context.Context, units []unit) {
	now := g.o.Clock.Now()
	for _, u := range units {
		if u.kind.Passive || u.kind.SelfCounted {
			continue
		}
		rd, ok := g.o.Meters.Get(u.res)
		if !ok || now.Sub(rd.MeasuredAt) > g.o.Config.StaleAfter {
			_ = g.o.Meters.Refresh(ctx, u.res)
		}
	}
}

func mkWait(rs []Reason) *Wait {
	w := &Wait{Reason: rs[0].String(), Reasons: rs}
	for _, r := range rs {
		if r.Until.After(w.Until) {
			w.Until = r.Until
		}
	}
	return w
}
