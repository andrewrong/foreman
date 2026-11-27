package secrets

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
)

// FilePrefix is the file name prefix of foreman's own secrets (D-026).
const FilePrefix = "foreman."

func validName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// copySuffixes mark a file as a copy of another file's secret.
var copySuffixes = []string{".old", ".orig", ".save", ".new", ".tmp", "~"}

func baseKey(name string) string {
	n := strings.ToLower(name)
	for changed := true; changed; {
		changed = false
		for _, s := range copySuffixes {
			if t := strings.TrimSuffix(n, s); t != n {
				n, changed = t, true
			}
		}
	}
	return n
}

// CheckDir verifies the secrets directory (privguard P6): a real directory
// (not a symlink), mode 0700, owned by the current user; no *.bak* file; no
// second source for one key. It compares file names only and opens no file.
// All violations are reported together.
func (c Checker) CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("secrets: %s: %w", dir, unwrapPath(err))
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("secrets: %s is not a directory", dir)
	}
	var errs []error
	if fi.Mode().Perm() != 0o700 {
		errs = append(errs, fmt.Errorf("%w: %s (mode %04o)", ErrDirMode, dir, fi.Mode().Perm()))
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != c.uid() {
		errs = append(errs, fmt.Errorf("%w: %s", ErrOwner, dir))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("secrets: %s: %w", dir, unwrapPath(err)))...)
	}
	keys := map[string][]string{}
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(strings.ToLower(n), ".bak") {
			errs = append(errs, fmt.Errorf("%w: %s", ErrBackupFile, n))
		}
		if strings.HasPrefix(strings.ToLower(n), FilePrefix) {
			k := baseKey(n)
			keys[k] = append(keys[k], n)
		}
	}
	var dup []string
	for k, names := range keys {
		if len(names) > 1 {
			sort.Strings(names)
			dup = append(dup, fmt.Sprintf("%s: %s", k, strings.Join(names, ", ")))
		}
	}
	sort.Strings(dup)
	for _, d := range dup {
		errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateKey, d))
	}
	return errors.Join(errs...)
}

// CheckDir checks dir as the current user.
func CheckDir(dir string) error { return Checker{}.CheckDir(dir) }
