package integrate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func (w *world) merge(id, ref string) (MergeResult, error) {
	return w.integ.SquashMergeBack(context.Background(), MergeSpec{ReqID: "r1", StepID: id, StepRef: ref, Subject: "step " + id})
}

func TestSquashMergeBack(t *testing.T) {
	w := newWorld(t, "main")
	base := w.sess.Branch
	ref := func(id string) string { return StepRefPrefix + "steps/" + id }

	// Two steps start from the same base; s1 has two commits.
	w.commitOnBranch("step/s1", base, map[string]string{"one.txt": "1\n"}, "s1 first")
	w.commitOnBranch("step/s1", "step/s1", map[string]string{"one.txt": "1\n1\n", "one-b.txt": "b\n"}, "s1 second")
	w.commitOnBranch("step/s2", base, map[string]string{"two.txt": "2\n"}, "s2 only")
	w.commitOnBranch("step/s3", base, map[string]string{"a.txt": "conflicting\n"}, "s3 edits a.txt")
	w.commitOnBranch("step/s4", base, map[string]string{"a.txt": "other\n"}, "s4 edits a.txt")
	w.fetchStep("step/s1", "s1")
	w.fetchStep("step/s2", "s2")
	w.fetchStep("step/s3", "s3")
	w.fetchStep("step/s4", "s4")
	baseSHA := w.integLine("rev-parse", "refs/remotes/mirror/main")

	// s1: two commits become one commit on top of the base, with a Step-Id trailer.
	res1, err := w.merge("s1", ref("s1"))
	if err != nil {
		t.Fatal(err)
	}
	if res1.AlreadyMerged || res1.Empty {
		t.Fatalf("unexpected result %+v", res1)
	}
	if n := w.integLine("rev-list", "--count", baseSHA+".."+res1.SHA); n != "1" {
		t.Fatalf("squashed commit count = %s, want 1", n)
	}
	if p := w.integLine("rev-parse", res1.SHA+"^"); p != baseSHA {
		t.Fatalf("parent = %s, want base %s", p, baseSHA)
	}
	if msg := w.integLine("log", "-1", "--format=%B", res1.SHA); !strings.HasSuffix(msg, "Step-Id: s1") {
		t.Fatalf("message lacks trailer: %q", msg)
	}
	if got := w.integLine("show", res1.SHA+":one.txt"); got != "1\n1" {
		t.Fatalf("one.txt = %q", got)
	}

	// Idempotent on the Step-Id trailer.
	again, err := w.merge("s1", ref("s1"))
	if err != nil || !again.AlreadyMerged || again.SHA != res1.SHA {
		t.Fatalf("second merge = %+v, %v", again, err)
	}

	// s2 branched from the old base: rebased onto s1, history stays linear.
	res2, err := w.merge("s2", ref("s2"))
	if err != nil {
		t.Fatal(err)
	}
	if p := w.integLine("rev-parse", res2.SHA+"^"); p != res1.SHA {
		t.Fatalf("s2 parent = %s, want s1 commit %s", p, res1.SHA)
	}
	if got := w.integLine("rev-parse", "refs/heads/"+base); got != res2.SHA {
		t.Fatalf("branch tip %s != %s", got, res2.SHA)
	}
	for _, f := range []string{"one.txt", "one-b.txt", "two.txt"} {
		w.integLine("cat-file", "-e", res2.SHA+":"+f)
	}
	if merges := w.integLine("rev-list", "--merges", "--count", res2.SHA); merges != "0" {
		t.Fatalf("history has merge commits: %s", merges)
	}

	// s3 then s4 both edit a.txt from the same base: the second conflicts.
	res3, err := w.merge("s3", ref("s3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.merge("s4", ref("s4"))
	var ce *ConflictError
	if !errors.Is(err, ErrMergeConflict) || !errors.As(err, &ce) || len(ce.Files) != 1 || ce.Files[0] != "a.txt" {
		t.Fatalf("expected conflict on a.txt, got %v", err)
	}
	if got := w.integLine("rev-parse", "refs/heads/"+base); got != res3.SHA {
		t.Fatalf("branch moved on conflict: %s", got)
	}
	if st := w.integLine("status", "--porcelain"); st != "" {
		t.Fatalf("integration dir dirty after conflict:\n%s", st)
	}

	// A step that adds nothing is reported Empty and does not move the branch.
	w.commitOnBranch("step/s5", base, nil, "noop")
	w.commitOnBranch("step/s5", "refs/heads/step/s5~1", nil, "noop again")
	w.fetchStep("step/s5", "s5")
	res5, err := w.merge("s5", ref("s5"))
	if err != nil || !res5.Empty || res5.SHA != res3.SHA {
		t.Fatalf("empty step = %+v, %v", res5, err)
	}
}

func TestSquashMergeBackRejectsBadNames(t *testing.T) {
	w := newWorld(t, "main")
	ctx := context.Background()
	for _, s := range []MergeSpec{
		{ReqID: "../x", StepID: "s", StepRef: StepRefPrefix + "s", Subject: "x"},
		{ReqID: "r1", StepID: "s/1", StepRef: StepRefPrefix + "s", Subject: "x"},
		{ReqID: "r1", StepID: "s", StepRef: "refs/heads/main", Subject: "x"},
		{ReqID: "r1", StepID: "s", StepRef: StepRefPrefix + "s", Subject: "--amend"},
	} {
		if _, err := w.integ.SquashMergeBack(ctx, s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}
