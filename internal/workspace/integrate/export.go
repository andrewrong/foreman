package integrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ExportCheckout creates a throwaway checkout of commit headSHA at dst for a
// sandboxed process that must be able to write its working tree and .git
// (L0). dst must not exist and must lie outside the integration directory.
//
// The checkout is a hardened "clone --shared --no-checkout" of the
// integration directory (cwd outside dst) followed by a hardened
// "checkout --detach"; both run before any sandbox starts. The objects are
// borrowed through alternates, so the sandbox needs read access to the
// integration directory only, which stays denyWrite (D-045, D-056).
// foremand must not run git inside dst once the sandboxed process has been
// given the directory: its .git/config is then untrusted.
func (d *Dir) ExportCheckout(ctx context.Context, headSHA, dst string) error {
	if !shaRE.MatchString(headSHA) {
		return fmt.Errorf("integrate: export: not a commit sha: %q", headSHA)
	}
	if !filepath.IsAbs(dst) || filepath.Clean(dst) != dst {
		return fmt.Errorf("integrate: export: dst must be a clean absolute path: %q", dst)
	}
	if dst == d.Path || strings.HasPrefix(dst, d.Path+string(filepath.Separator)) {
		return fmt.Errorf("integrate: export: dst is inside the integration directory")
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("integrate: export: dst exists: %s", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := d.r.Run(ctx, "", "clone", "--shared", "--no-checkout", "--quiet", "--", d.Path, dst); err != nil {
		return err
	}
	_, err := d.r.Run(ctx, dst, "checkout", "--quiet", "--detach", headSHA)
	return err
}
