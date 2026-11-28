// Session machinery shared by the code and review stages: it prepares the run
// directory (context.md, prompt), builds the harness Invocation, launches it
// through sandbox/launch and turns result.json into a Result (MD-7: the
// caller, not this package, calls state.Transition).

package context

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// ErrNeedsSandbox is returned instead of launching an adapter that relies on
// the OS sandbox (claude uses --permission-mode bypassPermissions, D-056)
// when the launcher has no srt.
var ErrNeedsSandbox = errors.New("context: adapter needs the srt sandbox but the launcher has none")

// sandboxRequired lists the adapters that must never run unsandboxed.
func sandboxRequired() map[string]bool {
	return map[string]bool{"claude": true}
}

// Defaults for the PP-5 fallback bounds (no adapter can stop mid-run).
const (
	DefaultTimeout  = 45 * time.Minute
	DefaultMaxTurns = 60
)

// Env is what a session needs besides the request.
type Env struct {
	Launcher launch.Launcher
	// Adapter is built by the caller with this Run's gate lease (MD-3).
	Adapter api.Adapter
	// Policy carries the run-independent sandbox inputs (Path, DenyWrite,
	// ProjectCacheDir, hosts ...); SessionRepo and RunTmp are set per Run.
	Policy   policy.Inputs
	Timeout  time.Duration
	MaxTurns int
}

// Request describes one session Run.
type Request struct {
	RunID, StepID string
	Role          api.Role
	Model         string
	RunDir        string // runs/<run id>: also the session's TMPDIR (access.yaml layout)
	SessionRepo   string
	Context       string // rendered context.md
	Prompt        string // role prompt, placed before context.md
	AcceptanceIDs []string
	Extra         map[string]string // adapter-specific (scripted: script path)
}

// Result is a finished session. Parsed is nil when no valid result.json
// exists; Event is then infra or bad_result.
type Result struct {
	Parsed *api.Result
	Event  model.Event
	Exit   launch.Exit
}

// Prepared is a session ready to start: files are written, the Invocation is
// built and the sandbox policy is resolved.
type Prepared struct {
	Inv    api.Invocation
	Policy policy.Policy
}

// Prepare writes context.md, prompt.md and the adapter's files into the run
// directory and resolves the invocation and sandbox policy. It does not start
// anything, so it is safe to repeat.
func (e Env) Prepare(r Request) (Prepared, error) {
	if sandboxRequired()[e.Adapter.Name()] && !e.Launcher.Sandboxed() {
		return Prepared{}, ErrNeedsSandbox
	}
	if err := os.MkdirAll(r.RunDir, 0o700); err != nil {
		return Prepared{}, err
	}
	ctxFile, promptFile := filepath.Join(r.RunDir, "context.md"), filepath.Join(r.RunDir, "prompt.md")
	for path, body := range map[string]string{ctxFile: r.Context, promptFile: r.Prompt} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return Prepared{}, err
		}
	}
	timeout, turns := e.Timeout, e.MaxTurns
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if turns == 0 {
		turns = DefaultMaxTurns
	}
	inv, err := e.Adapter.BuildInvocation(api.RunSpec{
		RunID: r.RunID, Role: r.Role, Model: r.Model, MaxTurns: turns, Timeout: timeout,
		WorkDir: r.SessionRepo, RunDir: r.RunDir, PromptFiles: []string{promptFile, ctxFile}, Extra: r.Extra,
	})
	if err != nil {
		return Prepared{}, fmt.Errorf("context: build invocation: %w", err)
	}
	for _, f := range inv.Files {
		if err := os.WriteFile(f.Path, f.Content, os.FileMode(f.Mode)); err != nil {
			return Prepared{}, err
		}
	}
	in := e.Policy
	in.SessionRepo, in.RunTmp = r.SessionRepo, r.RunDir
	pol, err := policy.New(in)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Inv: inv, Policy: pol}, nil
}

// Collect turns the end of a session into a Result. exit is nil when the exit
// status is not known (foremand restarted while the session ran); timedOut
// says foremand killed it for exceeding the invocation timeout. Without a
// valid result.json the event is infra (timeout, non-zero or unknown exit) or
// bad_result (clean exit without a usable result, PP-4).
func (e Env) Collect(r Request, exit *launch.Exit, timedOut bool) Result {
	res := Result{}
	if exit != nil {
		res.Exit = *exit
	}
	_, statErr := os.Stat(filepath.Join(r.RunDir, "result.json"))
	switch {
	case statErr == nil:
		return e.readResult(r, res) // a result outranks how the process ended
	case timedOut:
		res.Event = model.Event{Name: model.EventInfra, Detail: "session timed out"}
	case exit == nil:
		res.Event = model.Event{Name: model.EventInfra, Detail: "session ended without result.json (exit status unknown after restart)"}
	case exit.Code != 0:
		res.Event = model.Event{Name: model.EventInfra, Detail: fmt.Sprintf("session exited with code %d", exit.Code)}
	default:
		res.Event = model.Event{Name: model.EventBadResult, Detail: "result.json missing"}
	}
	return res
}

// RunSession launches the session and waits for it to end (synchronous path,
// for callers that do not supervise a detached process).
func (e Env) RunSession(ctx context.Context, r Request) (Result, error) {
	prep, err := e.Prepare(r)
	if err != nil {
		return Result{}, err
	}
	// A result.json that already exists belongs to a session that ended before
	// a crash or a failed Transition: read it, never launch a second session
	// for the same run (reconciler: "进程已退出且有 result.json").
	if _, err := os.Stat(filepath.Join(r.RunDir, "result.json")); err == nil {
		return e.readResult(r, Result{}), nil
	}
	stdout, err := os.Create(filepath.Join(r.RunDir, "stdout.log"))
	if err != nil {
		return Result{}, err
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(r.RunDir, "stderr.log"))
	if err != nil {
		return Result{}, err
	}
	defer stderr.Close()

	runCtx, cancel := context.WithTimeout(ctx, prep.Inv.Timeout)
	defer cancel()
	exit, err := e.Launcher.Run(runCtx, prep.Policy, prep.Inv.Argv, launch.IO{
		Stdin: bytes.NewReader(prep.Inv.Stdin), Stdout: stdout, Stderr: stderr, ExtraEnv: prep.Inv.Env,
	})
	timedOut := runCtx.Err() != nil && ctx.Err() == nil
	if err != nil && !timedOut {
		return Result{Exit: exit}, fmt.Errorf("context: launch: %w", err)
	}
	return e.Collect(r, &exit, timedOut), nil
}

// readResult validates result.json of an ended session into res.
func (e Env) readResult(r Request, res Result) Result {
	raw, err := os.ReadFile(filepath.Join(r.RunDir, "result.json"))
	if err != nil {
		res.Event = model.Event{Name: model.EventBadResult, Detail: "result.json missing"} // PP-4
		return res
	}
	parsed, err := api.ValidateResult(raw, api.ResultContext{RunID: r.RunID, Role: r.Role, Scope: "step", AcceptanceIDs: r.AcceptanceIDs})
	if err != nil {
		res.Event = model.Event{Name: model.EventBadResult, Detail: err.Error()}
		return res
	}
	res.Parsed = parsed
	res.Event.Usage = e.usage(filepath.Join(r.RunDir, "stdout.log"))
	return res
}

// usage reads the final result line of the session's stream, if any.
func (e Env) usage(stdoutPath string) model.Usage {
	f, err := os.Open(stdoutPath)
	if err != nil {
		return model.Usage{}
	}
	defer f.Close()
	sum := api.Collect(e.Adapter.ParseStream(f), nil)
	if sum.Result == nil {
		return model.Usage{}
	}
	return model.Usage{Input: sum.Result.Usage.InputTokens, Output: sum.Result.Usage.OutputTokens}
}
