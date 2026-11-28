//go:build darwin

package launch

import "golang.org/x/sys/unix"

// cloneDir clones the directory src to dst in one clonefile(2) call (APFS
// copy-on-write of the whole tree). `cp -c -R` clones file by file and is
// ~10x slower under parallel load. Any error makes CloneTree fall back to a
// plain copy (a test requires the clone to work on macOS).
func cloneDir(src, dst string) error {
	return unix.Clonefile(src, dst, 0)
}
