package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/forge/client"
)

const (
	ciLimit   = 10 * time.Minute // bound for one CI run to finish
	pollEvery = 10 * time.Second
)

// expectedOps are the operations of forge.yaml that foreman uses in v1.
var expectedOps = []string{
	"OP-01", "OP-02", "OP-03", "OP-04", "OP-05", "OP-06", "OP-07",
	"OP-08", "OP-09", "OP-10", "OP-11", "OP-12", "OP-13", "OP-14", "FG-8",
}

// waitCI waits until every workflow run on sha has completed and returns the
// first one. Polls OP-08 on the clock with a bounded deadline.
func (e *liveEnv) waitCI(ctx context.Context, sha string) (Run, error) {
	var first Run
	err := e.waitFor(ctx, "CI run on "+sha[:8], ciLimit, pollEvery, func() (bool, error) {
		runs, err := e.o.RunsList(ctx, call, e.repo, RunsFilter{HeadSHA: sha, PerPage: 20})
		if err != nil || len(runs) == 0 {
			return false, err
		}
		first = runs[0]
		for _, r := range runs {
			if r.Status != "completed" {
				return false, nil
			}
		}
		return true, nil
	})
	return first, err
}

// TestForgeLiveSmoke runs every v1 operation once against a real sandbox
// repository, in one ordered flow. It needs FOREMAN_TEST_GITHUB_TOKEN_FILE
// and FOREMAN_TEST_GITHUB_REPO (owner/name) and the repository must have a CI
// workflow; otherwise the test is skipped. The token is never printed. Each
// operation is logged with the HTTP statuses it produced.
func TestForgeLiveSmoke(t *testing.T) {
	e := newLiveEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	o, r := e.o, e.repo

	e.must("OP-11", "RateLimit", func() error { return o.RateLimit(ctx) })
	e.must("FIX", "default branch", func() error { return e.defaultBranch(ctx) })
	e.must("OP-12", "BranchProtectionGet", func() error {
		p, err := o.BranchProtectionGet(ctx, call, r, e.base)
		t.Logf("      protection available=%v", p.Available)
		return err
	})

	// Two branches from the same tip: A is the subject, B will be merged first
	// so that A falls behind the default branch.
	nameA, nameB := e.uniqueName("a"), e.uniqueName("b")
	var shaA string
	e.must("FIX", "branch A + commit", func() (err error) { shaA, err = e.newBranch(ctx, nameA); return })
	e.must("FIX", "branch B + commit", func() error { _, err := e.newBranch(ctx, nameB); return err })

	var prA, prB PR
	e.must("OP-01", "PRCreate A", func() (err error) {
		prA, err = o.PRCreate(ctx, call, r, NewPR{Title: "foreman live test " + nameA, Head: nameA, Base: e.base, Body: "Throwaway PR from the foreman live test."})
		if err == nil {
			e.trackPR(prA.Number)
		}
		return
	})
	e.must("OP-01", "PRCreate B", func() (err error) {
		prB, err = o.PRCreate(ctx, call, r, NewPR{Title: "foreman live test " + nameB, Head: nameB, Base: e.base})
		if err == nil {
			e.trackPR(prB.Number)
		}
		return
	})
	if prA.Head.SHA != shaA && shaA != "" {
		t.Logf("      note: PR head %s differs from the fixture commit %s", prA.Head.SHA[:8], shaA[:8])
	}

	// Poll PR status: REST list and get, then the GraphQL batch, then a Poll round.
	e.must("OP-02", "PRListOpen", func() error {
		prs, err := o.PRListOpen(ctx, call, r)
		var gotA, gotB bool
		for _, p := range prs {
			gotA = gotA || p.Number == prA.Number
			gotB = gotB || p.Number == prB.Number
		}
		if err == nil && !(gotA && gotB) {
			err = fmt.Errorf("open list lacks the new PRs (A=%v B=%v)", gotA, gotB)
		}
		return err
	})
	e.must("OP-03", "PRGet A", func() error {
		p, err := o.PRGet(ctx, call, r, prA.Number)
		if err == nil && (p.State != "open" || p.Head.SHA != prA.Head.SHA) {
			err = fmt.Errorf("PR A state=%s head=%s", p.State, p.Head.SHA)
		}
		return err
	})
	e.must("FG-8", "PRStatusBatch", func() error {
		m, err := o.PRStatusBatch(ctx, call, r, []int{prA.Number, prB.Number})
		if err == nil && (m[prA.Number].HeadSHA != prA.Head.SHA || m[prB.Number].State != "OPEN") {
			err = fmt.Errorf("batch answer does not match: %+v", m)
		}
		return err
	})
	_ = e.step("POLL", "Poll round", func() error {
		res, err := o.Poll(ctx, call, PollInput{Repo: r, BaseBranch: e.base, Active: true, Known: map[int]string{}, Watch: map[int]bool{prA.Number: true}})
		if err == nil && res.Details[prA.Number].PR.Number != prA.Number {
			err = errors.New("poll round returned no detail for PR A")
		}
		return err
	})

	// Wait for CI on the head, then read job logs (check run id = Actions job id).
	var run Run
	e.must("OP-08", "RunsList (wait CI)", func() (err error) { run, err = e.waitCI(ctx, prA.Head.SHA); return })
	t.Logf("      CI run %d status=%s conclusion=%s event=%s", run.ID, run.Status, run.Conclusion, run.Event)
	var jobID int64
	if err := e.step("OP-07", "ChecksForRef", func() error {
		checks, err := o.ChecksForRef(ctx, call, r, prA.Head.SHA)
		for _, c := range checks {
			if c.Status == "completed" && jobID == 0 {
				jobID = c.ID
			}
		}
		if err == nil && jobID == 0 {
			err = errors.New("no completed check run")
		}
		return err
	}); err != nil {
		e.skip("OP-07", "check runs not readable with this token or repo (forge.yaml: verified public-repo-only): "+err.Error())
		e.skip("OP-10", "no job id: OP-07 is the only operation that lists jobs")
	} else {
		e.must("OP-10", "JobLogs", func() error {
			b, err := o.JobLogs(ctx, call, r, jobID)
			t.Logf("      log bytes=%d", len(b))
			if err == nil && len(b) == 0 {
				err = errors.New("empty job log")
			}
			return err
		})
	}

	// Rerun: GitHub only accepts rerun-failed-jobs on a failed run. On a green
	// run it answers 403/409/422, which still proves the call is wired.
	e.must("OP-09", "RunRerunFailed", func() error {
		err := o.RunRerunFailed(ctx, call, r, run.ID)
		if s := statusOf(err); err != nil && (s == 403 || s == 409 || s == 422) {
			t.Logf("      rerun rejected with %d: run conclusion %q has no failed jobs", s, run.Conclusion)
			return nil
		}
		return err
	})
	e.must("OP-06", "PRComment", func() error { return o.PRComment(ctx, call, r, prA.Number, "foreman live test comment") })

	// Make A behind: merge B first (OP-04). If B cannot be merged (required
	// checks, protection), update-branch is exercised in "already up to date" mode.
	bMerged := e.step("OP-04", "PRMerge B (pinned)", func() error {
		return e.mergeRetry(ctx, prB.Number, prB.Head.SHA, 3*time.Minute)
	}) == nil
	if bMerged {
		e.untrackPR(prB.Number)
		e.must("OP-14", "RefDelete B", func() error {
			err := o.RefDelete(ctx, call, r, nameB)
			if err == nil {
				e.untrackBranch(nameB)
			}
			return err
		})
	} else {
		t.Logf("      B was not mergeable; update-branch will run in already-up-to-date mode")
	}

	headBefore := prA.Head.SHA
	e.must("OP-05", "PRUpdateBranch", func() error {
		msg, err := o.PRUpdateBranch(ctx, call, r, prA.Number, headBefore)
		t.Logf("      update-branch message=%q behind-mode=%v", msg, bMerged)
		if !bMerged && statusOf(err) == 422 {
			return nil // already up to date
		}
		return err
	})
	headNow := headBefore
	if bMerged {
		e.must("OP-03", "PRGet A (new head)", func() error {
			return e.waitFor(ctx, "update-branch to move the head", 3*time.Minute, 5*time.Second, func() (bool, error) {
				p, err := o.PRGet(ctx, call, r, prA.Number)
				headNow = p.Head.SHA
				return err == nil && headNow != headBefore, err
			})
		})
		// The old head is no longer the head: a pinned merge must be refused.
		e.must("OP-04", "PRMerge stale pin", func() error {
			_, err := o.PRMerge(ctx, call, r, prA.Number, headBefore, "squash")
			var cf client.Conflict
			if err == nil {
				return errors.New("merge with a stale head sha was accepted")
			}
			if !errors.As(err, &cf) {
				return err
			}
			return nil
		})
		e.must("OP-08", "RunsList (wait CI, new head)", func() error { _, err := e.waitCI(ctx, headNow); return err })
	}

	e.must("OP-04", "PRMerge A (pinned)", func() error { return e.mergeRetry(ctx, prA.Number, headNow, 5*time.Minute) })
	e.untrackPR(prA.Number)
	e.must("OP-03", "PRGet A (merged)", func() error {
		p, err := o.PRGet(ctx, call, r, prA.Number)
		if err == nil && (p.State != "closed" || !p.Merged) {
			err = fmt.Errorf("PR A state=%s merged=%v", p.State, p.Merged)
		}
		return err
	})
	e.must("OP-14", "RefDelete A", func() error {
		err := o.RefDelete(ctx, call, r, nameA)
		if err == nil {
			e.untrackBranch(nameA)
		}
		return err
	})

	// Close path (requirement dropped): a third PR is closed, not merged.
	nameC := e.uniqueName("c")
	e.must("FIX", "branch C + commit", func() error { _, err := e.newBranch(ctx, nameC); return err })
	var prC PR
	e.must("OP-01", "PRCreate C", func() (err error) {
		prC, err = o.PRCreate(ctx, call, r, NewPR{Title: "foreman live test " + nameC, Head: nameC, Base: e.base})
		if err == nil {
			e.trackPR(prC.Number)
		}
		return
	})
	e.must("OP-13", "PRClose C", func() error {
		err := o.PRClose(ctx, call, r, prC.Number)
		if err == nil {
			e.untrackPR(prC.Number)
		}
		return err
	})
	e.must("OP-14", "RefDelete C", func() error {
		err := o.RefDelete(ctx, call, r, nameC)
		if err == nil {
			e.untrackBranch(nameC)
		}
		return err
	})

	var missing []string
	for _, id := range expectedOps {
		if !slices.Contains(e.covered, id) {
			missing = append(missing, id)
		}
	}
	t.Logf("covered: %v", e.covered)
	t.Logf("not covered: %v (%v)", missing, e.skipped)
}
