package revision

import (
	"sort"
	"strings"
)

// maxDiffLines bounds the LCS table; larger files are shown as a full
// replacement.
const maxDiffLines = 2000

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff returns the changed lines of a file as "-old" / "+new" lines.
func lineDiff(a, b string) []string {
	x, y := splitLines(a), splitLines(b)
	if len(x) > maxDiffLines || len(y) > maxDiffLines {
		out := make([]string, 0, len(x)+len(y))
		for _, l := range x {
			out = append(out, "-"+l)
		}
		for _, l := range y {
			out = append(out, "+"+l)
		}
		return out
	}
	// lcs[i][j] = LCS length of x[i:], y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			switch {
			case x[i] == y[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i++
			j++
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, "-"+x[i])
			i++
		default:
			out = append(out, "+"+y[j])
			j++
		}
	}
	return out
}

// diffFiles renders the difference between two config directory states.
func diffFiles(before, after map[string]string) string {
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var sb strings.Builder
	for _, n := range sorted {
		b, hadB := before[n]
		a, hasA := after[n]
		if hadB && hasA && a == b {
			continue
		}
		from, to := "a/"+n, "b/"+n
		if !hadB {
			from = "/dev/null"
		}
		if !hasA {
			to = "/dev/null"
		}
		sb.WriteString("--- " + from + "\n+++ " + to + "\n")
		for _, l := range lineDiff(b, a) {
			sb.WriteString(l + "\n")
		}
	}
	return sb.String()
}

// changedNames lists the files that differ between two states.
func changedNames(a, b map[string]string) []string {
	var out []string
	for n, v := range a {
		if w, ok := b[n]; !ok || w != v {
			out = append(out, n)
		}
	}
	for n := range b {
		if _, ok := a[n]; !ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
