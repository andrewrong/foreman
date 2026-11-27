// Package client is the only holder of the GitHub token (forge.yaml FG-1). It
// sends requests over net/http, requires a gate lease for each (FG-4), feeds
// X-RateLimit-* headers to the meter (FG-2), applies ETag conditional GETs
// through an optional Cache (FG-3) and maps failures to typed errors (FG-5).
// Only internal/forge/ops builds Requests, so callers cannot reach arbitrary
// API paths (FG-6).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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
	apiVersion   = "2022-11-28"
	maxBodyRead  = 32 << 20
	defaultAPI   = "https://api.github.com"
	defaultAgent = "foreman"
)

// Cache is the ETag store (implemented by internal/forge/cache). Errors from
// it never fail a request: the response is simply not cached.
type Cache interface {
	Lookup(ctx context.Context, url string) (etag string, body []byte, ok bool, err error)
	Store(ctx context.Context, url, etag string, body []byte) error
	Touch(ctx context.Context, url string) error
}

// maxCacheBody mirrors cache.MaxBodyBytes (FG-3); bodies above it are not
// even offered to the cache.
const maxCacheBody = 256 << 10

// Config builds a Client.
type Config struct {
	BaseURL string // default https://api.github.com
	Token   string // passed in by the caller (secrets module); never logged
	HTTP    *http.Client
	Meters  *meter.Store
	Clock   clock.Clock
	Cache   Cache // optional
}

// Client performs requests. Safe for concurrent use.
type Client struct {
	base   string
	token  string
	hc     *http.Client
	meters *meter.Store
	clk    clock.Clock
	cache  Cache
}

// New returns a Client.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultAPI
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: base, token: cfg.Token, hc: hc, meters: cfg.Meters, clk: cfg.Clock, cache: cfg.Cache}
}

// Request is one API call.
type Request struct {
	Method string
	Path   string // begins with "/"
	Query  url.Values
	Body   any // JSON-encoded when non-nil
	Accept string

	// Resource is the meter resource consumed: ResREST or ResGraphQL. Empty
	// only for OP-11 (GET /rate_limit, not counted), which needs no lease.
	Resource string
	// Write marks content-creating calls that also consume ResWrite.
	Write bool
	// Lease must hold Resource (and ResWrite when Write is set).
	Lease *gate.Lease
	// NoCache disables conditional GET (OP-10 logs).
	NoCache bool
}

// Response is the result of Do. On error, Status is still set when the server
// answered, so the caller can settle consumption.
type Response struct {
	Status      int
	Header      http.Header
	Body        []byte
	NotModified bool // served from the ETag cache after a 304 (not counted)
}

// Do sends the request. It returns a typed error for non-2xx statuses.
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	if err := checkLease(r); err != nil {
		return Response{}, err
	}
	var rdr io.Reader
	if r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			return Response{}, err
		}
		rdr = bytes.NewReader(b)
	}
	full := c.base + r.Path
	if len(r.Query) > 0 {
		full += "?" + r.Query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, full, rdr)
	if err != nil {
		return Response{}, err
	}
	accept := r.Accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", defaultAgent)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cacheable := c.cache != nil && r.Method == http.MethodGet && !r.NoCache
	var cachedBody []byte
	var haveCached bool
	if cacheable {
		if etag, body, ok, err := c.cache.Lookup(ctx, full); err == nil && ok {
			req.Header.Set("If-None-Match", etag)
			cachedBody, haveCached = body, true
		}
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// The token is never part of the URL, so the error text is safe.
		return Response{}, Transient{Base: Base{Message: err.Error()}, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	out := Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
	if err != nil {
		return out, Transient{Base: Base{Status: resp.StatusCode, Message: err.Error()}, Err: err}
	}
	c.ingest(r, resp.Header)

	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		out.NotModified = true
		out.Status = http.StatusOK
		out.Body = cachedBody
		_ = c.cache.Touch(ctx, full)
		return out, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if cacheable && resp.StatusCode == http.StatusOK {
			if etag := resp.Header.Get("ETag"); etag != "" && len(body) <= maxCacheBody {
				_ = c.cache.Store(ctx, full, etag, body)
			}
		}
		return out, nil
	}
	return out, c.mapError(r.Resource, r.Write, resp.StatusCode, resp.Header, body)
}

func checkLease(r Request) error {
	if r.Resource == "" {
		return nil // OP-11 only
	}
	if r.Lease == nil {
		return ErrNoLease
	}
	held := map[string]bool{}
	for _, it := range r.Lease.Items() {
		held[it.Resource] = true
	}
	if !held[r.Resource] || (r.Write && !held[ResWrite]) {
		return fmt.Errorf("%w: %s", ErrLeaseResource, r.Resource)
	}
	return nil
}

// ingest feeds X-RateLimit-* headers into the meter (FG-2).
func (c *Client) ingest(r Request, h http.Header) {
	l, ok := meter.ParseGitHubHeaders(h)
	if !ok {
		return
	}
	res := meterResource(h.Get("X-RateLimit-Resource"), r.Resource)
	if res != ResREST && res != ResGraphQL {
		return // search, code_search, ... are not tracked resources
	}
	c.meters.IngestGitHub(res, l, c.clk.Now())
}
