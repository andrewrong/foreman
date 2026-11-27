package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

	GitHubBase  string // API base URL; empty means api.github.com
	GitHubToken string // read by the secrets module in the daemon (D-026)

	Clock clock.Clock
	// SrtPath is the sandbox runtime. Empty runs scripted sessions and L0
	// commands directly (pass-through) and refuses the claude adapter.
	SrtPath string
	// Harness is "scripted" or "claude".
	Harness string
	// Models maps a role to its model name (skills.yaml in the daemon).
	Models map[string]string
	// Scripts maps a role to its script path (scripted harness).
	Scripts map[api.Role]string
	// Path is the session PATH; it is passed explicitly, never inherited.
	Path         string
	L0           [][]string
	Constitution string

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
	checks, err := recover.Preflight(cfg.Home, cfg.Project)
	if err != nil {
		return nil, err
	}
	stores, err := sqlite.Open(ctx, filepath.Join(cfg.Home, "db"), cfg.Clock)
	if err != nil {
		return nil, err
	}
	app := &App{Stores: stores, ProjectID: cfg.Project, Startup: checks}
	if err := app.wire(ctx, cfg); err != nil {
		_ = stores.Close()
		return nil, err
	}
	// Compose may run on a FOREMAN_HOME a previous daemon used: rebuild the
	// ledger from the Runs that hold reservations and hand the leases over.
	rep, err := recover.Startup(ctx, recover.Deps{Stores: stores, Gate: app.Gate, Ledger: cfg.Ledger, Project: cfg.Project, Home: cfg.Home})
	app.Startup = append(app.Startup, rep.Checks...)
	if err != nil {
		_ = stores.Close()
		return nil, err
	}
	app.Sched.AdoptLeases(rep.Leases)
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
	sessionIdent := wsgit.Identity{Name: "foreman-session", Email: "foreman-session@users.noreply.example"}
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
	if err := a.Integ.UseHaltStore(ctx, pushHalts{db: a.Stores.Global, project: cfg.Project, clock: cfg.Clock}); err != nil {
		return err
	}

	a.Engine = state.NewEngine(cfg.Clock, state.Config{Models: cfg.Models, Harness: cfg.Harness, RunsDir: filepath.Join(cfg.Home, "runs")})
	launcher := launch.Launcher{SrtPath: cfg.SrtPath, PassThrough: cfg.SrtPath == ""}
	if cfg.Harness != "scripted" && cfg.Harness != "claude" {
		return fmt.Errorf("compose: unknown harness %q", cfg.Harness)
	}
	a.Sched, err = sched.New(sched.Deps{
		DB: a.Stores.Work, Engine: a.Engine, Clock: cfg.Clock, Gate: a.Gate, Meters: a.Meters,
		Home: cfg.Home, Project: cfg.Project, Repo: cfg.Repo, Ops: a.Ops, Git: a.Git, Mirror: a.Mirror, Integ: a.Integ,
		SessionIdent: sessionIdent, Launcher: launcher,
		Policy: policy.Inputs{
			// denyWrite lists subdirectories of the home, never the home itself
			// (sessions/ and runs/ live inside it): access.yaml layout.
			DenyWrite: []string{filepath.Join(cfg.Home, "db"), filepath.Join(cfg.Home, "config"),
				filepath.Join(cfg.Home, "projects", cfg.Project, "repo.git"), integDir},
			ProjectCacheDir: filepath.Join(cfg.Home, "cache", cfg.Project),
			Path:            cfg.Path,
		},
		NewAdapter: func(role api.Role, lease *gate.Lease) (api.Adapter, error) {
			if cfg.Harness == "claude" {
				return claude.New(lease, claude.Config{})
			}
			return scripted.New(lease, scripted.Config{})
		},
		RunExtra: func(role api.Role, _ string) map[string]string {
			if p := cfg.Scripts[role]; p != "" {
				return map[string]string{scripted.ExtraScript: p}
			}
			return nil
		},
		L0: cfg.L0, Constitution: cfg.Constitution, OnTransition: cfg.OnTransition,
	})
	return err
}
