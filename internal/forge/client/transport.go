package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Cache is the ETag store (implemented by internal/forge/cache). Errors from
// it never fail a request: the response is simply not cached.
type Cache interface {
	Lookup(ctx context.Context, url string) (etag string, body []byte, ok bool, err error)
	Store(ctx context.Context, url, etag string, body []byte) error
	Touch(ctx context.Context, url string) error
}

const (
	maxBodyRead = 32 << 20
	// maxCacheBody mirrors cache.MaxBodyBytes (FG-3); bodies above it are not
	// even offered to the cache.
	maxCacheBody = 256 << 10
	// CacheHitHeader is set on a response served from the ETag cache after a
	// 304: GitHub does not count it, so ops do not settle it.
	CacheHitHeader = "X-Foreman-Cache"
)

// Errors for misuse.
var (
	ErrNoLease       = errors.New("forge: request needs a gate lease")
	ErrLeaseResource = errors.New("forge: lease does not hold the resource this request consumes")
)

func readAll(r io.Reader, limit int64) ([]byte, error) { return io.ReadAll(io.LimitReader(r, limit)) }

// leaseRT is the outermost layer: it refuses requests without a matching
// lease (FG-4) and is the only place that sets the token (FG-1).
type leaseRT struct {
	next  http.RoundTripper
	token string
}

func (t *leaseRT) RoundTrip(req *http.Request) (*http.Response, error) {
	u, ok := useOf(req.Context())
	if !ok || (!u.Free && u.Lease == nil) {
		return nil, ErrNoLease
	}
	if !u.Free {
		held := map[string]bool{}
		for _, it := range u.Lease.Items() {
			held[it.Resource] = true
		}
		if !held[u.Resource] || (u.Write && !held[ResWrite]) {
			return nil, fmt.Errorf("%w: %s", ErrLeaseResource, u.Resource)
		}
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(req)
}

// ingestRT feeds X-RateLimit-* headers into the meter (FG-2). It sits below
// the ETag layer so 304 answers are ingested too.
type ingestRT struct {
	next http.RoundTripper
	c    *Client
}

func (t *ingestRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if l, ok := meter.ParseGitHubHeaders(resp.Header); ok {
		u, _ := useOf(req.Context())
		res := meterResource(resp.Header.Get("X-RateLimit-Resource"), u.Resource)
		if res == ResREST || res == ResGraphQL { // search, code_search, ... are not tracked
			t.c.meters.IngestGitHub(res, l, t.c.clk.Now())
		}
	}
	return resp, nil
}

// etagRT is the conditional-GET layer (LIB-3, FG-3): it sends If-None-Match
// for a cached URL, answers a 304 from the cache, and stores 200 bodies that
// carry an ETag.
type etagRT struct {
	next  http.RoundTripper
	cache Cache
}

func (t *etagRT) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := useOf(req.Context())
	if req.Method != http.MethodGet || u.NoCache {
		return t.next.RoundTrip(req)
	}
	ctx, key := req.Context(), req.URL.String()
	var cached []byte
	var haveCached bool
	if etag, body, ok, err := t.cache.Lookup(ctx, key); err == nil && ok {
		req = req.Clone(ctx)
		req.Header.Set("If-None-Match", etag)
		cached, haveCached = body, true
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		_ = resp.Body.Close()
		_ = t.cache.Touch(ctx, key)
		h := resp.Header.Clone()
		h.Set(CacheHitHeader, "hit")
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor, ProtoMinor: resp.ProtoMinor,
			Header: h, Body: io.NopCloser(bytes.NewReader(cached)), ContentLength: int64(len(cached)), Request: req,
		}, nil
	case resp.StatusCode == http.StatusOK:
		etag := resp.Header.Get("ETag")
		if etag == "" {
			return resp, nil
		}
		buf, err := readAll(resp.Body, maxCacheBody+1)
		if err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		if len(buf) <= maxCacheBody {
			_ = resp.Body.Close()
			_ = t.cache.Store(ctx, key, etag, buf)
			resp.Body = io.NopCloser(bytes.NewReader(buf))
			return resp, nil
		}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), resp.Body), resp.Body}
	}
	return resp, nil
}
