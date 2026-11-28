// Package ops is the fixed set of GitHub operations foreman may perform
// (forge.yaml FG-6, OP-01..OP-14). Each operation declares the resources it
// consumes, acquires a lease from the gate before the call (FG-4) and settles
// the real consumption afterwards. Merge, push and PR traffic is Essential
// (resources.yaml reserved_for).
package ops

import (
	"context"
	"net/http"

	"github.com/google/go-github/v93/github"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Repo names a repository.
type Repo struct{ Owner, Name string }

// String renders owner/name.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Call attributes one operation to a priority and caller for the gate.
type Call struct {
	Priority gate.Priority
	Caller   gate.Caller
}

// Waiting is returned when the gate answers Wait: nothing was sent.
type Waiting struct{ Wait *gate.Wait }

func (w Waiting) Error() string { return "forge: gate wait: " + w.Wait.Reason }

// Config builds Ops.
type Config struct {
	Client *client.Client
	Gate   *gate.Gate
	Meters *meter.Store
	Clock  clock.Clock
}

// Ops performs operations. Safe for concurrent use.
type Ops struct {
	c      *client.Client
	g      *gate.Gate
	meters *meter.Store
	clk    clock.Clock
}

// New returns Ops.
func New(cfg Config) *Ops {
	return &Ops{c: cfg.Client, g: cfg.Gate, meters: cfg.Meters, clk: cfg.Clock}
}

// graphqlReserve is what a GraphQL call reserves before its real cost is known.
const graphqlReserve = 10

type spec struct {
	id        string
	essential bool
	write     bool
	graphql   bool
	noCache   bool // OP-10 logs are never cached (FG-3)
}

// Operation declarations (the resource column of the OP table).
var (
	opPRCreate       = spec{id: "OP-01", essential: true, write: true}
	opPRListOpen     = spec{id: "OP-02"}
	opPRGet          = spec{id: "OP-03"}
	opPRMerge        = spec{id: "OP-04", essential: true, write: true}
	opPRUpdateBranch = spec{id: "OP-05", essential: true, write: true}
	opPRComment      = spec{id: "OP-06", write: true}
	opChecksForRef   = spec{id: "OP-07"}
	opRunsList       = spec{id: "OP-08"}
	opRunRerun       = spec{id: "OP-09", write: true}
	opJobLogs        = spec{id: "OP-10", noCache: true}
	opBranchProt     = spec{id: "OP-12"}
	opPRClose        = spec{id: "OP-13", essential: true, write: true}
	opRefDelete      = spec{id: "OP-14", essential: true, write: true}
	opGraphQL        = spec{id: "FG-8", graphql: true}
)

func (s spec) resource() string {
	if s.graphql {
		return client.ResGraphQL
	}
	return client.ResREST
}

// do is the Gate wrapper (LIB-4, FG-4): it acquires a lease before the call,
// runs fn with the lease in the request context, settles the real consumption
// afterwards and maps errors to the typed ones (FG-5). fn performs exactly one
// go-github / githubv4 call. cost is the GraphQL cost read from the response
// (0 for REST); the lease is settled with it, never with the reserve.
func (o *Ops) do(ctx context.Context, call Call, s spec, fn func(ctx context.Context, gh *github.Client) (*github.Response, error)) error {
	return o.run(ctx, call, s, func(ctx context.Context) (*http.Response, int64, error) {
		resp, err := fn(ctx, o.c.REST())
		if resp != nil {
			return resp.Response, 0, err
		}
		return nil, 0, err
	})
}

// run is the shared part of do and the GraphQL path.
func (o *Ops) run(ctx context.Context, call Call, s spec, fn func(ctx context.Context) (resp *http.Response, cost int64, err error)) error {
	amount := int64(1)
	if s.graphql {
		amount = graphqlReserve
	}
	items := []gate.Item{{Resource: s.resource(), Amount: amount}}
	if s.write {
		items = append(items, gate.Item{Resource: client.ResWrite, Amount: 1})
	}
	dec, err := o.g.Acquire(ctx, gate.Demand{Items: items, Priority: call.Priority, Caller: call.Caller, Essential: s.essential})
	if err != nil {
		return err
	}
	if !dec.Allowed() {
		return Waiting{Wait: dec.Wait}
	}
	use := client.Use{Resource: s.resource(), Write: s.write, Lease: dec.Lease, NoCache: s.noCache}
	resp, cost, err := fn(client.WithUse(ctx, use))
	err = o.c.MapError(use, err)

	var actual []gate.Item
	if resp != nil { // the server answered, so GitHub counted the call
		switch {
		case s.graphql:
			if cost < 1 {
				cost = 1
			}
			actual = append(actual, gate.Item{Resource: client.ResGraphQL, Amount: cost})
		case resp.Header.Get(client.CacheHitHeader) == "":
			actual = append(actual, gate.Item{Resource: client.ResREST, Amount: 1})
		}
		if s.write {
			actual = append(actual, gate.Item{Resource: client.ResWrite, Amount: 1})
		}
	}
	if _, serr := dec.Lease.Settle(ctx, actual); serr != nil && err == nil {
		err = serr
	}
	return err
}
