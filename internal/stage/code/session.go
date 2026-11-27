// Package code runs the code stage of work steps: it prepares the run
// directory (context.md, prompt), builds the harness Invocation, launches it
// through sandbox/launch and turns result.json into an event value (MD-7:
// the caller, not this package, calls state.Transition). The session
// machinery here is shared with stage/review.
package code

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
var ErrNeedsSandbox = errors.New("code: adapter needs the srt sandbox but the launcher has none")

// sandboxRequired lists the adapters that must never run unsandboxed.
var sandboxRequired = map[string]bool{"claude": true}

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

// RunSession launches the session and waits for it to end.
func (e Env) RunSession(ctx context.Context, r Request) (Result, error) {
	if sandboxRequired[e.Adapter.Name()] && !e.Launcher.Sandboxed() {
		return Result{}, ErrNeedsSandbox
	}
	if err := os.MkdirAll(r.RunDir, 0o700); err != nil {
		return Result{}, err
	}
	ctxFile, promptFile := filepath.Join(r.RunDir, "context.md"), filepath.Join(r.RunDir, "prompt.md")
	for path, body := range map[string]string{ctxFile: r.Context, promptFile: r.Prompt} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return Result{}, err
		}
	}
	// A result.json that already exists belongs to a session that ended before
	// a crash or a failed Transition: read it, never launch a second session
	// for the same run (reconciler: "进程已退出且有 result.json").
	if _, err := os.Stat(filepath.Join(r.RunDir, "result.json")); err == nil {
		return e.readResult(r, Result{}), nil
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
		return Result{}, fmt.Errorf("code: build invocation: %w", err)
	}
	for _, f := range inv.Files {
		if err := os.WriteFile(f.Path, f.Content, os.FileMode(f.Mode)); err != nil {
			return Result{}, err
		}
	}
	in := e.Policy
	in.SessionRepo, in.RunTmp = r.SessionRepo, r.RunDir
	pol, err := policy.New(in)
	if err != nil {
		return Result{}, err
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

	runCtx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()
	exit, err := e.Launcher.Run(runCtx, pol, inv.Argv, launch.IO{
		Stdin: bytes.NewReader(inv.Stdin), Stdout: stdout, Stderr: stderr, ExtraEnv: inv.Env,
	})
	res := Result{Exit: exit}
	switch {
	case runCtx.Err() != nil && ctx.Err() == nil:
		res.Event = model.Event{Name: model.EventInfra, Detail: "session timed out"}
		return res, nil
	case err != nil:
		return res, fmt.Errorf("code: launch: %w", err)
	case exit.Code != 0:
		res.Event = model.Event{Name: model.EventInfra, Detail: fmt.Sprintf("session exited with code %d", exit.Code)}
		return res, nil
	}
	return e.readResult(r, res), nil
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
