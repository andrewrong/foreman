package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/pipeline/sched"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	stagecheck "github.com/andrewrong/foreman/internal/stage/check"
	stagecode "github.com/andrewrong/foreman/internal/stage/code"
	ctxmd "github.com/andrewrong/foreman/internal/stage/context"
	stageintegrate "github.com/andrewrong/foreman/internal/stage/integrate"
	stagemerge "github.com/andrewrong/foreman/internal/stage/merge"
	stagereview "github.com/andrewrong/foreman/internal/stage/review"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// The types in this file implement the interfaces internal/pipeline/sched
// defines (ports.go) over the stage, workspace, forge, harness and sandbox
// packages. They only map fields and call through: the scheduler decides, the
// stages act (D-078, MD-12).

const (
	coderPrompt = "You are the coder of one step. Read context.md in the run directory, implement the step in the " +
		"session repository, commit, and write result.json as protocol-run.yaml PF-RESULT describes.\n"
	reviewerPrompt = "You are the reviewer of one step. Read context.md in the run directory, judge the diff against " +
		"the acceptance items, and write result.json with a verdict.\n"
	// maxDiffBytes caps the diff given to a reviewer.
	maxDiffBytes = 100 << 10
)

// mergeSlot is the Gate resource of the merge slot of a repository. The Gate
// derives "github/base-health/<repo>" from it, and forge ingests the base
// health under ops.Repo.String(); both must agree, so this is the only place
// the key is built (RES-SLOTS, RES-BASE).
func mergeSlot(r ops.Repo) string { return "local/slots/merge/" + r.String() }

func opsCall(c sched.Call) ops.Call { return ops.Call{Priority: c.Priority, Caller: c.Caller} }

// launchProcs is recover.Procs over the sandbox launcher's pid.json and kill.
type launchProcs struct{}

func (launchProcs) ReadPid(runDir string) (recover.PidFile, bool, error) {
	pf, ok, err := launch.ReadPidFile(runDir)
	return recover.PidFile{Pid: pf.Pid, StartTime: pf.StartTime}, ok, err
}

func (launchProcs) Alive(pf recover.PidFile) bool {
	return launch.PidFile{Pid: pf.Pid, StartTime: pf.StartTime}.Alive()
}

func (launchProcs) ForgetPid(runDir string) error {
	if err := os.Remove(filepath.Join(runDir, launch.PidFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (launchProcs) KillRun(runDir string) ([]int, error) { return launch.KillRun(runDir) }

// forgeAdapter is sched.Forge over forge/ops.
type forgeAdapter struct {
	ops   *ops.Ops
	repo  ops.Repo
	integ *integrate.Dir
}

func (f forgeAdapter) EnsureFresh(ctx context.Context) error { return f.ops.EnsureFresh(ctx) }

// RefreshBase: a failed poll is not fatal, the Gate then decides from what it has.
func (f forgeAdapter) RefreshBase(ctx context.Context, c sched.Call) {
	_, _ = f.ops.Poll(ctx, opsCall(c), ops.PollInput{Repo: f.repo, BaseBranch: f.integ.Default, Active: true})
}

// workspaceAdapter is sched.Workspace over the mirror, session repos and the
// integration directory.
type workspaceAdapter struct {
	git    *wsgit.Runner
	mirror *session.Mirror
	integ  *integrate.Dir
	ident  wsgit.Identity
}

func (w workspaceAdapter) CreateSessionRepo(ctx context.Context, reqID, dir string) (string, error) {
	if err := w.mirror.Update(ctx); err != nil {
		return "", err
	}
	repo, err := session.Create(ctx, w.git, w.mirror, dir, session.Spec{ReqID: reqID, BaseBranch: w.integ.Default, Identity: w.ident})
	if err != nil {
		return "", err
	}
	return repo.Dir, nil
}

// Diff is read in the integration directory (never with a session repo as cwd).
func (w workspaceAdapter) Diff(ctx context.Context, d sched.DiffSpec) string {
	base := d.Base
	if base == "" {
		base = "refs/remotes/mirror/" + w.integ.Default
	}
	head := integrate.StepRefPrefix + "steps/" + d.StepID
	if d.Branch != "" {
		head = "refs/heads/" + d.Branch
	}
	out, err := w.git.Run(ctx, w.integ.Path, "diff", "--no-color", base+"..."+head)
	if err != nil {
		return ""
	}
	if len(out) > maxDiffBytes {
		out = out[:maxDiffBytes]
	}
	return out
}

// briefInput converts the scheduler's Brief to the input of context.md.
func briefInput(b sched.Brief) ctxmd.Input {
	in := ctxmd.Input{Role: b.Role, Constitution: b.Constitution, Goal: b.Goal, Boundaries: b.Boundaries,
		StepID: b.StepID, StepGoal: b.StepGoal, Diff: b.Diff}
	for _, a := range b.Outcome {
		in.Outcome = append(in.Outcome, ctxmd.Acc(a))
	}
	for _, a := range b.StepAcceptance {
		in.StepAcceptance = append(in.StepAcceptance, ctxmd.Acc(a))
	}
	for _, o := range b.Others {
		in.Others = append(in.Others, ctxmd.StepLine(o))
	}
	for _, f := range b.OpenFindings {
		in.OpenFindings = append(in.OpenFindings, ctxmd.FindingLine(f))
	}
	if b.LastFailure != nil {
		in.LastFailure = &ctxmd.Failure{FailingTests: b.LastFailure.FailingTests, LogTail: b.LastFailure.LogTail}
	}
	return in
}

// sessionStage is sched.SessionStage of the code and review stages.
type sessionStage struct {
	prompt     string
	launcher   launch.Launcher
	policy     policy.Inputs
	exe        string // foreman binary serving launch.FlagRunSession; empty: os.Args[0]
	newAdapter func(role api.Role, lease *gate.Lease) (api.Adapter, error)
	runExtra   func(role api.Role, stepID string) map[string]string
	finish     func(ctx context.Context, env ctxmd.Env, job sched.SessionJob, req ctxmd.Request, res ctxmd.Result) (model.Event, error)
}

func (s sessionStage) Timeout() time.Duration { return ctxmd.DefaultTimeout }

func (s sessionStage) env(job sched.SessionJob) (ctxmd.Env, error) {
	ad, err := s.newAdapter(api.Role(job.Role), job.Lease)
	if err != nil {
		return ctxmd.Env{}, err
	}
	return ctxmd.Env{Launcher: s.launcher, Adapter: ad, Policy: s.policy}, nil
}

func (s sessionStage) request(job sched.SessionJob) ctxmd.Request {
	return ctxmd.Request{RunID: job.RunID, StepID: job.StepID, Role: api.Role(job.Role), Model: job.Model, RunDir: job.RunDir,
		SessionRepo: job.SessionRepo, Context: ctxmd.Render(briefInput(job.Brief)), Prompt: s.prompt, AcceptanceIDs: job.Acceptance}
}

// Start adds the adapter settings of the Run only when a session is about to
// be launched, never per poll of a running one, so a runExtra that hands out
// one script per call sees one call per launch.
func (s sessionStage) Start(_ context.Context, job sched.SessionJob, beforeLaunch func() error) (sched.Proc, error) {
	env, err := s.env(job)
	if err != nil {
		return nil, err
	}
	req := s.request(job)
	if s.runExtra != nil {
		req.Extra = s.runExtra(req.Role, req.StepID)
	}
	prep, err := env.Prepare(req)
	if err != nil {
		return nil, err
	}
	if err := beforeLaunch(); err != nil {
		return nil, err
	}
	h, err := env.Launcher.StartDetached(prep.Policy, prep.Inv.Argv, launch.Detached{
		RunDir: job.RunDir, Exe: s.exe, Stdin: prep.Inv.Stdin, ExtraEnv: prep.Inv.Env,
		StdoutPath: filepath.Join(job.RunDir, "stdout.log"), StderrPath: filepath.Join(job.RunDir, "stderr.log"),
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s sessionStage) Collect(ctx context.Context, job sched.SessionJob, exited sched.Proc, timedOut bool) (model.Event, error) {
	env, err := s.env(job)
	if err != nil {
		return model.Event{}, err
	}
	var exit *launch.Exit
	if h, ok := exited.(*launch.Handle); ok && h != nil {
		e := h.Exit()
		exit = &e
	}
	req := s.request(job)
	return s.finish(ctx, env, job, req, env.Collect(req, exit, timedOut))
}

func codeFinish(_ context.Context, _ ctxmd.Env, _ sched.SessionJob, _ ctxmd.Request, res ctxmd.Result) (model.Event, error) {
	return stagecode.EventOf(res), nil
}

// reviewMerger judges a reviewer's verdict and, for build and fix steps,
// squash-merges the step back (stagereview.Finish).
type reviewMerger struct {
	integ  *integrate.Dir
	mirror *session.Mirror
	ident  wsgit.Identity
}

func (w reviewMerger) finish(ctx context.Context, env ctxmd.Env, job sched.SessionJob, req ctxmd.Request, res ctxmd.Result) (model.Event, error) {
	rr := stagereview.Request{Session: req}
	if mb := job.MergeBack; mb != nil {
		rr.MergeBack = &stagereview.MergeBack{ReqID: mb.ReqID, StepID: mb.StepID, SessionRepo: mb.SessionRepo,
			SessionBranch: mb.SessionBranch, Subject: mb.Subject, Identity: w.ident}
	}
	return stagereview.Finish(ctx, stagereview.Env{Session: env, Integ: w.integ, Mirror: w.mirror}, rr, res)
}

// checkStage is sched.CheckStage over stage/check.
type checkStage struct {
	integ     *integrate.Dir
	launcher  launch.Launcher
	policy    policy.Inputs // L0 runs AI-written code in a throwaway checkout: the session's deny-write list, integrate/ included
	forbidden []string
	l0        [][]string
}

func (c checkStage) Run(ctx context.Context, job sched.CheckJob) (model.Event, error) {
	return stagecheck.Run(ctx, stagecheck.Env{Integ: c.integ, Launcher: c.launcher, Policy: c.policy, Forbidden: c.forbidden},
		stagecheck.Request{RunID: job.RunID, StepID: job.StepID, SessionRepo: job.SessionRepo, SessionBranch: job.SessionBranch,
			RunDir: job.RunDir, Commands: c.l0})
}

// integrateStage is sched.IntegrateStage over stage/integrate.
type integrateStage struct {
	integ *integrate.Dir
	ops   *ops.Ops
	repo  ops.Repo
}

func (i integrateStage) Run(ctx context.Context, job sched.IntegrateJob) (sched.IntegrateResult, error) {
	res, err := stageintegrate.Run(ctx, stageintegrate.Env{Integ: i.integ, Ops: i.ops, Repo: i.repo},
		stageintegrate.Request{ReqID: job.ReqID, Title: job.Title, Body: job.Body, PR: job.PR, Priority: job.Call.Priority,
			Caller: job.Call.Caller, DeadlinePassed: job.DeadlinePassed, Reruns: job.Reruns, MarkRerun: job.MarkRerun})
	return sched.IntegrateResult{PR: res.PR, Head: res.Head, Event: res.Event}, err
}

// mergeStage is sched.MergeStage over stage/merge.
type mergeStage struct {
	ops        *ops.Ops
	repo       ops.Repo
	integ      *integrate.Dir
	mirror     *session.Mirror
	guardrails []model.Guardrail
}

func (m mergeStage) Run(ctx context.Context, job sched.MergeJob) (sched.MergeResult, error) {
	res, err := stagemerge.Run(ctx, stagemerge.Env{Ops: m.ops, Repo: m.repo, Integ: m.integ, Mirror: m.mirror, Guardrails: m.guardrails},
		stagemerge.Request{ReqID: job.ReqID, PR: job.PR, Head: job.Head, Priority: job.Call.Priority, Caller: job.Call.Caller,
			LastReviewed: job.LastReviewed, GuardrailApproved: job.GuardrailApproved, Reruns: job.Reruns,
			MarkRerun: job.MarkRerun, DeadlinePassed: job.DeadlinePassed})
	return sched.MergeResult{Head: res.Head, Event: res.Event}, err
}
