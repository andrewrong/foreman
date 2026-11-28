package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"

	"github.com/shurcooL/githubv4"
)

// FG-8: GraphQL is only used where one query replaces three or more REST
// calls. PRStatusBatch is that case: per PR it replaces OP-03 and OP-07, so
// two PRs already replace four calls. Every query carries
// rateLimit { cost remaining } and is settled by the returned cost.

// ErrGraphQLNotWorthIt is returned when a batch is too small to justify GraphQL.
var ErrGraphQLNotWorthIt = errors.New("forge: graphql only for 3+ replaced REST calls (FG-8)")

// PRStatus is one entry of PRStatusBatch. The graphql tags are the query.
type PRStatus struct {
	Number    int    `graphql:"number"`
	State     string `graphql:"state"`
	HeadSHA   string `graphql:"headRefOid"`
	Mergeable string `graphql:"mergeable"`
	Commits   struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string
				}
			}
		}
	} `graphql:"commits(last: 1)"`
}

// CheckState returns the combined check state of the head commit, "" if none.
func (p PRStatus) CheckState() string {
	if len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return p.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

// rateLimitField is the mandatory cost field of every query (FG-8).
type rateLimitField struct {
	Cost      int
	Remaining int
}

// PRStatusBatch reads state, head sha, mergeability and check rollup of the
// given PR numbers in one GraphQL query.
func (o *Ops) PRStatusBatch(ctx context.Context, call Call, r Repo, numbers []int) (map[int]PRStatus, error) {
	if len(numbers) < 2 {
		return nil, ErrGraphQLNotWorthIt
	}
	// githubv4 builds the query text from struct tags, so the per-PR aliases
	// (p7:pullRequest(number: 7)) need a struct built at run time.
	prType := reflect.TypeFor[PRStatus]()
	fields := make([]reflect.StructField, len(numbers))
	for i, n := range numbers {
		fields[i] = reflect.StructField{
			Name: fmt.Sprintf("P%d", n), Type: prType,
			Tag: reflect.StructTag(fmt.Sprintf(`graphql:"p%d:pullRequest(number: %d)"`, n, n)),
		}
	}
	q := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "Repository", Type: reflect.StructOf(fields), Tag: `graphql:"repository(owner: $owner, name: $name)"`},
		{Name: "RateLimit", Type: reflect.TypeFor[rateLimitField]()},
	}))
	vars := map[string]any{"owner": githubv4.String(r.Owner), "name": githubv4.String(r.Name)}

	err := o.run(ctx, call, opGraphQL, func(ctx context.Context) (*http.Response, int64, error) {
		err := o.c.GraphQL().Query(ctx, q.Interface(), vars)
		var ue *url.Error
		if errors.As(err, &ue) { // never reached the server: nothing was counted
			return nil, 0, err
		}
		cost := int64(q.Elem().FieldByName("RateLimit").FieldByName("Cost").Int())
		return &http.Response{Header: http.Header{}}, cost, err
	})
	if err != nil {
		return nil, err
	}
	repo := q.Elem().FieldByName("Repository")
	out := make(map[int]PRStatus, len(numbers))
	for i := range numbers {
		p := repo.Field(i).Interface().(PRStatus)
		out[p.Number] = p
	}
	return out, nil
}
