package meter

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"
)

// ClaudeWindow is one entry of rate_limit_info.unifiedWindows in claude's
// stream-json rate_limit_event (docs/probes/phase-1.md): utilization is a
// 0-1 fraction, ResetsAt unix seconds.
type ClaudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resetsAt"`
	Status      string  `json:"status"`
}

// ClaudeRateLimitInfo is rate_limit_event.rate_limit_info.
type ClaudeRateLimitInfo struct {
	UnifiedWindows map[string]ClaudeWindow `json:"unifiedWindows"`
}

// ParseClaudeRateLimitInfo decodes the rate_limit_info JSON object.
func ParseClaudeRateLimitInfo(raw []byte) (ClaudeRateLimitInfo, error) {
	var info ClaudeRateLimitInfo
	err := json.Unmarshal(raw, &info)
	return info, err
}

// claudeWindowName maps claude's window keys to resource window names. Keys
// not listed are used verbatim so they are gated too (D-051: every reported
// window is checked).
var claudeWindowName = map[string]string{"five_hour": "5h", "seven_day": "7d"}

// IngestClaude records the windows reported by one rate_limit_event for
// model/claude/<account>/<window>. Only reported windows are updated; windows
// absent from the event keep their last value. Returns the resource names
// updated.
func (s *Store) IngestClaude(account string, info ClaudeRateLimitInfo, at time.Time) []string {
	return s.IngestModel("claude", account, info, at)
}

// IngestModel is IngestClaude for any harness that reports the same shape.
func (s *Store) IngestModel(harness, account string, info ClaudeRateLimitInfo, at time.Time) []string {
	var out []string
	for key, w := range info.UnifiedWindows {
		name := key
		if n, ok := claudeWindowName[key]; ok {
			name = n
		}
		res := "model/" + harness + "/" + account + "/" + name
		r := Reading{
			Resource:   res,
			Capacity:   100,
			Used:       int64(math.Round(math.Min(math.Max(w.Utilization, 0), 1) * 100)),
			MeasuredAt: at,
			Source:     harness + ":rate_limit_event",
		}
		if w.ResetsAt > 0 {
			r.ResetsAt = time.Unix(w.ResetsAt, 0)
		}
		s.Observe(r)
		out = append(out, res)
	}
	return out
}

// GitHubLimits are the X-RateLimit-* header values of one response.
type GitHubLimits struct {
	Limit, Remaining int64
	ResetUnix        int64
}

// ParseGitHubHeaders reads X-RateLimit-Limit/Remaining/Reset.
func ParseGitHubHeaders(h http.Header) (GitHubLimits, bool) {
	get := func(k string) (int64, bool) {
		v, err := strconv.ParseInt(h.Get(k), 10, 64)
		return v, err == nil
	}
	l, ok1 := get("X-RateLimit-Limit")
	rem, ok2 := get("X-RateLimit-Remaining")
	reset, ok3 := get("X-RateLimit-Reset")
	return GitHubLimits{l, rem, reset}, ok1 && ok2 && ok3
}

// IngestGitHub records rate-limit values for a github resource such as
// "github/rest" or "github/graphql".
func (s *Store) IngestGitHub(resource string, l GitHubLimits, at time.Time) {
	r := Reading{
		Resource: resource, Capacity: l.Limit, Used: l.Limit - l.Remaining,
		MeasuredAt: at, Source: "github:headers",
	}
	if l.ResetUnix > 0 {
		r.ResetsAt = time.Unix(l.ResetUnix, 0)
	}
	s.Observe(r)
}

// IngestBaseHealth records the base-branch health of a repo (RES-BASE):
// capacity 1 when the latest base run is success or in progress, 0 on failure.
func (s *Store) IngestBaseHealth(repo string, healthy bool, at time.Time) {
	var c int64
	if healthy {
		c = 1
	}
	s.Observe(Reading{Resource: "github/base-health/" + repo, Capacity: c, MeasuredAt: at, Source: "github:base-run"})
}
