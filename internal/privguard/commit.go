package privguard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Commit is the metadata P4 checks.
type Commit struct {
	ID             string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	Message        string
}

// CheckCommit applies P4: author and committer addresses must be noreply
// addresses, and names and message must pass the text rules.
func (sc *Scanner) CheckCommit(c Commit) []Finding {
	id := "commit:" + c.ID
	var out []Finding
	for _, e := range []struct{ kind, email string }{
		{"author-email", c.AuthorEmail}, {"committer-email", c.CommitterEmail},
	} {
		if !isNoreply(e.email) {
			out = append(out, Finding{Rule: RuleCommit, Kind: e.kind, File: id})
		}
	}
	for _, name := range []string{c.AuthorName, c.CommitterName} {
		for _, f := range sc.ScanText(id, name) {
			f.Kind, f.Rule = "name-"+f.Kind, RuleCommit
			out = append(out, f)
		}
	}
	return append(out, sc.ScanText(id+" message", c.Message)...)
}

// CheckCommit is CheckCommit with the default rules.
func CheckCommit(c Commit) []Finding { return defaultScanner.CheckCommit(c) }

func isNoreply(email string) bool {
	if !strings.Contains(email, "@") {
		return false
	}
	return !notNoreply(email, 0, len(email))
}

// CommitsInRange lists the commits of "git log <rng>" in dir (for example
// "base..head"). git runs with a scrubbed environment: no user or system
// config, no inherited variables except PATH.
func CommitsInRange(ctx context.Context, dir, rng string) ([]Commit, error) {
	const sep, end = "\x1f", "\x1e"
	format := strings.Join([]string{"%H", "%an", "%ae", "%cn", "%ce", "%B"}, sep) + end
	cmd := exec.CommandContext(ctx, "git", "log", "--format="+format, rng, "--")
	cmd.Dir = dir
	cmd.Env = ScrubbedGitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("privguard: git log %s: %v: %s", rng, err, strings.TrimSpace(stderr.String()))
	}
	var out []Commit
	for _, rec := range strings.Split(string(raw), end) {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, sep, 6)
		if len(f) != 6 {
			return nil, fmt.Errorf("privguard: unexpected git log record")
		}
		out = append(out, Commit{ID: f[0], AuthorName: f[1], AuthorEmail: f[2],
			CommitterName: f[3], CommitterEmail: f[4], Message: strings.TrimRight(f[5], "\n")})
	}
	return out, nil
}

// CheckRange runs CheckCommit on every commit of the range.
func (sc *Scanner) CheckRange(ctx context.Context, dir, rng string) ([]Finding, error) {
	commits, err := CommitsInRange(ctx, dir, rng)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, c := range commits {
		out = append(out, sc.CheckCommit(c)...)
	}
	return out, nil
}

// ScrubbedGitEnv is the environment for git calls made by privguard.
func ScrubbedGitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
}
