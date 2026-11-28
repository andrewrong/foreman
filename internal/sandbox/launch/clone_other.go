//go:build !darwin

package launch

import "errors"

// cloneDir: only macOS has directory clones; CloneTree copies instead.
func cloneDir(_, _ string) error { return errors.ErrUnsupported }
