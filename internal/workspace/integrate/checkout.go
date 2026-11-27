package integrate

import (
	"context"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// CheckoutDetached makes the working tree of the integration directory equal
// to ref (a ref under refs/foreman/, as fetched by FetchFromSession) and
// returns its sha, so L0 can run on exactly the fetched commit (state-machine
// reconciler: running(check)). The tree is cleaned of untracked and ignored
// files first; the directory is foremand's own, never a session repo.
func (d *Dir) CheckoutDetached(ctx context.Context, ref string) (string, error) {
	if err := git.ValidateName(ref); err != nil || !strings.HasPrefix(ref, StepRefPrefix) {
		return "", git.ErrInvalidName
	}
	sha, err := d.r.Line(ctx, d.Path, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if _, err := d.r.Run(ctx, d.Path, "checkout", "--quiet", "--force", "--detach", sha); err != nil {
		return "", err
	}
	if _, err := d.r.Run(ctx, d.Path, "clean", "--quiet", "-d", "-x", "--force"); err != nil {
		return "", err
	}
	return sha, nil
}

// BranchSHA resolves the integration directory's requirement branch
// foreman/<req id>, which is what Push publishes.
func (d *Dir) BranchSHA(ctx context.Context, reqID string) (string, error) {
	branch, err := git.RequirementBranch(reqID)
	if err != nil {
		return "", err
	}
	return d.r.Line(ctx, d.Path, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
}
