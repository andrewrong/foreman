package secrets

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MinTokenLen is the minimum length of a token-class secret (D-026).
const MinTokenLen = 32

// Kind selects the content rules of a secret file.
type Kind int

const (
	// Plain secrets only need to be non-empty.
	Plain Kind = iota
	// Token secrets must be at least MinTokenLen bytes after trimming.
	Token
)

// Error texts name files and rules, never values.
var (
	ErrNotRegular   = errors.New("secrets: not a regular file")
	ErrSymlink      = errors.New("secrets: symbolic link refused")
	ErrMode         = errors.New("secrets: permissions allow group or other access")
	ErrOwner        = errors.New("secrets: not owned by the current user")
	ErrParentWrite  = errors.New("secrets: parent directory is group or world writable")
	ErrDirMode      = errors.New("secrets: secrets directory must be 0700")
	ErrEmpty        = errors.New("secrets: file is empty")
	ErrTooShort     = errors.New("secrets: token is shorter than the minimum length")
	ErrBadName      = errors.New("secrets: invalid secret name")
	ErrBackupFile   = errors.New("secrets: backup file in secrets directory")
	ErrDuplicateKey = errors.New("secrets: second source for the same secret")
)

// Checker holds the identity that files must belong to. The zero value uses
// the effective uid of the process; tests override UID.
type Checker struct {
	UID func() int
}

func (c Checker) uid() int {
	if c.UID != nil {
		return c.UID()
	}
	return os.Geteuid()
}

// CheckFile verifies path without reading its content: a regular file (not a
// symlink), perm&0o077 == 0, owned by the current user, in a parent directory
// that is not group or world writable.
func (c Checker) CheckFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("secrets: %s: %w", path, unwrapPath(err))
	}
	return c.checkInfo(path, fi)
}

func (c Checker) checkInfo(path string, fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s (mode %04o, want 0600 or stricter)", ErrMode, path, fi.Mode().Perm())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != c.uid() {
		return fmt.Errorf("%w: %s", ErrOwner, path)
	}
	parent := filepath.Dir(path)
	pi, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("secrets: %s: %w", parent, unwrapPath(err))
	}
	if pi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s (mode %04o)", ErrParentWrite, parent, pi.Mode().Perm())
	}
	return nil
}

// ReadFile checks path and returns its value. The file is opened with
// O_NOFOLLOW and the checks run again on the open descriptor, so a swap
// between check and read is detected. Trailing whitespace is trimmed.
func (c Checker) ReadFile(path string, kind Kind) (Secret, error) {
	if err := c.CheckFile(path); err != nil {
		return Secret{}, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Secret{}, fmt.Errorf("secrets: %s: %w", path, unwrapPath(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Secret{}, fmt.Errorf("secrets: %s: %w", path, unwrapPath(err))
	}
	if err := c.checkInfo(path, fi); err != nil {
		return Secret{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<16))
	if err != nil {
		return Secret{}, fmt.Errorf("secrets: %s: %w", path, unwrapPath(err))
	}
	v := strings.TrimSpace(string(b))
	switch {
	case v == "":
		return Secret{}, fmt.Errorf("%w: %s", ErrEmpty, path)
	case kind == Token && len(v) < MinTokenLen:
		return Secret{}, fmt.Errorf("%w: %s (need %d)", ErrTooShort, path, MinTokenLen)
	}
	return Secret{v: v}, nil
}

// Read reads the secret named name (file foreman.<name>) from dir.
func (c Checker) Read(dir, name string, kind Kind) (Secret, error) {
	if !validName(name) {
		return Secret{}, fmt.Errorf("%w: %q", ErrBadName, name)
	}
	return c.ReadFile(filepath.Join(dir, FilePrefix+name), kind)
}

// unwrapPath drops the path embedded in *PathError so messages stay uniform.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ReadFile checks and reads path as the current user.
func ReadFile(path string, kind Kind) (Secret, error) { return Checker{}.ReadFile(path, kind) }

// Read reads foreman.<name> from dir as the current user.
func Read(dir, name string, kind Kind) (Secret, error) { return Checker{}.Read(dir, name, kind) }

// CheckFile checks path as the current user.
func CheckFile(path string) error { return Checker{}.CheckFile(path) }
