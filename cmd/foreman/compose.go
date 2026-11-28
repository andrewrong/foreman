package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/cache"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/claude"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/pipeline/sched"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/store/sqlite"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// Config is what Compose wires together. Anything the daemon will later read
// from config files and secrets is a plain field here.
type Config struct {
	Home    string // FOREMAN_HOME, absolute and short
	Project string
	Remote  string // clone URL or path of the project's remote
	Repo    ops.Repo
	// RepoPath and DefaultBranch are the project's repo_path and
	// default_branch (CF-01). RepoPath is the owner's checkout, which foremand
	// never touches; DefaultBranch, when set, must match the mirror's.
	RepoPath, DefaultBranch string

	GitHubBase  string // API base URL; empty means api.github.com
	GitHubToken string // read by the secrets module in the daemon (D-026)
	// ModelToken is the harness credential (C-08): it reaches model sessions
	// only, never L0. Empty means the session has no model credential.
	ModelToken string
	// SessionTimeout and SessionMaxTurns bound one model session (PP-5);
	// zero means the stage defaults.
	SessionTimeout  time.Duration
	SessionMaxTurns int

	Clock clock.Clock
	// SrtPath is the sandbox runtime. Empty runs scripted sessions and L0
	// commands directly (pass-through) and refuses the claude adapter.
	SrtPath string
	// NodeDir is the real node directory srt starts with (sandbox.node_dir).
	NodeDir string
	// ReadPaths and DenyRoots are sandbox.read_paths and sandbox.deny_roots
	// (D-080, D-081); OwnerHome is added to the deny roots by the policy.
	ReadPaths, DenyRoots []string
	OwnerHome            string
	// Toolchain resolves a session repo's mise.toml (D-085); nil means system
	// tools only (scripted tests).
	Toolchain launch.ToolchainSource
	// Environ supplies the proxy/TLS variables forwarded to the cache warm-up
	// (nil: the process environment).
	Environ func() []string
	// SessionGit is the identity of the generated session gitconfig and of
	// the commits foremand makes in session workspaces (D-082, session_git.*).
	// It is an owner key (D-074): Compose refuses an unset identity.
	SessionGit wsgit.Identity
	// SecretsDir is runtime.toml secrets_dir (D-026); Workers is
	// parallel.workers (CF-04). Both are owner keys.
	SecretsDir string
	Workers    int
	// Harness is "scripted" or "claude".
	Harness string
	// Models maps a role to its model name (skills.yaml in the daemon).
	Models map[string]string
	// Scripts maps a role to its script path (scripted harness).
	Scripts map[api.Role]string
	// ScriptFor, if set, picks the script of each Run at launch (scripted
	// harness) and takes precedence over Scripts; empty falls back to Scripts.
	ScriptFor func(role api.Role, stepID string) string
	// Path is the session PATH; it is passed explicitly, never inherited.
	Path         string
	L0           [][]string
	Constitution string
	// Guardrails and Forbidden come from spec/guardrails.yaml (the config
	// module loads them in the daemon): the path list that needs H6 at merge,
	// and the globs L0 refuses.
	Guardrails []model.Guardrail
	Forbidden  []string

	// Ledger is the reservation book. It is required: Compose fails without it.
	Ledger *ledger.Ledger

	OnTransition func(stepID string, ev state.Event, out state.Outcome)
}

// App is the wired daemon core.
type App struct {
	Stores    *sqlite.Stores
	Sched     *sched.Scheduler
	Engine    *state.Engine
	Gate      *gate.Gate
	Meters    *meter.Store
	Ops       *ops.Ops
	Git       *wsgit.Runner
	Mirror    *session.Mirror
	Integ     *integrate.Dir
	ProjectID string
	cacheDir  string // FOREMAN_HOME/cache/<project>, the warm cache master
	// warmFailed is the head whose warm-up failed; it is not retried until
	// the head moves (only one goroutine at a time touches it).
	warmFailed string
	// Startup is the list of D-047 check lines (what `foreman status` prints).
	Startup []recover.Check
}

// Close releases the stores and their directory lock.
func (a *App) Close() error { return a.Stores.Close() }

// newGate builds the Gate. gate.New falls back to a private in-memory ledger
// when none is given (so forge and harness tests need not import ledger,
// MD-6); production wiring must never get that silent fallback, because a
// ledger the daemon cannot see breaks RC-2 recovery and the merge-slot
// accounting.
func newGate(o gate.Options) (*gate.Gate, error) {
	if o.Ledger == nil {
		return nil, errors.New("compose: gate needs the daemon's ledger (RC-2); refusing the in-memory fallback")
	}
	return gate.New(o), nil
}

// openMirror reuses the mirror of an earlier run after checking that it is a
// bare repository of the configured remote.
func openMirror(ctx context.Context, g *wsgit.Runner, dir, remote string) (*session.Mirror, error) {
	m := session.OpenMirror(g, dir)
	if _, err := m.DefaultBranch(ctx); err != nil {
		return nil, fmt.Errorf("compose: %s is not a usable mirror: %w", dir, err)
	}
	url, err := m.RemoteURL(ctx)
	if err != nil {
		return nil, fmt.Errorf("compose: mirror %s has no origin: %w", dir, err)
	}
	if url != remote {
		return nil, fmt.Errorf("compose: mirror %s tracks %q, config says %q", dir, url, remote)
	}
	return m, nil
}

// Compose opens the stores and builds the engine, gate, forge, harness,
// sandbox launcher, workspace and scheduler.
func Compose(ctx context.Context, cfg Config) (*App, error) {
	if cfg.Clock == nil {
		return nil, errors.New("compose: clock is required")
	}
	if cfg.Ledger == nil {
		return nil, errors.New("compose: gate needs the daemon's ledger (RC-2); refusing the in-memory fallback")
	}
	if cfg.SessionGit.Name == "" || cfg.SessionGit.Email == "" {
		return nil, errors.New("compose: session_git.name and session_git.email are owner keys (D-074); no built-in identity")
	}
	checks, err := recover.Preflight(cfg.Home, cfg.Project)
	if err != nil {
		return nil, err
	}
	stores, err := sqlite.Open(ctx, filepath.Join(cfg.Home, "db"), cfg.Clock)
	if err != nil {
		return nil, err
	}
	app := &App{Stores: stores, ProjectID: cfg.Project, Startup: checks, cacheDir: filepath.Join(cfg.Home, "cache", cfg.Project)}
	if err := app.wire(ctx, cfg); err != nil {
		_ = stores.Close()
		return nil, err
	}
	// Compose may run on a FOREMAN_HOME a previous daemon used: rebuild the
	// ledger from the Runs that hold reservations and hand the leases over.
	rep, err := recover.Startup(ctx, recover.Deps{Stores: stores, Gate: app.Gate, Ledger: cfg.Ledger, Project: cfg.Project, Home: cfg.Home, Procs: launchProcs{}})
	app.Startup = append(app.Startup, rep.Checks...)
	if err != nil {
		_ = stores.Close()
		return nil, err
	}
	app.Sched.AdoptLeases(rep.Leases)
	// Warming the project cache is an optimisation: a failure is reported in
	// the startup checks, never a reason to refuse the start (runs then use
	// empty caches).
	app.Startup = append(app.Startup, app.WarmCache(ctx, cfg))
	return app, nil
}

func (a *App) wire(ctx context.Context, cfg Config) error {
	a.Meters = meter.NewStore()
	var err error
	if a.Gate, err = newGate(gate.Options{Clock: cfg.Clock, Config: meter.DefaultConfig(), Meters: a.Meters, Ledger: cfg.Ledger}); err != nil {
		return err
	}
	c := client.New(client.Config{BaseURL: cfg.GitHubBase, Token: cfg.GitHubToken, Meters: a.Meters, Clock: cfg.Clock,
		Cache: cache.New(a.Stores.Global, cfg.Clock, 0)})
	a.Ops = ops.New(ops.Config{Client: c, Gate: a.Gate, Meters: a.Meters, Clock: cfg.Clock})
	if err := a.Ops.EnsureFresh(ctx); err != nil {
		return fmt.Errorf("compose: GitHub rate limit probe: %w", err)
	}

	foremanIdent := wsgit.Identity{Name: "foreman", Email: "foreman@users.noreply.example"}
	sessionIdent := cfg.SessionGit
	if a.Git, err = wsgit.New(wsgit.Config{Home: filepath.Join(cfg.Home, "githome"), Clock: cfg.Clock, Ident: &foremanIdent}); err != nil {
		return err
	}
	mirrorDir := session.MirrorDir(cfg.Home, cfg.Project)
	if _, serr := os.Stat(mirrorDir); serr == nil {
		if a.Mirror, err = openMirror(ctx, a.Git, mirrorDir, cfg.Remote); err != nil {
			return err
		}
	} else if a.Mirror, err = session.CloneMirror(ctx, a.Git, mirrorDir, cfg.Remote); err != nil {
		return err
	}
	integDir := filepath.Join(cfg.Home, "projects", cfg.Project, "integrate")
	if _, serr := os.Stat(integDir); serr == nil {
		a.Integ, err = integrate.Open(ctx, a.Git, a.Mirror, integDir, foremanIdent)
	} else {
		a.Integ, err = integrate.Init(ctx, a.Git, a.Mirror, integDir, foremanIdent)
	}
	if err != nil {
		return err
	}
	if cfg.GitHubToken != "" {
		// Only foremand pushes (C-08); the token reaches git through the
		// environment of the push process alone.
		if err := a.Integ.UseToken(cfg.GitHubToken); err != nil {
			return err
		}
		if err := a.Mirror.UseToken(cfg.GitHubToken); err != nil {
			return err
		}
	}
	if err := a.Integ.UseHaltStore(ctx, pushHalts{db: a.Stores.Global, project: cfg.Project, clock: cfg.Clock}); err != nil {
		return err
	}

	a.Engine = state.NewEngine(cfg.Clock, state.Config{Models: cfg.Models, Harness: cfg.Harness, RunsDir: filepath.Join(cfg.Home, "runs")})
	launcher, err := launch.Launcher{SrtPath: cfg.SrtPath, NodeDir: cfg.NodeDir, PassThrough: cfg.SrtPath == ""}.Resolve()
	if err != nil {
		return err
	}
	if cfg.Harness != "scripted" && cfg.Harness != "claude" {
		return fmt.Errorf("compose: unknown harness %q", cfg.Harness)
	}
	policyIn, err := a.layoutPolicy(ctx, cfg, integDir)
	if err != nil {
		return err
	}
	newAdapter := func(role api.Role, lease *gate.Lease) (api.Adapter, error) {
		if cfg.Harness == "claude" {
			return claude.New(lease, claude.Config{})
		}
		return scripted.New(lease, scripted.Config{})
	}
	runExtra := func(role api.Role, stepID string) map[string]string {
		p := cfg.Scripts[role]
		if cfg.ScriptFor != nil {
			if q := cfg.ScriptFor(role, stepID); q != "" {
				p = q
			}
		}
		if p != "" {
			return map[string]string{scripted.ExtraScript: p}
		}
		return nil
	}
	// L0 runs AI-written code and never gets the model token (C-08).
	sessionIn := policyIn
	if cfg.ModelToken != "" {
		sessionIn.Model = &policy.ModelToken{Name: modelTokenEnv, Value: cfg.ModelToken}
	}
	merger := reviewMerger{integ: a.Integ, mirror: a.Mirror, ident: sessionIdent}
	a.Sched, err = sched.New(sched.Deps{
		DB: a.Stores.Work, Engine: a.Engine, Clock: cfg.Clock, Gate: a.Gate, Meters: a.Meters,
		Home: cfg.Home, Project: cfg.Project,
		Slots:     sched.Slots{REST: client.ResREST, Write: client.ResWrite, Merge: mergeSlot(cfg.Repo)},
		Forge:     forgeAdapter{ops: a.Ops, repo: cfg.Repo, integ: a.Integ},
		Workspace: workspaceAdapter{git: a.Git, mirror: a.Mirror, integ: a.Integ, ident: sessionIdent},
		Procs:     launchProcs{},
		Code: sessionStage{prompt: coderPrompt, launcher: launcher, toolchain: cfg.Toolchain, policy: sessionIn, newAdapter: newAdapter,
			runExtra: runExtra, finish: codeFinish, timeout: cfg.SessionTimeout, maxTurns: cfg.SessionMaxTurns},
		Review: sessionStage{prompt: reviewerPrompt, launcher: launcher, toolchain: cfg.Toolchain, policy: sessionIn, newAdapter: newAdapter,
			runExtra: runExtra, finish: merger.finish, timeout: cfg.SessionTimeout, maxTurns: cfg.SessionMaxTurns},
		Check:        checkStage{integ: a.Integ, launcher: launcher, toolchain: cfg.Toolchain, policy: policyIn, forbidden: cfg.Forbidden, l0: cfg.L0},
		Integrate:    integrateStage{integ: a.Integ, ops: a.Ops, repo: cfg.Repo},
		Merge:        mergeStage{ops: a.Ops, repo: cfg.Repo, integ: a.Integ, mirror: a.Mirror, guardrails: cfg.Guardrails},
		Constitution: cfg.Constitution, OnTransition: cfg.OnTransition,
	})
	return err
}

// layoutPolicy creates the layout directories the policy lists (it refuses a
// path that does not exist, D-081 ②; access.yaml layout) and returns the
// run-independent policy inputs.
func (a *App) layoutPolicy(ctx context.Context, cfg Config, integDir string) (policy.Inputs, error) {
	defBranch, err := a.Mirror.DefaultBranch(ctx)
	if err != nil {
		return policy.Inputs{}, err
	}
	if cfg.DefaultBranch != "" && cfg.DefaultBranch != defBranch {
		return policy.Inputs{}, fmt.Errorf("compose: config default_branch %q, mirror default branch %q", cfg.DefaultBranch, defBranch)
	}
	for _, d := range []string{"db", "config", filepath.Join("cache", cfg.Project)} {
		if err := os.MkdirAll(filepath.Join(cfg.Home, d), 0o700); err != nil {
			return policy.Inputs{}, err
		}
	}
	return sessionPolicy(cfg, integDir, a.Mirror.Dir, defBranch), nil
}

// sessionPolicy is the run-independent sandbox policy of sessions and of L0.
// denyWrite lists subdirectories of the home, never the home itself
// (sessions/ and runs/ live inside it): access.yaml layout. L0 runs
// AI-written code in a throwaway checkout under the Run's directory, so the
// deny-write list is the session's own, integrate/ included: it must not be
// able to alter the directory foremand runs its VCS in.
func sessionPolicy(cfg Config, integDir, mirrorDir, defaultBranch string) policy.Inputs {
	return policy.Inputs{
		MirrorRepo: mirrorDir, ReadPaths: cfg.ReadPaths, DenyRoots: cfg.DenyRoots, OwnerHome: cfg.OwnerHome,
		SessionGit: policy.SessionGit{Name: cfg.SessionGit.Name, Email: cfg.SessionGit.Email, DefaultBranch: defaultBranch},
		DenyWrite: []string{filepath.Join(cfg.Home, "db"), filepath.Join(cfg.Home, "config"),
			filepath.Join(cfg.Home, "projects", cfg.Project, "repo.git"), integDir},
		ProjectCacheDir: filepath.Join(cfg.Home, "cache", cfg.Project),
		Path:            cfg.Path,
		GoFlags:         policy.DefaultGoFlags,
		Families:        policy.AllFamilies(), // access.yaml R0: package hosts, always granted
	}
}
