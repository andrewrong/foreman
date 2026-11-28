//go:build darwin

package launch

import (
	"syscall"
	"unsafe"
)

const (
	// sysClonefileat is clonefileat(2) on macOS. The syscall package has no
	// wrapper and golang.org/x/sys is not a registered direct dependency, so
	// the call goes through syscall.Syscall6; any error makes CloneTree fall
	// back to a plain copy (a test requires the clone to work on macOS).
	sysClonefileat = 462
	// atFDCWD is AT_FDCWD (-2) on macOS.
	atFDCWD = ^uintptr(1)
)

// cloneDir clones the directory src to dst in one call (APFS copy-on-write of
// the whole tree). `cp -c -R` clones file by file and is ~10x slower under
// parallel load.
func cloneDir(src, dst string) error {
	s, err := syscall.BytePtrFromString(src)
	if err != nil {
		return err
	}
	d, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return err
	}
	if _, _, e := syscall.Syscall6(sysClonefileat, atFDCWD, uintptr(unsafe.Pointer(s)), atFDCWD, uintptr(unsafe.Pointer(d)), 0, 0); e != 0 {
		return e
	}
	return nil
}
