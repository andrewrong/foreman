package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/spec/model"
)

const step3 = reqID + "/S3"

// seedMulti inserts a size-m requirement the way a planner would: two work
// steps (S2 depends on S1) and the integrate step depending on both.
func seedMulti(t *testing.T, app *App, clk clock.Clock) {
	t.Helper()
	ctx := context.Background()
	req := model.Requirement{ID: reqID, Kind: model.KindFeature, Source: model.SourceOwner, Priority: model.P2,
		SpecVersion: 1, Branch: "foreman/" + reqID, Hold: model.HoldNone, Status: model.StatusSubmitted, Size: model.SizeM,
		Spec: model.Spec{Title: "add Sum", Goal: "Sum adds two ints", Change: []string{"sum.go"},
			Outcome: []model.Acceptance{
				{ID: "A1", EARS: "When Sum(1,2) is called the system shall return 3", Verify: model.VerifyTest, VerifyDetail: "go test ./...", SandboxOK: true},
				{ID: "A2", EARS: "The package shall carry a package comment", Verify: model.VerifyTest, VerifyDetail: "go test ./...", SandboxOK: true}}}}
	if err := state.InsertRequirement(ctx, app.Stores.Work, req); err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.Step{
		{ReqID: reqID, Type: model.StepBuild, Goal: "implement Sum", Acceptance: []string{"A1"}, Touches: []string{"root"}},
		{ReqID: reqID, Type: model.StepBuild, Goal: "document and rename", Acceptance: []string{"A2"}, Touches: []string{"root"}, DependsOn: []string{step1}},
		{ReqID: reqID, Type: model.StepIntegrate, Goal: "integrate " + reqID, Acceptance: []string{"A1", "A2"}, Touches: []string{"root"},
			DependsOn: []string{step1, step2}},
	} {
		if err := state.InsertStep(ctx, app.Stores.Work, clk, s); err != nil {
			t.Fatal(err)
		}
	}
}

// secondStep is S2's coder: it fails (git exits non-zero, the session ends
// without a result) unless S1's file and the file pushed to the default branch
// meanwhile are in its session repo; then it renames S1's file, so S2's change
// is built on S1's.
func secondStep() scripted.Script {
	acts := []scripted.Action{
		{Git: []string{"cat-file", "-e", "HEAD:sum.go"}},   // step 1's file
		{Git: []string{"cat-file", "-e", "HEAD:base.txt"}}, // the default branch moved while S1 was reviewed
		{Git: []string{"mv", "sum.go", "add.go"}},
		{Write: &scripted.Write{Path: "doc.go", Content: "// Package sum adds ints.\npackage sum\n"}},
	}
	rest := coderFor("document and rename", nil, "A2").Actions
	return scripted.Script{Actions: append(acts, rest...)}
}

// A requirement with two dependent work steps and the integrate step, driven
// through Scheduler.Tick: S2 waits for S1 (S02, S10), its session repo has
// what S1 merged back (the squash commit rebased on the moved default branch,
// so the session was refreshed), the integrate step takes the many-step path
// (S30, review S06/S43, merge) and its review reads the whole requirement diff.
func TestM1MultiStepRequirement(t *testing.T) {
	s := newScenarioSeeded(t, seedMulti)
	s.q.push("coder", coderCommits("implement Sum", okFiles()))
	s.q.push("coder", secondStep())

	s.atStep(step1, "queued", "review", "", 40)
	s.f.pushMain(map[string]string{"base.txt": "moved\n"}, "owner change")
	s.atStep(step1, "done", "review", "", 20)
	if st, _, why := s.stepPos(step2); st == "running" || st == "done" {
		t.Errorf("S2 is %s/%s the Tick S1 finished: it must come through S10 first", st, why)
	}
	// The merge-back refreshed the session repo: its head is the requirement
	// branch tip, which now contains the default branch's new commit.
	tip, err := s.app.Integ.BranchSHA(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.sessionHead(); got != tip {
		t.Errorf("session head %s after S1's merge-back, want the requirement tip %s", got, tip)
	}

	s.finish()
	s.wantRows("S04", "S40", "S05", "S41", "S06", "S42", "S10", "S04", "S40", "S05", "S41", "S06", "S42",
		"S10", "S07", "S30", "S06", "S43", "S08", "S44")
	if s.count("S29") != 0 || s.count("S50") != 0 || s.count("S75") != 0 || s.count("S71") != 0 {
		t.Errorf("rows %v: single-step path, retry or infra in a clean multi-step run", s.rows)
	}

	// Order and dependencies, by step.
	at := func(entry string, from int) int {
		i := slices.Index(s.trace[from:], entry)
		if i < 0 {
			t.Fatalf("%q not in trace after %d: %v", entry, from, s.trace)
		}
		return from + i
	}
	s1Done := at(step1+" S42", 0)
	s2Waited := at(step2+" S02", 0)
	s2Ready := at(step2+" S10", 0)
	s2Start := at(step2+" S04", 0)
	s2Done := at(step2+" S42", 0)
	s3Ready := at(step3+" S10", 0)
	if !(s2Waited < s1Done && s1Done < s2Ready && s2Ready < s2Start && s2Start < s2Done && s2Done < s3Ready) {
		t.Errorf("order wrong: S2 waited %d, S1 done %d, S2 ready %d, S2 started %d, S2 done %d, S3 ready %d; trace %v",
			s2Waited, s1Done, s2Ready, s2Start, s2Done, s3Ready, s.trace)
	}
	if n := slices.Index(s.trace, step3+" S07"); n < s3Ready {
		t.Errorf("integrate step started CI (index %d) before its dependencies were done (%d)", n, s3Ready)
	}

	// S2 ran once and succeeded, so its script found both files.
	coders := s.runsOf("coder")
	if len(coders) != 2 || coders[1].Outcome != "ok" {
		t.Fatalf("coder runs %+v, want two clean runs", coders)
	}

	// The integrate step's reviewer saw the whole requirement diff since the
	// base: S1's file as S2 renamed it, S2's doc and S1's test, not the base
	// commit the default branch gained meanwhile.
	reviewers := s.runsOf("reviewer")
	if len(reviewers) != 3 {
		t.Fatalf("reviewer runs %+v, want S1, S2 and integrate", reviewers)
	}
	diff := s.contextOf(reviewers[2].ID)["Diff since last review"]
	if got, want := diffFiles(diff), []string{"add.go", "doc.go", "sum_test.go"}; !slices.Equal(got, want) {
		t.Errorf("integrate review diff files %v, want %v", got, want)
	}

	// Final remote state: two squashed step commits on the moved default branch.
	if n, _ := s.f.git.Line(context.Background(), s.f.remote, "rev-list", "--count", "refs/heads/main..refs/heads/foreman/"+reqID); n != "2" {
		t.Errorf("requirement branch has %s commits beyond main, want 2", n)
	}
	files, _ := s.f.git.Run(context.Background(), s.f.remote, "ls-tree", "-r", "--name-only", "refs/heads/foreman/"+reqID)
	if got := strings.Fields(files); !slices.Equal(got, []string{"add.go", "base.txt", "doc.go", "go.mod", "sum_test.go"}) {
		t.Errorf("requirement branch files %v", got)
	}
	if len(s.gh.created) != 1 || len(s.gh.merges) != 1 || s.gh.merges[0]["sha"] != s.remoteHead() {
		t.Errorf("created %d PRs, merges %+v", len(s.gh.created), s.gh.merges)
	}
}
