// Package client is the only holder of the GitHub token (forge.yaml FG-1). It
// wraps go-github (REST, LIB-1) and githubv4 (GraphQL, LIB-2) over one
// http.RoundTripper chain that requires a gate lease for each request (FG-4),
// feeds X-RateLimit-* headers to the meter (FG-2) and applies ETag conditional
// GETs through an optional Cache (FG-3, LIB-3). Failures are mapped to typed
// errors by MapError (FG-5). Only internal/forge/ops calls the two clients, so
// other code cannot reach arbitrary API paths (FG-6, MD-2).
package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v93/github"
	"github.com/shurcooL/githubv4"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Meter resource names used by forge.
const (
	ResREST    = "github/rest"
	ResGraphQL = "github/graphql"
	ResWrite   = "github/content-create"
)

const (
	defaultAPI   = "https://api.github.com"
	defaultAgent = "foreman"
)

// Config builds a Client.
type Config struct {
	BaseURL string // default https://api.github.com
	Token   string // passed in by the caller (secrets module); never logged
	HTTP    *http.Client
	Meters  *meter.Store
	Clock   clock.Clock
	Cache   Cache // optional
}

// Client holds the REST and GraphQL clients. Safe for concurrent use.
type Client struct {
	rest   *github.Client
	gql    *githubv4.Client
	plain  *http.Client // follows redirects to blob storage, never sends the token
	meters *meter.Store
	clk    clock.Clock
}

// New returns a Client. BaseURL, when set, points both clients at it (the
// GraphQL endpoint is BaseURL + "/graphql"), which the tests use.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultAPI
	}
	var hc http.Client
	if cfg.HTTP != nil {
		hc = *cfg.HTTP
	}
	inner := hc.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	c := &Client{meters: cfg.Meters, clk: cfg.Clock, plain: &http.Client{Transport: inner}}

	var rt http.RoundTripper = &ingestRT{next: inner, c: c}
	if cfg.Cache != nil {
		rt = &etagRT{next: rt, cache: cfg.Cache}
	}
	hc.Transport = &leaseRT{next: rt, token: cfg.Token}

	baseURL := base + "/"
	rest, err := github.NewClient(
		github.WithHTTPClient(&hc),
		github.WithURLs(&baseURL, &baseURL),
		github.WithUserAgent(defaultAgent),
		github.WithDisableRateLimitCheck(), // the gate and meter decide; no wall clock inside go-github
	)
	if err != nil {
		panic("forge: github client: " + err.Error()) // only invalid options fail; ours are fixed
	}
	c.rest = rest
	c.gql = githubv4.NewEnterpriseClient(base+"/graphql", &hc)
	return c
}

// REST returns the go-github client; only internal/forge/ops uses it.
func (c *Client) REST() *github.Client { return c.rest }

// GraphQL returns the githubv4 client; only internal/forge/ops uses it.
func (c *Client) GraphQL() *githubv4.Client { return c.gql }

// Download fetches a redirect target (OP-10 log blob) without the token.
func (c *Client) Download(ctx context.Context, u *url.URL) ([]byte, error) {
	if base, err := url.Parse(c.rest.BaseURL()); err == nil {
		u = base.ResolveReference(u) // a relative Location is relative to the API
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.plain.Do(req)
	if err != nil {
		return nil, Transient{Base: Base{Message: err.Error()}, Err: err}
	}
	defer resp.Body.Close()
	body, err := readAll(resp.Body, maxBodyRead)
	if err != nil {
		return nil, Transient{Base: Base{Status: resp.StatusCode, Message: err.Error()}, Err: err}
	}
	if resp.StatusCode/100 != 2 {
		return nil, Transient{Base: Base{Status: resp.StatusCode, Message: message(body)}}
	}
	return body, nil
}

// Use says what one request consumes. Ops puts it in the request context
// with WithUse; a request without it is refused (FG-4).
type Use struct {
	// Resource is the meter resource consumed: ResREST or ResGraphQL.
	Resource string
	// Write marks content-creating calls that also consume ResWrite.
	Write bool
	// Lease must hold Resource (and ResWrite when Write is set).
	Lease *gate.Lease
	// Free marks OP-11 (GET /rate_limit, not counted): no lease needed.
	Free bool
	// NoCache disables conditional GET (OP-10 logs, OP-11).
	NoCache bool
}

type useKey struct{}

// WithUse returns a context carrying u.
func WithUse(ctx context.Context, u Use) context.Context {
	return context.WithValue(ctx, useKey{}, u)
}

func useOf(ctx context.Context) (Use, bool) {
	u, ok := ctx.Value(useKey{}).(Use)
	return u, ok
}
