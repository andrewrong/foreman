package integrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// remoteRefPrefix is where AdoptRemote keeps the fetched remote branch.
const remoteRefPrefix = StepRefPrefix + "remote/"

func (d *Dir) mirrorBase() string { return "refs/remotes/mirror/" + d.Default }

// BehindBase reports whether head lacks commits of the default branch as the
// last SyncFromMirror saw it. It is a local check; it never asks GitHub.
func (d *Dir) BehindBase(ctx context.Context, head string) (bool, error) {
	_, err := d.r.Run(ctx, d.Path, "merge-base", "--is-ancestor", d.mirrorBase(), head)
	var ge *git.Error
	switch {
	case err == nil:
		return false, nil
	case errors.As(err, &ge) && ge.ExitCode == 1:
		return true, nil
	}
	return false, err
}

// AdoptRemote fetches the requirement branch from the real remote (GitHub's
// update-branch added a merge commit there) and fast-forwards the local
// branch to it, so that a later push is still a fast-forward. It returns the
// remote tip. A remote branch that does not contain the local tip is an
// error: foremand never rewinds its own work.
func (d *Dir) AdoptRemote(ctx context.Context, reqID string) (string, error) {
	branch, err := git.RequirementBranch(reqID)
	if err != nil {
		return "", err
	}
	ref := remoteRefPrefix + branch
	if _, err := d.r.Run(ctx, d.Path, "-c", "fetch.fsckObjects=true", "fetch", "--no-tags", "--no-recurse-submodules",
		"origin", "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	tip, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	local, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if local == tip {
		return tip, nil
	}
	if _, err := d.r.Run(ctx, d.Path, "merge-base", "--is-ancestor", local, tip); err != nil {
		return "", fmt.Errorf("integrate: remote %s (%s) does not contain the local tip %s: %w", branch, tip, local, err)
	}
	if _, err := d.r.Run(ctx, d.Path, "update-ref", "refs/heads/"+branch, tip, local); err != nil {
		return "", err
	}
	return tip, nil
}

// PRFiles lists the files head changes against the default branch (diff from
// the merge base to head, D-063 "real diff"), sorted.
func (d *Dir) PRFiles(ctx context.Context, head string) ([]string, error) {
	mb, err := d.r.Line(ctx, d.Path, "merge-base", d.mirrorBase(), head)
	if err != nil {
		return nil, err
	}
	return d.diffNames(ctx, mb, head)
}

// BaseFilesSince lists the files the default branch changed since the point
// where reviewed branched off it (ACR-02: base changes since last_reviewed_sha).
func (d *Dir) BaseFilesSince(ctx context.Context, reviewed string) ([]string, error) {
	fork, err := d.r.Line(ctx, d.Path, "merge-base", d.mirrorBase(), reviewed)
	if err != nil {
		return nil, err
	}
	return d.diffNames(ctx, fork, d.mirrorBase())
}

func (d *Dir) diffNames(ctx context.Context, from, to string) ([]string, error) {
	out, err := d.r.Run(ctx, d.Path, "diff", "--name-only", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, n := range strings.Split(out, "\x00") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}
