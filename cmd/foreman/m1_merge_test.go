package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/harness/scripted"
)

const updatePath = "PUT /repos/acme/widgets/pulls/7/update-branch"

func readme(first, last string) string {
	lines := []string{first}
	for i := 2; i <= 9; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	return strings.Join(append(lines, last), "\n") + "\n"
}

// Merge path, base moved and the PR shares a file with the base's change:
// update-branch (a real merge commit in the local bare remote), then the files
// both sides changed since the last review intersect, so needs_rereview (S46):
// the reviewer sees the new delta, approves (S43) and the merge pins the new
// head. The local integration branch follows the remote merge commit.
func TestM1MergeUpdateBranchIntersectingFilesNeedsRereview(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	s.f.pushMain(map[string]string{"README.txt": readme("first", "last")}, "add readme")
	s.q.push("coder", coderCommits("edit readme", map[string]string{"sum.go": sumGo, "sum_test.go": sumTst,
		"README.txt": readme("pr edit", "last")}))
	s.atStep(step2, "queued", "merge", "", 80)
	oldHead := s.remoteHead()
	s.f.pushMain(map[string]string{"README.txt": readme("first", "main edit")}, "main moves the same file")
	s.finish()

	s.wantRows("S29", "S08", "S46", "S06", "S43", "S08", "S44")
	if n := s.counter(step2, "rereviews"); n != 1 {
		t.Errorf("rereviews %d, want 1", n)
	}
	if got := s.gh.count(updatePath); got != 1 {
		t.Errorf("update-branch called %d times, want 1", got)
	}
	s.gh.set(func() {
		if u, m := slices.Index(s.gh.requests, updatePath), slices.Index(s.gh.requests, "PUT /repos/acme/widgets/pulls/7/merge"); u < 0 || m < u {
			t.Errorf("update-branch at %d, merge at %d: update must come first", u, m)
		}
	})
	newHead := s.remoteHead()
	if newHead == oldHead || s.pr("head_sha") != newHead || s.pr("last_reviewed_sha") != newHead {
		t.Errorf("heads: old %s new %s row %s reviewed %s", oldHead, newHead, s.pr("head_sha"), s.pr("last_reviewed_sha"))
	}
	if len(s.gh.merges) != 1 || s.gh.merges[0]["sha"] != newHead {
		t.Errorf("merges %+v, want one pinned to %s", s.gh.merges, newHead)
	}
	if parents, _ := s.f.git.Line(context.Background(), s.f.remote, "rev-list", "--parents", "-n1", newHead); len(strings.Fields(parents)) != 3 {
		t.Errorf("new head %q is not a merge commit", parents)
	}
	local, _ := s.f.git.Line(context.Background(), s.app.Integ.Path, "rev-parse", "refs/heads/foreman/"+reqID)
	if local != newHead {
		t.Errorf("integration branch %s did not follow the remote head %s", local, newHead)
	}
	// The integrate review saw the increment after update-branch.
	reviewers := s.runsOf("reviewer")
	if len(reviewers) != 2 {
		t.Fatalf("reviewer runs %+v, want step review and the re-review", reviewers)
	}
	diff := s.contextOf(reviewers[1].ID)["Diff since last review"]
	if !strings.Contains(diff, "main edit") {
		t.Errorf("re-review diff lacks the base change: %q", diff)
	}
}

// Same, but the base changed a file the PR does not touch: no re-review; L1
// runs again on the new head and only then does the merge go ahead (ACR-02).
func TestM1MergeUpdateBranchDisjointFilesOnlyRerunsL1(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	s.atStep(step2, "queued", "merge", "", 80)
	oldHead := s.remoteHead()
	s.f.pushMain(map[string]string{"other.txt": "unrelated\n"}, "main moves another file")
	s.finish()

	s.wantRows("S29", "S08", "S44")
	if s.count("S46") != 0 || len(s.runsOf("reviewer")) != 1 {
		t.Errorf("rows %v, reviewer runs %d: disjoint base changes must not trigger a re-review", s.rows, len(s.runsOf("reviewer")))
	}
	if got := s.gh.count(updatePath); got != 1 {
		t.Errorf("update-branch called %d times, want 1", got)
	}
	newHead := s.remoteHead()
	if newHead == oldHead || len(s.gh.merges) != 1 || s.gh.merges[0]["sha"] != newHead {
		t.Errorf("old %s new %s merges %+v", oldHead, newHead, s.gh.merges)
	}
	s.gh.set(func() {
		if h := s.gh.heads[newHead]; h == nil || h.phase != 1 {
			t.Errorf("L1 did not finish on the new head before the merge: %+v", h)
		}
	})
}

// S47: a PR whose real diff touches internal/workspace/** (G-09) is held for
// the owner (H6) before any merge call; the owner's grant (H21) records the
// approved head and the merge goes ahead. A new head (here after
// update-branch) is not covered by the approval and asks again.
func TestM1GuardrailHoldAskGrantMerge(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	s.q.push("coder", coderCommits("touch workspace", map[string]string{"sum.go": sumGo, "sum_test.go": sumTst,
		"internal/workspace/guard.go": "package workspace\n"}))
	s.atStep(step2, "waiting", "merge", "hold", 80)
	s.wantRows("S29", "S08", "S47")
	if h := s.hold(); h != "owner" {
		t.Errorf("hold %s, want owner", h)
	}
	if got := s.openAsks(); len(got) != 1 || got[0] != "H6 "+step2 {
		t.Errorf("open asks %v, want one H6 about the integrate step", got)
	}
	var checked string
	query(t, s.app, `SELECT checked FROM asks WHERE status = 'open'`, &checked)
	if !strings.Contains(checked, "G-09 internal/workspace/guard.go") {
		t.Errorf("ask checked %q does not name the guardrail and file", checked)
	}
	for i := 0; i < 4; i++ {
		s.tick()
	}
	if len(s.gh.merges) != 0 || s.pr("guardrail_approved_sha") != "" {
		t.Fatalf("merged or approved while held: merges %+v approved %q", s.gh.merges, s.pr("guardrail_approved_sha"))
	}

	head := s.remoteHead()
	s.ownerAnswers("grant")
	if got := s.pr("guardrail_approved_sha"); got != head || s.pr("head_sha") != head {
		t.Errorf("approved %q, head %q, want both %s", got, s.pr("head_sha"), head)
	}
	if st, stage, _ := s.stepPos(step2); st != "queued" || stage != "merge" || s.hold() != "none" {
		t.Errorf("after the grant: step %s/%s hold %s", st, stage, s.hold())
	}

	// The base moves before the merge: update-branch makes a new head, the
	// approval of the old head does not cover it.
	s.f.pushMain(map[string]string{"other.txt": "unrelated\n"}, "main moves")
	s.atStep(step2, "waiting", "merge", "hold", 40)
	if s.count("S47") != 2 || len(s.gh.merges) != 0 {
		t.Fatalf("rows %v merges %+v: the new head must ask again", s.rows, s.gh.merges)
	}
	newHead := s.remoteHead()
	if newHead == head || s.pr("guardrail_approved_sha") != head {
		t.Errorf("new head %s old %s approved %q", newHead, head, s.pr("guardrail_approved_sha"))
	}
	if got := s.openAsks(); len(got) != 1 || got[0] != "H6 "+step2 {
		t.Errorf("open asks %v", got)
	}

	s.ownerAnswers("grant")
	s.finish()
	s.wantRows("S47", "H21", "S08", "S47", "H21", "S08", "S44")
	if len(s.gh.merges) != 1 || s.gh.merges[0]["sha"] != newHead || s.pr("guardrail_approved_sha") != newHead {
		t.Errorf("merges %+v approved %q, want one merge pinned to %s", s.gh.merges, s.pr("guardrail_approved_sha"), newHead)
	}
}

func coderRemoves(msg, path string) scripted.Script {
	return scripted.Script{Actions: []scripted.Action{
		{Git: []string{"rm", "-rq", path}},
		{Git: []string{"commit", "-q", "-m", msg}},
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],` +
			`"acceptance":[{"id":"A1","result":"pass","evidence":"go test"}]}`}},
		{Finish: &scripted.Finish{Turns: 1, Input: 10, Output: 5, Text: "done"}},
	}}
}

// check:forbidden-paths (G-05): .github/** never reaches the PR; L0 fails the
// step (S50) at once with a finding that tells the coder what to do instead,
// and a head without the path passes.
func TestM1ForbiddenPathFailsCheckBeforeL0(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	s.q.push("coder", coderCommits("add workflow", map[string]string{"sum.go": sumGo, "sum_test.go": sumTst,
		".github/workflows/ci.yml": "name: ci\n"}))
	s.q.push("coder", coderRemoves("drop the workflow", ".github"))
	s.finish()
	s.wantRows("S04", "S40", "S05", "S50", "S04", "S40", "S05", "S41", "S06", "S42", "S29", "S08", "S44")
	coders := s.runsOf("coder")
	if len(coders) != 2 {
		t.Fatalf("coder runs %+v", coders)
	}
	prev := s.contextOf(coders[1].ID)["Previous failure"]
	for _, want := range []string{"forbidden-paths", ".github/workflows/ci.yml", "discovery"} {
		if !strings.Contains(prev, want) {
			t.Errorf("Previous failure %q lacks %q", prev, want)
		}
	}
	if len(s.gh.created) != 1 {
		t.Errorf("PRs created %d", len(s.gh.created))
	}
}
