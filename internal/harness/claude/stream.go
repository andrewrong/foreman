package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"iter"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

type rawLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system/init
	SessionID string   `json:"session_id"`
	Model     string   `json:"model"`
	Tools     []string `json:"tools"`

	// assistant
	Message struct {
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`

	// rate_limit_event
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`

	// result
	IsError        bool                `json:"is_error"`
	NumTurns       int                 `json:"num_turns"`
	Result         string              `json:"result"`
	TotalCostUSD   float64             `json:"total_cost_usd"`
	Usage          api.Usage           `json:"usage"`
	ModelUsage     map[string]rawModel `json:"modelUsage"`
	TerminalReason string              `json:"terminal_reason"`
	Denials        []json.RawMessage   `json:"permission_denials"`
}

type rawModel struct {
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD             float64 `json:"costUSD"`
}

// ParseStream decodes claude --output-format stream-json (probe P2: system,
// assistant, rate_limit_event, result; other types are ignored). Undecodable
// lines yield ErrorEvent and parsing continues.
func ParseStream(r io.Reader) iter.Seq[api.Event] {
	return func(yield func(api.Event) bool) {
		br := bufio.NewReader(r)
		for n := 1; ; n++ {
			line, err := br.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				ev, perr := decodeLine(line)
				switch {
				case perr != nil:
					if !yield(api.ErrorEvent{Line: n, Err: perr}) {
						return
					}
				case ev != nil:
					if !yield(ev) {
						return
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(api.ErrorEvent{Line: n, Err: err})
				}
				return
			}
		}
	}
}

func decodeLine(line []byte) (api.Event, error) {
	var l rawLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, err
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return api.InitEvent{SessionID: l.SessionID, Model: l.Model, Tools: l.Tools}, nil
		}
	case "assistant":
		return assistant(l), nil
	case "rate_limit_event":
		info, err := meter.ParseClaudeRateLimitInfo(l.RateLimitInfo)
		if err != nil {
			return nil, err
		}
		return api.RateLimitEvent{Info: info}, nil
	case "result":
		return result(l), nil
	}
	return nil, nil
}

func assistant(l rawLine) api.AssistantEvent {
	ev := api.AssistantEvent{Model: l.Message.Model, ToolCounts: map[string]int{}}
	for _, c := range l.Message.Content {
		switch c.Type {
		case "text":
			ev.Text += c.Text
		case "tool_use":
			ev.ToolCounts[c.Name]++
			if c.Name == "Skill" {
				var in struct {
					Skill string `json:"skill"`
					Name  string `json:"name"`
				}
				_ = json.Unmarshal(c.Input, &in)
				if in.Skill == "" {
					in.Skill = in.Name
				}
				ev.SkillCalls = append(ev.SkillCalls, in.Skill)
			}
		case "server_tool_use":
			if c.Name == "advisor" {
				ev.AdvisorCalls++
			}
		}
	}
	return ev
}

func result(l rawLine) api.ResultEvent {
	ev := api.ResultEvent{
		Subtype: l.Subtype, IsError: l.IsError, Turns: l.NumTurns, Text: l.Result,
		CostUSD: l.TotalCostUSD, Usage: l.Usage, TerminalReason: l.TerminalReason,
		Denials: len(l.Denials), ModelUsage: map[string]api.ModelUsage{},
	}
	for name, m := range l.ModelUsage {
		ev.ModelUsage[name] = api.ModelUsage{
			Usage: api.Usage{
				InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
				CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens,
			},
			CostUSD: m.CostUSD,
		}
	}
	return ev
}
