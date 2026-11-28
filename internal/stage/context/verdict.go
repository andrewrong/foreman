package context

import (
	"slices"

	"github.com/andrewrong/foreman/internal/forge/ops"
)

// okConclusion reports whether a completed check run did not fail; these are the conclusions of a completed check run that do not fail L1.
func okConclusion(c string) bool {
	return slices.Contains([]string{"success", "neutral", "skipped"}, c)
}

// Verdict folds the check runs of the head commit: "pass" only when there is
// at least one run and all have completed successfully (zero runs is not a
// pass); "fail" when any completed run failed; otherwise "pending".
func Verdict(checks []ops.CheckRun) string {
	if len(checks) == 0 {
		return "pending"
	}
	pass := true
	for _, c := range checks {
		if c.Status != "completed" {
			pass = false
			continue
		}
		if !okConclusion(c.Conclusion) {
			return "fail"
		}
	}
	if pass {
		return "pass"
	}
	return "pending"
}

func failedChecks(checks []ops.CheckRun) []string {
	var out []string
	for _, c := range checks {
		if c.Status == "completed" && !okConclusion(c.Conclusion) {
			out = append(out, c.Name)
		}
	}
	return out
}
