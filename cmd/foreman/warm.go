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

// WarmCache warms the project cache cache/<p>/ (access.yaml R0) from the
// default branch when its head differs from the commit the cache was last
// warmed from. It runs in foremand, outside the sandbox, so the steps only
// download and compile (launch.WarmCache): no project binary is started, no
// go generate. It returns a startup check line; it never fails the daemon.
func (a *App) WarmCache(ctx context.Context, cfg Config) recover.Check {
	const name = "cache-warm"
	bad := func(format string, args ...any) recover.Check {
		return recover.Check{Name: name, Detail: fmt.Sprintf(format, args...)}
	}
	def, err := a.Mirror.DefaultBranch(ctx)
	if err != nil {
		return bad("default branch: %v", err)
	}
	_ = a.Mirror.Update(ctx) // best effort: offline, the mirror's last head is warmed
	sha, err := a.Mirror.BranchSHA(ctx, def)
	if err != nil {
		return bad("%v", err)
	}
	if launch.WarmedCommit(a.cacheDir) == sha {
		return recover.Check{Name: name, OK: true, Detail: "up to date at " + sha[:12]}
	}
	if err := a.Integ.SyncFromMirror(ctx); err != nil {
		return bad("sync integration dir: %v", err)
	}
	tmp, err := os.MkdirTemp(a.cacheDir, ".src-")
	if err != nil {
		return bad("%v", err)
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "src")
	if err := a.Integ.ExportCheckout(ctx, sha, src); err != nil {
		return bad("checkout %s: %v", sha[:12], err)
	}
	path := cfg.Path
	if cfg.Toolchain != nil {
		tc, err := cfg.Toolchain.Toolchain(ctx, src) // D-085: the checkout's mise.toml
		if err != nil {
			return bad("%v", err)
		}
		if len(tc.Path) > 0 {
			path = strings.Join(tc.Path, ":") + ":" + path
		}
	}
	if _, err := launch.WarmCache(ctx, launch.Warm{Master: a.cacheDir, Src: src, Commit: sha, Path: path, Environ: cfg.Environ}); err != nil {
		return bad("%v", err)
	}
	return recover.Check{Name: name, OK: true, Detail: "warmed at " + sha[:12]}
}
