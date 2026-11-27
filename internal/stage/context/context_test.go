package context

import (
	"fmt"
	"strings"
	"testing"
)

func sample() Input {
	return Input{
		Role: "coder", Constitution: "C-01 only state writes", Goal: "add Sum",
		Outcome:    []Acc{{"A1", "the system shall add"}},
		Boundaries: []string{"no new deps"},
		StepID:     "r1/S1", StepGoal: "implement Sum", StepAcceptance: []Acc{{"A1", "Sum(1,2)=3"}},
		Others:      []StepLine{{"r1/S2", "integrate", "open PR", "waiting"}},
		Discoveries: []string{"X1 answer: go 1.21"},
		Resources:   []string{"hosts: proxy.golang.org"},
		Files:       []string{"sum.go"},
		Rules:       []string{"gofmt"},
		Headroom:    []string{"root: 12/3000 non-test lines"},
	}
}

func idx(s, sub string) int { return strings.Index(s, sub) }

func TestContextSectionOrderStableFirst(t *testing.T) {
	out := Render(sample())
	order := []string{"## Constitution", "## Language rules", "## Requirement", "## Current step r1/S1",
		"## Other steps", "## Discoveries", "## Available resources", "## Relevant files", "## Package headroom"}
	last := -1
	for _, h := range order {
		i := idx(out, h)
		if i < 0 {
			t.Fatalf("missing %q in\n%s", h, out)
		}
		if i < last {
			t.Fatalf("%q out of order", h)
		}
		last = i
	}
}

func TestContextRendersPreviousFailureTailCut(t *testing.T) {
	in := sample()
	var lines []string
	for i := 1; i <= 200; i++ {
		lines = append(lines, fmt.Sprintf("log line %d", i))
	}
	in.LastFailure = &Failure{FailingTests: []string{"TestSum"}, LogTail: strings.Join(lines, "\n")}
	out := Render(in)
	if !strings.Contains(out, "TestSum") || !strings.Contains(out, "log line 200") {
		t.Fatalf("failure missing:\n%s", out)
	}
	if strings.Contains(out, "log line 120\n") || !strings.Contains(out, "log line 121\n") {
		t.Errorf("tail is not exactly the last %d lines", MaxLogLines)
	}
	if idx(out, "## Previous failure") > idx(out, "## Available resources") {
		t.Error("varying content must come after stable content")
	}
}

func TestContextReviewerOmitsCoderMaterial(t *testing.T) {
	in := sample()
	in.Role = "reviewer"
	in.Diff = "+func Sum(a, b int) int { return a + b }\n"
	in.LastFailure = &Failure{LogTail: "coder-note-leak"}
	in.OpenFindings = []FindingLine{{"F1", "blocking", "a.go:3", "bad", "fixed"}}
	out := Render(in)
	for _, banned := range []string{"X1 answer", "coder-note-leak", "## Other steps", "no new deps", "## Relevant files"} {
		if strings.Contains(out, banned) {
			t.Errorf("reviewer context contains %q", banned)
		}
	}
	for _, want := range []string{"Goal: add Sum", "Sum(1,2)=3", "return a + b", "F1 [blocking]"} {
		if !strings.Contains(out, want) {
			t.Errorf("reviewer context lacks %q", want)
		}
	}
}
