// Package check runs the check stage of work steps (state-machine reconciler:
// running(check)): the session head is fetched into foremand's integration
// directory with the hardened fetch (D-045), checked out detached, and the
// project's L0 commands run there. The result is an event value (MD-7).
package check

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
)

// Log limits of a kind=check finding: PF-CTX renders at most 80 lines and
// the findings table caps log_tail at 8000 characters.
const (
	MaxLogLines = 80
	MaxLogBytes = 8000
)

// Env is what the stage needs besides the request.
type Env struct {
	Integ *integrate.Dir
	// Launcher runs the L0 commands: with srt they run inside the sandbox
	// (the integration directory is the working directory); pass-through
	// only in tests.
	Launcher launch.Launcher
	// Policy carries the run-independent sandbox inputs (Path, DenyWrite,
	// hosts ...); SessionRepo (the integration directory) and RunTmp are set
	// per Run.
	Policy policy.Inputs
}

// Request is one check Run.
type Request struct {
	RunID, StepID string
	SessionRepo   string
	SessionBranch string     // foreman/<req id> in the session repo
	RunDir        string     // the Run's TMPDIR
	Commands      [][]string // L0 commands, argv form; the first failure stops the stage
}

// Run fetches and checks out the session head and runs every L0 command.
// It returns check_pass, or check_fail with the failing tests and the log
// tail, or infra when a command cannot be started or is killed.
func Run(ctx context.Context, env Env, r Request) (model.Event, error) {
	ref := integrate.StepRefPrefix + "steps/" + r.StepID
	if _, err := env.Integ.FetchFromSession(ctx, r.SessionRepo, r.SessionBranch, ref); err != nil {
		return model.Event{}, fmt.Errorf("check: fetch session head: %w", err)
	}
	head, err := env.Integ.CheckoutDetached(ctx, ref)
	if err != nil {
		return model.Event{}, fmt.Errorf("check: checkout: %w", err)
	}
	in := env.Policy
	in.SessionRepo, in.RunTmp = env.Integ.Path, r.RunDir
	pol, err := policy.New(in)
	if err != nil {
		return model.Event{}, err
	}
	var log bytes.Buffer
	for _, argv := range r.Commands {
		fmt.Fprintf(&log, "$ %s\n", strings.Join(argv, " "))
		exit, err := env.Launcher.Run(ctx, pol, argv, launch.IO{Stdout: &log, Stderr: &log})
		switch {
		case err != nil:
			return model.Event{Name: model.EventInfra, Head: head, Detail: fmt.Sprintf("L0 command %q: %v", argv[0], err)}, nil
		case exit.Signaled:
			return model.Event{Name: model.EventInfra, Head: head, Detail: fmt.Sprintf("L0 command %q was killed", argv[0])}, nil
		case exit.Code != 0:
			return model.Event{Name: model.EventCheckFail, Head: head, FailingTests: failingTests(log.String()),
				LogTail: logTail(log.String())}, nil
		}
	}
	return model.Event{Name: model.EventCheckPass, Head: head}, nil
}

var failRE = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)

// failingTests lists the test names of "--- FAIL:" lines (go test output),
// without duplicates. A build failure has none: the log tail carries it.
func failingTests(out string) []string {
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

// logTail keeps the last MaxLogLines lines, cut again to MaxLogBytes.
func logTail(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > MaxLogLines {
		lines = lines[len(lines)-MaxLogLines:]
	}
	s := strings.Join(lines, "\n")
	if len(s) > MaxLogBytes {
		s = s[len(s)-MaxLogBytes:]
	}
	return s
}
