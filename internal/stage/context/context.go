// Package context renders context.md, the foremand-to-session input file
// (protocol-run.yaml PF-CTX). Stable content comes first (constitution,
// language rules, spec) and varying content last, so consecutive sessions of
// one requirement hit the prompt cache.
//
// The package is also the one place stage code shared by several stages lives
// (MD-11): the session machinery of the code and review stages (Env, Request,
// Result), the L1 reading used by the integrate and merge stages (ReadL1) and
// the go test output parser used by check and L1.
package context

import (
	"fmt"
	"strings"
)

// MaxLogLines is the longest log tail rendered under "previous failure".
const MaxLogLines = 80

// Acc is one acceptance item.
type Acc struct{ ID, EARS string }

// StepLine is one line about another step of the requirement.
type StepLine struct{ ID, Type, Goal, State string }

// Failure is the open kind=check finding of the previous attempt (ACR-01).
type Failure struct {
	FailingTests []string
	LogTail      string // already redacted by the caller
}

// FindingLine is one open finding.
type FindingLine struct {
	ID, Severity, Location, Problem, CloseWhen string
}

// Input is everything a context.md is rendered from. The scheduler fills it
// from the database; the renderer reads nothing else.
type Input struct {
	Role         string // coder|diagnoser|reviewer
	Constitution string
	Goal         string
	Outcome      []Acc
	Boundaries   []string

	StepID         string
	StepGoal       string
	StepAcceptance []Acc

	Others       []StepLine
	Discoveries  []string // coder only
	LastFailure  *Failure // coder only
	Guidance     string   // rescuer guidance injected into the next Run; coder only
	OpenFindings []FindingLine
	Resources    []string // access.yaml context_pack_section
	Files        []string // relevant paths, listed by the program
	Rules        []string // language profile short rules
	Headroom     []string // touched-package line headroom (ACR-27)

	// Diff is the reviewer's diff since last_reviewed_sha.
	Diff string
}

// Render returns context.md. The reviewer gets the reduced form of
// role_overrides.reviewer: constitution, goal and outcome, the step's
// acceptance, the diff and the open findings; nothing the coder wrote (ACR-15).
func Render(in Input) string {
	var b strings.Builder
	section := func(title string, lines ...string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, strings.Join(lines, "\n"))
	}
	fmt.Fprintf(&b, "# Context for %s (%s)\n\n", in.StepID, in.Role)
	if in.Constitution != "" {
		section("Constitution", in.Constitution)
	}
	reviewer := in.Role == "reviewer"
	if !reviewer {
		section("Language rules", bullets(in.Rules)...)
	}
	req := []string{"Goal: " + in.Goal}
	for _, a := range in.Outcome {
		req = append(req, fmt.Sprintf("- %s: %s", a.ID, a.EARS))
	}
	if !reviewer && len(in.Boundaries) > 0 {
		req = append(req, "Boundaries:")
		req = append(req, bullets(in.Boundaries)...)
	}
	section("Requirement", req...)

	step := []string{"Goal: " + in.StepGoal, "Acceptance:"}
	for _, a := range in.StepAcceptance {
		step = append(step, fmt.Sprintf("- %s: %s", a.ID, a.EARS))
	}
	section("Current step "+in.StepID, step...)

	if !reviewer {
		var lines []string
		for _, o := range in.Others {
			lines = append(lines, fmt.Sprintf("- %s [%s, %s]: %s", o.ID, o.Type, o.State, o.Goal))
		}
		section("Other steps", lines...)
		section("Discoveries and answered unknowns", bullets(in.Discoveries)...)
		if in.Guidance != "" {
			section("Guidance from the rescuer", in.Guidance)
		}
		if f := in.LastFailure; f != nil {
			lines := []string{"Failing tests: " + strings.Join(f.FailingTests, ", "), "", "```", tail(f.LogTail, MaxLogLines), "```"}
			section("Previous failure", lines...)
		}
	}
	var open []string
	for _, f := range in.OpenFindings {
		open = append(open, fmt.Sprintf("- %s [%s] %s: %s (close when: %s)", f.ID, f.Severity, f.Location, f.Problem, f.CloseWhen))
	}
	section("Open findings", open...)
	if reviewer {
		section("Diff since last review", "```diff", strings.TrimRight(in.Diff, "\n"), "```")
		return b.String()
	}
	section("Available resources", bullets(in.Resources)...)
	section("Relevant files", bullets(in.Files)...)
	section("Package headroom (C-03)", bullets(in.Headroom)...)
	return b.String()
}

func bullets(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, "- "+s)
	}
	return out
}

// tail keeps the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
