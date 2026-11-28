package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolver resolves paths with filepath.EvalSymlinks (D-081 ②): srt matches
// real paths, so an allowRead, denyRead, allowWrite, denyWrite or PATH entry
// that is a symlink (a mise "lts" or "shims" link) would silently not apply.
// The first path that cannot be resolved stops the build.
type resolver struct{ err error }

// one resolves p; a missing path is an error naming it.
func (r *resolver) one(name, p string) string {
	if r.err != nil || p == "" {
		return p
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.err = fmt.Errorf("policy: %s %s does not exist", name, p)
		} else {
			r.err = fmt.Errorf("policy: resolve %s %s: %w", name, p, err)
		}
		return p
	}
	return real
}

func (r *resolver) list(name string, ps []string) []string {
	if ps == nil {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = r.one(name, p)
	}
	return out
}

// resolveInputs returns in with every path resolved. The inputs were already
// checked to be clean absolute paths.
func resolveInputs(in Inputs) (Inputs, error) {
	r := &resolver{}
	in.SessionRepo = r.one("SessionRepo", in.SessionRepo)
	in.RunTmp = r.one("RunTmp", in.RunTmp)
	in.ForemanHome = r.one("ForemanHome", in.ForemanHome)
	in.DenyWrite = r.list("DenyWrite", in.DenyWrite)
	in.ProjectCacheDir = r.one("ProjectCacheDir", in.ProjectCacheDir)
	in.OwnerHome = r.one("OwnerHome", in.OwnerHome)
	in.DenyRead = r.list("DenyRead", in.DenyRead)
	in.DenyRoots = r.list("DenyRoots", in.DenyRoots)
	in.MirrorRepo = r.one("MirrorRepo", in.MirrorRepo)
	if in.MirrorRepo != "" {
		// R0: only the object store is readable (clone --shared alternates);
		// the mirror's config and refs stay unreadable.
		in.mirrorObjects = r.one("MirrorRepo objects", filepath.Join(in.MirrorRepo, "objects"))
	}
	in.HarnessPaths = r.list("HarnessPaths", in.HarnessPaths)
	in.ReadPaths = r.list("ReadPaths", in.ReadPaths)
	if in.Path != "" {
		var dirs []string
		for _, d := range strings.Split(in.Path, ":") {
			if !filepath.IsAbs(d) {
				return in, fmt.Errorf("policy: PATH entry %q must be an absolute path", d)
			}
			dirs = append(dirs, r.one("PATH entry", filepath.Clean(d)))
		}
		in.Path = strings.Join(dirs, ":")
	}
	if r.err != nil {
		return in, r.err
	}
	// The secret directories under the owner home are program-chosen names
	// that a machine may not have (no ~/.aws); a missing one is kept as is.
	in.ownerSecrets = nil
	if in.OwnerHome != "" {
		for _, d := range ownerSecretDirs() {
			p := filepath.Join(in.OwnerHome, d)
			if real, err := filepath.EvalSymlinks(p); err == nil {
				p = real
			}
			in.ownerSecrets = append(in.ownerSecrets, p)
		}
	}
	return in, nil
}

// checkAllowRead refuses an allowRead path that would re-allow something the
// policy denies by name: srt lets allowRead override denyRead.
func checkAllowRead(in Inputs) error {
	denied := append([]string{}, in.DenyRead...)
	denied = append(denied, in.ownerSecrets...)
	denied = append(denied, in.DenyRoots...)
	if in.OwnerHome != "" {
		denied = append(denied, in.OwnerHome)
	}
	allowed := []string{in.SessionRepo, in.RunTmp, in.mirrorObjects}
	allowed = append(allowed, in.HarnessPaths...)
	allowed = append(allowed, in.ReadPaths...)
	for _, a := range allowed {
		if a == "" {
			continue
		}
		for _, d := range denied {
			if within(d, a) {
				return fmt.Errorf("policy: allowRead %s covers denyRead %s", a, d)
			}
		}
	}
	return nil
}
