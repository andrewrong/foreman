package policy

import (
	"os"
	"path/filepath"
	"runtime"
)

// SystemDenyRoots are the shared, user-writable areas the policy always
// denies for reading (D-090 ⑥), whatever the configuration says: every user
// home root, every volume mount root and the system temp directories. Other
// programs keep files there; a session that ran `find /` must not see them.
// The paths the session needs (session repo, run dir, mirror objects,
// toolchain, harness) are re-opened through allowRead, which srt lets
// override a broader denyRead (docs/probes/phase-1.md P5). Only roots that
// exist are returned, as real paths, sorted.
func SystemDenyRoots() []string {
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		// tmp and var are symlinks into the system's private tree; the
		// candidates are resolved below, as srt matches real paths.
		cands = []string{"/Users", "/Volumes", "/tmp", "/var/folders", "/var/tmp"}
	default:
		cands = []string{"/home", "/root", "/mnt", "/media", "/tmp", "/var/tmp"}
	}
	set := map[string]bool{}
	for _, c := range cands {
		if real, err := filepath.EvalSymlinks(c); err == nil {
			if _, err := os.Stat(real); err == nil {
				set[real] = true
			}
		}
	}
	return sortedKeys(set)
}
