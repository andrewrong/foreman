package context

import "regexp"

var failRE = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)

// FailingTests lists the test names of "--- FAIL:" lines (go test output),
// without duplicates. A build failure has none: the log tail carries it.
// Shared by the check stage (L0 output) and the L1 reader (job logs).
func FailingTests(out string) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, m := range failRE.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}
