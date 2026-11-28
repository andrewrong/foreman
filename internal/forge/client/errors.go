package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// Typed errors (forge.yaml FG-5); match them with errors.As.

// Base carries the HTTP status and GitHub's message.
type Base struct {
	Status  int
	Message string
}

func (b Base) Error() string { return fmt.Sprintf("github: %d %s", b.Status, b.Message) }

// RateLimited is the primary limit being exhausted; Reset is when it clears.
type RateLimited struct {
	Base
	Resource string // meter resource that was marked exhausted
	Reset    time.Time
}

// SecondaryLimited is GitHub's abuse/secondary limit; wait RetryAfter.
type SecondaryLimited struct {
	Base
	RetryAfter time.Duration
}

// NotFound is 404.
type NotFound struct{ Base }

// Conflict is 409.
type Conflict struct{ Base }

// Unprocessable is 422.
type Unprocessable struct{ Base }

// Unauthorized is 401 or a 403 that is not a rate limit.
type Unauthorized struct{ Base }

// Transient is a 5xx or a transport failure; the call may be retried.
type Transient struct {
	Base
	Err error
}

func (t Transient) Unwrap() error { return t.Err }

// APIError is any other non-2xx status (for example 405 on merge).
type APIError struct{ Base }

// DefaultSecondaryWait applies when GitHub gives no Retry-After.
const DefaultSecondaryWait = 60 * time.Second

// MapError converts an error from go-github, githubv4 or the transport chain
// into the typed errors above (FG-5) and marks the meter exhausted for rate
// limits. use is what the failed request consumed. Errors that are not about
// the call itself (lease misuse, decode failures) pass through unchanged.
func (c *Client) MapError(use Use, err error) error {
	if err == nil {
		return nil
	}
	now := c.clk.Now()
	var rl *github.RateLimitError
	var ab *github.AbuseRateLimitError
	var er *github.ErrorResponse
	var ue *url.Error
	switch {
	case errors.As(err, &rl):
		b := Base{Status: statusOf(rl.Response), Message: rl.Message}
		reset := rl.Rate.Reset.Time
		if reset.IsZero() {
			reset = now.Add(DefaultSecondaryWait)
		}
		target := meterResource(headerOf(rl.Response, "X-RateLimit-Resource"), use.Resource)
		c.meters.MarkExhausted(target, reset)
		return RateLimited{Base: b, Resource: target, Reset: reset}
	case errors.As(err, &ab):
		wait := DefaultSecondaryWait
		if ab.RetryAfter != nil {
			wait = *ab.RetryAfter
		}
		c.meters.MarkExhausted(use.Resource, now.Add(wait))
		if use.Write {
			c.meters.MarkExhausted(ResWrite, now.Add(wait))
		}
		return SecondaryLimited{Base: Base{Status: statusOf(ab.Response), Message: ab.Message}, RetryAfter: wait}
	case errors.As(err, &er):
		return c.mapStatus(use, er, now)
	case errors.Is(err, ErrNoLease), errors.Is(err, ErrLeaseResource):
		return err
	case errors.As(err, &ue):
		// The token is never part of the URL, and go-github sanitizes it.
		return Transient{Base: Base{Message: err.Error()}, Err: err}
	}
	return err
}

func (c *Client) mapStatus(use Use, er *github.ErrorResponse, now time.Time) error {
	status := statusOf(er.Response)
	b := Base{Status: status, Message: er.Message}
	switch {
	case status == http.StatusTooManyRequests: // 429 that is neither header-marked nor a secondary limit
		reset := now.Add(DefaultSecondaryWait)
		if n, err := strconv.ParseInt(headerOf(er.Response, "X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
			reset = time.Unix(n, 0)
		}
		target := meterResource(headerOf(er.Response, "X-RateLimit-Resource"), use.Resource)
		c.meters.MarkExhausted(target, reset)
		return RateLimited{Base: b, Resource: target, Reset: reset}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Unauthorized{b}
	case status == http.StatusNotFound:
		return NotFound{b}
	case status == http.StatusConflict:
		return Conflict{b}
	case status == http.StatusUnprocessableEntity:
		return Unprocessable{b}
	case status >= 500:
		return Transient{Base: b}
	}
	return APIError{b}
}

func statusOf(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}

func headerOf(r *http.Response, k string) string {
	if r == nil {
		return ""
	}
	return r.Header.Get(k)
}

func message(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func meterResource(hdr, fallback string) string {
	switch hdr {
	case "core":
		return ResREST
	case "graphql":
		return ResGraphQL
	}
	return fallback
}
