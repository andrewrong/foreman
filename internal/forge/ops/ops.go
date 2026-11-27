// Package ops is the fixed set of GitHub operations foreman may perform
// (forge.yaml FG-6, OP-01..OP-14). Each operation declares the resources it
// consumes, acquires a lease from the gate before the call (FG-4) and settles
// the real consumption afterwards. Merge, push and PR traffic is Essential
// (resources.yaml reserved_for).
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Repo names a repository.
type Repo struct{ Owner, Name string }

// String renders owner/name.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

func (r Repo) path(format string, a ...any) string {
	return "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + fmt.Sprintf(format, a...)
}

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
	opJobLogs        = spec{id: "OP-10"}
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

// run acquires a lease, sends the request and settles consumption.
func (o *Ops) run(ctx context.Context, call Call, s spec, req client.Request) (client.Response, error) {
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
		return client.Response{}, err
	}
	if !dec.Allowed() {
		return client.Response{}, Waiting{Wait: dec.Wait}
	}
	req.Resource, req.Write, req.Lease = s.resource(), s.write, dec.Lease
	resp, err := o.c.Do(ctx, req)

	var actual []gate.Item
	if resp.Status > 0 { // the server answered, so GitHub counted the call
		switch {
		case s.graphql:
			actual = append(actual, gate.Item{Resource: client.ResGraphQL, Amount: graphqlCost(resp.Body)})
		case !resp.NotModified:
			actual = append(actual, gate.Item{Resource: client.ResREST, Amount: 1})
		}
		if s.write {
			actual = append(actual, gate.Item{Resource: client.ResWrite, Amount: 1})
		}
	}
	if _, serr := dec.Lease.Settle(ctx, actual); serr != nil && err == nil {
		err = serr
	}
	return resp, err
}

func (o *Ops) getJSON(ctx context.Context, call Call, s spec, path string, q url.Values, dst any) (client.Response, error) {
	resp, err := o.run(ctx, call, s, client.Request{Method: "GET", Path: path, Query: q})
	if err != nil {
		return resp, err
	}
	return resp, decode(resp.Body, dst)
}

func (o *Ops) sendJSON(ctx context.Context, call Call, s spec, method, path string, body, dst any) (client.Response, error) {
	resp, err := o.run(ctx, call, s, client.Request{Method: method, Path: path, Body: body})
	if err != nil {
		return resp, err
	}
	if dst == nil {
		return resp, nil
	}
	return resp, decode(resp.Body, dst)
}

func decode(b []byte, dst any) error {
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("forge: decode response: %w", err)
	}
	return nil
}

// escapeRef escapes each segment of a branch name but keeps the slashes.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
