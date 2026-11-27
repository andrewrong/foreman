package api

import (
	"iter"
	"sort"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Event is one typed item of a session's output stream.
type Event interface{ isEvent() }

// Usage is token usage as reported in a result event.
type Usage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
}

// Total is the token count a Run token cap is compared with: fresh input,
// cache writes and output; cache reads are excluded (they are priced and
// quota-weighted far below fresh tokens).
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens + u.CacheCreationTokens }

// Tokens converts to the meter's estimator input.
func (u Usage) Tokens() meter.Tokens {
	return meter.Tokens{Input: u.InputTokens + u.CacheCreationTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens}
}

// ModelUsage is one entry of result.modelUsage.
type ModelUsage struct {
	Usage
	CostUSD float64
}

// InitEvent is system/init.
type InitEvent struct {
	SessionID string
	Model     string
	Tools     []string
}

// AssistantEvent summarises one assistant message.
type AssistantEvent struct {
	Model        string
	Text         string
	ToolCounts   map[string]int // tool_use name -> count
	SkillCalls   []string       // names passed to the Skill tool (D-059)
	AdvisorCalls int            // server_tool_use name=advisor (D-044)
}

// RateLimitEvent carries the windows one rate_limit_event reported.
type RateLimitEvent struct{ Info meter.ClaudeRateLimitInfo }

// ResultEvent is the final result.
type ResultEvent struct {
	Subtype        string
	IsError        bool
	Turns          int
	Text           string
	CostUSD        float64
	Usage          Usage
	ModelUsage     map[string]ModelUsage
	TerminalReason string
	Denials        int
}

// ErrorEvent is a stream line that could not be decoded.
type ErrorEvent struct {
	Line int
	Err  error
}

func (InitEvent) isEvent()      {}
func (AssistantEvent) isEvent() {}
func (RateLimitEvent) isEvent() {}
func (ResultEvent) isEvent()    {}
func (ErrorEvent) isEvent()     {}

// Summary is the fold of a whole stream.
type Summary struct {
	Init           *InitEvent
	AssistantTurns int
	ToolCounts     map[string]int
	SkillCalls     map[string]int
	AdvisorCalls   int
	RateLimits     []RateLimitEvent
	Result         *ResultEvent
	Errors         []ErrorEvent
}

// Collect drains seq into a Summary. sink, if non-nil, receives every
// rate-limit observation as it arrives.
func Collect(seq iter.Seq[Event], sink WindowSink) Summary {
	s := Summary{ToolCounts: map[string]int{}, SkillCalls: map[string]int{}}
	for ev := range seq {
		switch e := ev.(type) {
		case InitEvent:
			s.Init = &e
		case AssistantEvent:
			s.AssistantTurns++
			for k, n := range e.ToolCounts {
				s.ToolCounts[k] += n
			}
			for _, n := range e.SkillCalls {
				s.SkillCalls[n]++
			}
			s.AdvisorCalls += e.AdvisorCalls
		case RateLimitEvent:
			s.RateLimits = append(s.RateLimits, e)
			if sink != nil {
				sink(e.Info)
			}
		case ResultEvent:
			s.Result = &e
		case ErrorEvent:
			s.Errors = append(s.Errors, e)
		}
	}
	return s
}

// SkillNames returns the called skill names, sorted.
func (s Summary) SkillNames() []string {
	var out []string
	for n := range s.SkillCalls {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// WindowSink receives quota-window observations.
type WindowSink func(meter.ClaudeRateLimitInfo)

// MeterSink feeds observations into the resource meter via IngestModel, so
// every reported window is gated (D-051). Windows absent from an event keep
// their last value (meter semantics).
func MeterSink(store *meter.Store, harness, account string, clk clock.Clock) WindowSink {
	return func(info meter.ClaudeRateLimitInfo) {
		var at time.Time = clk.Now()
		store.IngestModel(harness, account, info, at)
	}
}
