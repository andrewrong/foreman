package context

import (
	"strings"
)

//go:generate go run ./resultgen

// Prompts of the two session roles. A session reads only its run directory and
// session repository, so a prompt may name only files that are there
// (context.md) or that the session itself writes (result.json); the field
// description of result.json is part of context.md (D-091).
const (
	CoderPrompt = "You are the coder of one step. Read context.md in the run directory, implement the step in the " +
		"session repository, commit, and write result.json in the run directory as the \"Writing result.json\" " +
		"section of context.md describes.\n"
	ReviewerPrompt = "You are the reviewer of one step. Read context.md in the run directory, judge the diff against " +
		"the acceptance items, and write result.json in the run directory as the \"Writing result.json\" " +
		"section of context.md describes.\n"
)

// resultSection tells the session who it is and how to write result.json:
// the run_id and role to copy, every field, and one valid example when the
// spec has one for the role. The text comes from protocol-run.yaml PF-RESULT
// through resultdoc_gen.go.
func resultSection(in Input) []string {
	lines := []string{
		"This run: `run_id` = `" + in.RunID + "`, `role` = `" + in.Role + "`. Copy both into result.json exactly.",
		"",
		"Write `result.json` (JSON object) in the run directory when you are done. Fields:",
		"",
	}
	lines = append(lines, resultFieldLines...)
	if ex, ok := resultExamples[in.Role]; ok {
		lines = append(lines, "", "A valid example (the ids, shas and texts are illustrative; use this run's `run_id` and the acceptance ids and step scope given above):",
			"", "```json", strings.ReplaceAll(ex, resultRunIDPlaceholder, in.RunID), "```")
	}
	return lines
}
