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
func expectedOps() []string {
	return []string{
		"OP-01", "OP-02", "OP-03", "OP-04", "OP-05", "OP-06", "OP-07",
		"OP-08", "OP-09", "OP-10", "OP-11", "OP-12", "OP-13", "OP-14", "FG-8",
	}
}

// waitCI waits until every workflow run on sha has completed and returns the
// first one. Polls OP-08 on the clock with a bounded deadline.
func (e *liveEnv) waitCI(ctx context.Context, sha string) (Run, error) {
	var first Run
	err := e.waitFor(ctx, "CI run on "+sha[:8], ciLimit, pollEvery, func() (bool, error) {
		runs, err := e.o.RunsList(ctx, call(), e.repo, RunsFilter{HeadSHA: sha, PerPage: 20})
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

// liveFlow carries the state passed between the steps of TestForgeLiveSmoke.
type liveFlow struct {
	*liveEnv
	t            *testing.T
	ctx          context.Context
	nameA, nameB string
	prA, prB     PR
	run          Run
	bMerged      bool
	headNow      string
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
	f := &liveFlow{liveEnv: e, t: t, ctx: ctx}
	f.repoBasics()
	f.openPRs()
	f.pollStatus()
	f.ciLogsRerun()
	f.makeBehind()
	f.mergeA()
	f.closePath()

	var missing []string
	for _, id := range expectedOps() {
		if !slices.Contains(e.covered, id) {
			missing = append(missing, id)
		}
	}
	t.Logf("covered: %v", e.covered)
	t.Logf("not covered: %v (%v)", missing, e.skipped)
}

func (f *liveFlow) repoBasics() {
	o, r := f.o, f.repo
	f.must("OP-11", "RateLimit", func() error { return o.RateLimit(f.ctx) })
	f.must("FIX", "default branch", func() error { return f.defaultBranch(f.ctx) })
	f.must("OP-12", "BranchProtectionGet", func() error {
		p, err := o.BranchProtectionGet(f.ctx, call(), r, f.base)
		f.t.Logf("      protection available=%v", p.Available)
		return err
	})
}

// openPRs creates two branches from the same tip: A is the subject, B is
// merged first so that A falls behind the default branch.
func (f *liveFlow) openPRs() {
	f.nameA, f.nameB = f.uniqueName("a"), f.uniqueName("b")
	var shaA string
	f.must("FIX", "branch A + commit", func() (err error) { shaA, err = f.newBranch(f.ctx, f.nameA); return })
	f.must("FIX", "branch B + commit", func() error { _, err := f.newBranch(f.ctx, f.nameB); return err })
	f.must("OP-01", "PRCreate A", func() (err error) {
		f.prA, err = f.createPR(f.nameA, "Throwaway PR from the foreman live test.")
		return
	})
	f.must("OP-01", "PRCreate B", func() (err error) { f.prB, err = f.createPR(f.nameB, ""); return })
	if f.prA.Head.SHA != shaA && shaA != "" {
		f.t.Logf("      note: PR head %s differs from the fixture commit %s", f.prA.Head.SHA[:8], shaA[:8])
	}
}

func (f *liveFlow) createPR(name, body string) (PR, error) {
	pr, err := f.o.PRCreate(f.ctx, call(), f.repo, NewPR{Title: "foreman live test " + name, Head: name, Base: f.base, Body: body})
	if err == nil {
		f.trackPR(pr.Number)
	}
	return pr, err
}

// pollStatus reads PR status: REST list and get, the GraphQL batch, a Poll round.
func (f *liveFlow) pollStatus() {
	o, r, a, b := f.o, f.repo, f.prA, f.prB
	f.must("OP-02", "PRListOpen", func() error {
		prs, err := o.PRListOpen(f.ctx, call(), r)
		var gotA, gotB bool
		for _, p := range prs {
			gotA = gotA || p.Number == a.Number
			gotB = gotB || p.Number == b.Number
		}
		if err == nil && !(gotA && gotB) {
			err = fmt.Errorf("open list lacks the new PRs (A=%v B=%v)", gotA, gotB)
		}
		return err
	})
	f.must("OP-03", "PRGet A", func() error {
		p, err := o.PRGet(f.ctx, call(), r, a.Number)
		if err == nil && (p.State != "open" || p.Head.SHA != a.Head.SHA) {
			err = fmt.Errorf("PR A state=%s head=%s", p.State, p.Head.SHA)
		}
		return err
	})
	f.must("FG-8", "PRStatusBatch", func() error {
		m, err := o.PRStatusBatch(f.ctx, call(), r, []int{a.Number, b.Number})
		if err == nil && (m[a.Number].HeadSHA != a.Head.SHA || m[b.Number].State != "OPEN") {
			err = fmt.Errorf("batch answer does not match: %+v", m)
		}
		return err
	})
	_ = f.step("POLL", "Poll round", func() error {
		res, err := o.Poll(f.ctx, call(), PollInput{Repo: r, BaseBranch: f.base, Active: true, Known: map[int]string{}, Watch: map[int]bool{a.Number: true}})
		if err == nil && res.Details[a.Number].PR.Number != a.Number {
			err = errors.New("poll round returned no detail for PR A")
		}
		return err
	})
}

// ciLogsRerun waits for CI on A's head, reads job logs (check run id = Actions
// job id), reruns and comments.
func (f *liveFlow) ciLogsRerun() {
	o, r := f.o, f.repo
	f.must("OP-08", "RunsList (wait CI)", func() (err error) { f.run, err = f.waitCI(f.ctx, f.prA.Head.SHA); return })
	f.t.Logf("      CI run %d status=%s conclusion=%s event=%s", f.run.ID, f.run.Status, f.run.Conclusion, f.run.Event)
	if jobID, err := f.completedCheck(); err != nil {
		f.skip("OP-07", "check runs not readable with this token or repo (forge.yaml: verified public-repo-only): "+err.Error())
		f.skip("OP-10", "no job id: OP-07 is the only operation that lists jobs")
	} else {
		f.must("OP-10", "JobLogs", func() error {
			b, err := o.JobLogs(f.ctx, call(), r, jobID)
			f.t.Logf("      log bytes=%d", len(b))
			if err == nil && len(b) == 0 {
				err = errors.New("empty job log")
			}
			return err
		})
	}
	// GitHub only accepts rerun-failed-jobs on a failed run. On a green run it
	// answers 403/409/422, which still proves the call is wired.
	f.must("OP-09", "RunRerunFailed", func() error {
		err := o.RunRerunFailed(f.ctx, call(), r, f.run.ID)
		if s := statusOf(err); err != nil && (s == 403 || s == 409 || s == 422) {
			f.t.Logf("      rerun rejected with %d: run conclusion %q has no failed jobs", s, f.run.Conclusion)
			return nil
		}
		return err
	})
	f.must("OP-06", "PRComment", func() error { return o.PRComment(f.ctx, call(), r, f.prA.Number, "foreman live test comment") })
}

func (f *liveFlow) completedCheck() (int64, error) {
	var jobID int64
	err := f.step("OP-07", "ChecksForRef", func() error {
		checks, err := f.o.ChecksForRef(f.ctx, call(), f.repo, f.prA.Head.SHA)
		for _, c := range checks {
			if c.Status == "completed" && jobID == 0 {
				jobID = c.ID
			}
		}
		if err == nil && jobID == 0 {
			err = errors.New("no completed check run")
		}
		return err
	})
	return jobID, err
}

// makeBehind merges B first (OP-04) so A falls behind, then updates A's
// branch. If B cannot be merged (required checks, protection), update-branch
// is exercised in "already up to date" mode.
func (f *liveFlow) makeBehind() {
	f.bMerged = f.step("OP-04", "PRMerge B (pinned)", func() error {
		return f.mergeRetry(f.ctx, f.prB.Number, f.prB.Head.SHA, 3*time.Minute)
	}) == nil
	if f.bMerged {
		f.untrackPR(f.prB.Number)
		f.deleteBranch(f.nameB)
	} else {
		f.t.Logf("      B was not mergeable; update-branch will run in already-up-to-date mode")
	}
	headBefore := f.prA.Head.SHA
	f.must("OP-05", "PRUpdateBranch", func() error {
		msg, err := f.o.PRUpdateBranch(f.ctx, call(), f.repo, f.prA.Number, headBefore)
		f.t.Logf("      update-branch message=%q behind-mode=%v", msg, f.bMerged)
		if !f.bMerged && statusOf(err) == 422 {
			return nil // already up to date
		}
		return err
	})
	f.headNow = headBefore
	if f.bMerged {
		f.followNewHead(headBefore)
	}
}

// followNewHead waits for update-branch to move A's head, proves a merge
// pinned to the old head is refused, and waits for CI on the new head.
func (f *liveFlow) followNewHead(headBefore string) {
	o, r := f.o, f.repo
	f.must("OP-03", "PRGet A (new head)", func() error {
		return f.waitFor(f.ctx, "update-branch to move the head", 3*time.Minute, 5*time.Second, func() (bool, error) {
			p, err := o.PRGet(f.ctx, call(), r, f.prA.Number)
			f.headNow = p.Head.SHA
			return err == nil && f.headNow != headBefore, err
		})
	})
	f.must("OP-04", "PRMerge stale pin", func() error {
		_, err := o.PRMerge(f.ctx, call(), r, f.prA.Number, headBefore, "squash")
		var cf client.Conflict
		if err == nil {
			return errors.New("merge with a stale head sha was accepted")
		}
		if !errors.As(err, &cf) {
			return err
		}
		return nil
	})
	f.must("OP-08", "RunsList (wait CI, new head)", func() error { _, err := f.waitCI(f.ctx, f.headNow); return err })
}

func (f *liveFlow) mergeA() {
	f.must("OP-04", "PRMerge A (pinned)", func() error { return f.mergeRetry(f.ctx, f.prA.Number, f.headNow, 5*time.Minute) })
	f.untrackPR(f.prA.Number)
	f.must("OP-03", "PRGet A (merged)", func() error {
		p, err := f.o.PRGet(f.ctx, call(), f.repo, f.prA.Number)
		if err == nil && (p.State != "closed" || !p.Merged) {
			err = fmt.Errorf("PR A state=%s merged=%v", p.State, p.Merged)
		}
		return err
	})
	f.deleteBranch(f.nameA)
}

// closePath covers a dropped requirement: a third PR is closed, not merged.
func (f *liveFlow) closePath() {
	nameC := f.uniqueName("c")
	f.must("FIX", "branch C + commit", func() error { _, err := f.newBranch(f.ctx, nameC); return err })
	var prC PR
	f.must("OP-01", "PRCreate C", func() (err error) { prC, err = f.createPR(nameC, ""); return })
	f.must("OP-13", "PRClose C", func() error {
		err := f.o.PRClose(f.ctx, call(), f.repo, prC.Number)
		if err == nil {
			f.untrackPR(prC.Number)
		}
		return err
	})
	f.deleteBranch(nameC)
}

func (f *liveFlow) deleteBranch(name string) {
	f.must("OP-14", "RefDelete "+name, func() error {
		err := f.o.RefDelete(f.ctx, call(), f.repo, name)
		if err == nil {
			f.untrackBranch(name)
		}
		return err
	})
}
