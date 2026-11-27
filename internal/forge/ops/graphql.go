package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/andrewrong/foreman/internal/forge/client"
)

// FG-8: GraphQL is only used where one query replaces three or more REST
// calls. PRStatusBatch is that case: per PR it replaces OP-03 and OP-07, so
// two PRs already replace four calls. Every query carries
// rateLimit { cost remaining } and is settled by the returned cost.

// ErrGraphQLNotWorthIt is returned when a batch is too small to justify GraphQL.
var ErrGraphQLNotWorthIt = errors.New("forge: graphql only for 3+ replaced REST calls (FG-8)")

// PRStatus is one entry of PRStatusBatch.
type PRStatus struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	HeadSHA   string `json:"headRefOid"`
	Mergeable string `json:"mergeable"`
	Commits   struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// CheckState returns the combined check state of the head commit, "" if none.
func (p PRStatus) CheckState() string {
	if len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return p.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

// PRStatusBatch reads state, head sha, mergeability and check rollup of the
// given PR numbers in one GraphQL query.
func (o *Ops) PRStatusBatch(ctx context.Context, call Call, r Repo, numbers []int) (map[int]PRStatus, error) {
	if len(numbers) < 2 {
		return nil, ErrGraphQLNotWorthIt
	}
	var sb strings.Builder
	sb.WriteString("query($owner:String!,$name:String!){repository(owner:$owner,name:$name){")
	for _, n := range numbers {
		fmt.Fprintf(&sb, "p%d:pullRequest(number:%d){number state headRefOid mergeable commits(last:1){nodes{commit{statusCheckRollup{state}}}}}", n, n)
	}
	sb.WriteString("}rateLimit{cost remaining}}")
	data, err := o.graphql(ctx, call, sb.String(), map[string]any{"owner": r.Owner, "name": r.Name})
	if err != nil {
		return nil, err
	}
	var repo map[string]PRStatus
	if err := decode(data["repository"], &repo); err != nil {
		return nil, err
	}
	out := map[int]PRStatus{}
	for _, p := range repo {
		out[p.Number] = p
	}
	return out, nil
}

// graphql sends one query. The lease is settled with the cost from the
// response's rateLimit field.
func (o *Ops) graphql(ctx context.Context, call Call, query string, vars map[string]any) (map[string]json.RawMessage, error) {
	if !strings.Contains(query, "rateLimit") {
		return nil, errors.New("forge: graphql query lacks rateLimit { cost remaining } (FG-8)")
	}
	resp, err := o.sendRaw(ctx, call, opGraphQL, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	var env struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := decode(resp.Body, &env); err != nil {
		return nil, err
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("forge: graphql: %s", env.Errors[0].Message)
	}
	return env.Data, nil
}

func (o *Ops) sendRaw(ctx context.Context, call Call, s spec, method, path string, body any) (client.Response, error) {
	return o.sendJSON(ctx, call, s, method, path, body, nil)
}

// graphqlCost extracts data.rateLimit.cost; at least 1 so a response without
// it is still counted.
func graphqlCost(body []byte) int64 {
	var env struct {
		Data struct {
			RateLimit struct {
				Cost int64 `json:"cost"`
			} `json:"rateLimit"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil || env.Data.RateLimit.Cost < 1 {
		return 1
	}
	return env.Data.RateLimit.Cost
}
