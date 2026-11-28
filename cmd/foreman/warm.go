package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/pipeline/recover"
	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

const warmCheck = "cache-warm"

// WarmCache warms the project cache cache/<p>/ (access.yaml R0) from the
// default branch when its head differs from the commit the cache was last
// warmed from. It runs in foremand, outside the sandbox, so the steps only
// download and compile (launch.WarmCache): no project binary is started, no
// go generate. It returns a startup check line; it never fails the daemon.
func (a *App) WarmCache(ctx context.Context, cfg Config) recover.Check {
	_ = a.Mirror.Update(ctx) // best effort: offline, the mirror's last head is warmed
	c, _ := a.warmIfAdvanced(ctx, cfg)
	return c
}

// Rewarm is the D-088 re-warm trigger of the running daemon: it compares the
// mirror's default branch head, as the stages left it (they fetch the mirror
// themselves; nothing is fetched here), with the commit the cache was warmed
// from and warms again when it moved. ran is false when nothing was done: the
// cache is current, or this very head already failed to warm.
func (a *App) Rewarm(ctx context.Context, cfg Config) (c recover.Check, ran bool) {
	return a.warmIfAdvanced(ctx, cfg)
}

func warmBad(format string, args ...any) recover.Check {
	return recover.Check{Name: warmCheck, Detail: fmt.Sprintf(format, args...)}
}

func (a *App) warmIfAdvanced(ctx context.Context, cfg Config) (recover.Check, bool) {
	def, err := a.Mirror.DefaultBranch(ctx)
	if err != nil {
		return warmBad("default branch: %v", err), true
	}
	sha, err := a.Mirror.BranchSHA(ctx, def)
	if err != nil {
		return warmBad("%v", err), true
	}
	if launch.WarmedCommit(a.cacheDir) == sha {
		return recover.Check{Name: warmCheck, OK: true, Detail: "up to date at " + sha[:12]}, false
	}
	if a.warmFailed == sha {
		return warmBad("skipped: warming %s already failed", sha[:12]), false
	}
	c := a.warm(ctx, cfg, sha)
	if !c.OK {
		a.warmFailed = sha // not retried until the head moves
	}
	return c, true
}

func (a *App) warm(ctx context.Context, cfg Config, sha string) recover.Check {
	if err := a.Integ.SyncFromMirror(ctx); err != nil {
		return warmBad("sync integration dir: %v", err)
	}
	tmp, err := os.MkdirTemp(a.cacheDir, ".src-")
	if err != nil {
		return warmBad("%v", err)
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "src")
	if err := a.Integ.ExportCheckout(ctx, sha, src); err != nil {
		return warmBad("checkout %s: %v", sha[:12], err)
	}
	path := cfg.Path
	if cfg.Toolchain != nil {
		tc, err := cfg.Toolchain.Toolchain(ctx, src) // D-085: the checkout's mise.toml
		if err != nil {
			return warmBad("%v", err)
		}
		if len(tc.Path) > 0 {
			path = strings.Join(tc.Path, ":") + ":" + path
		}
	}
	if _, err := launch.WarmCache(ctx, launch.Warm{Master: a.cacheDir, Src: src, Commit: sha, Path: path, Environ: cfg.Environ}); err != nil {
		return warmBad("%v", err)
	}
	return recover.Check{Name: warmCheck, OK: true, Detail: "warmed at " + sha[:12]}
}
