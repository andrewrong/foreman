package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// Errors for misuse.
var (
	ErrNoLease       = errors.New("forge: request needs a gate lease")
	ErrLeaseResource = errors.New("forge: lease does not hold the resource this request consumes")
)

// DefaultSecondaryWait applies when GitHub gives no Retry-After.
const DefaultSecondaryWait = 60 * time.Second

func (c *Client) mapError(res string, write bool, status int, h http.Header, body []byte) error {
	msg := message(body)
	b := Base{Status: status, Message: msg}
	now := c.clk.Now()
	lower := strings.ToLower(msg)
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusForbidden:
		if ra := h.Get("Retry-After"); ra != "" || strings.Contains(lower, "secondary rate limit") || strings.Contains(lower, "abuse") {
			wait := DefaultSecondaryWait
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				wait = time.Duration(n) * time.Second
			}
			c.markExhausted(res, write, now.Add(wait))
			return SecondaryLimited{Base: b, RetryAfter: wait}
		}
		if h.Get("X-RateLimit-Remaining") == "0" || status == http.StatusTooManyRequests {
			reset := now.Add(DefaultSecondaryWait)
			if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
				reset = time.Unix(n, 0)
			}
			target := meterResource(h.Get("X-RateLimit-Resource"), res)
			c.meters.MarkExhausted(target, reset)
			return RateLimited{Base: b, Resource: target, Reset: reset}
		}
		return Unauthorized{b}
	case status == http.StatusUnauthorized:
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

func (c *Client) markExhausted(res string, write bool, until time.Time) {
	c.meters.MarkExhausted(res, until)
	if write {
		c.meters.MarkExhausted(ResWrite, until)
	}
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
