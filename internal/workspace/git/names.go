package git

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidName is returned for ids, branch names or ref names that are not
// in the conservative accepted shape.
var ErrInvalidName = errors.New("git: invalid name")

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ValidateName accepts a conservative subset of ref names: no leading '-' or
// '/', no "..", "//", trailing '/', '.' or ".lock", no dot-leading segment.
func ValidateName(n string) error {
	if n == "" || !nameRE.MatchString(n) || strings.HasPrefix(n, "-") || strings.HasPrefix(n, "/") ||
		strings.Contains(n, "..") || strings.Contains(n, "//") || strings.HasSuffix(n, "/") ||
		strings.HasSuffix(n, ".") || strings.HasSuffix(n, ".lock") {
		return ErrInvalidName
	}
	for _, seg := range strings.Split(n, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return ErrInvalidName
		}
	}
	return nil
}

// ValidateID accepts a requirement or step id: one segment, no '/'.
func ValidateID(id string) error {
	if strings.Contains(id, "/") {
		return ErrInvalidName
	}
	return ValidateName(id)
}

// RequirementBranch is the branch name for a requirement: foreman/<req id>.
// It is both the session repo branch and the only push target (D-050).
func RequirementBranch(reqID string) (string, error) {
	if err := ValidateID(reqID); err != nil {
		return "", err
	}
	return "foreman/" + reqID, nil
}
