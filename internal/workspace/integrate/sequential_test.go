package integrate

import (
	"testing"
)

// Two steps of one requirement share the session branch: the second builds on
// the first's commit and edits a file the first created. The first was squashed
// onto the requirement branch, so the second must be squashed from where the
// first ended, not from the merge base (that would add the first step's files
// a second time and conflict on every one of them).
func TestSquashMergeBackSequentialStepsOnOneSessionBranch(t *testing.T) {
	w := newWorld(t, "main")
	ref := func(id string) string { return StepRefPrefix + "steps/" + id }
	baseSHA := w.integLine("rev-parse", "refs/remotes/mirror/main")

	w.commitOnBranch("work", w.sess.Branch, map[string]string{"one.txt": "1\n", "keep.txt": "k\n"}, "first step")
	w.fetchStep("work", "s1")
	res1, err := w.merge("s1", ref("s1"))
	if err != nil {
		t.Fatal(err)
	}

	// Same session branch, next step: edits one.txt (created by s1) and adds two.txt.
	w.commitOnBranch("work", "work", map[string]string{"one.txt": "1\nfixed\n", "two.txt": "2\n"}, "second step")
	w.fetchStep("work", "s2")
	res2, err := w.merge("s2", ref("s2"))
	if err != nil {
		t.Fatalf("second step on the same session branch: %v", err)
	}
	if res2.Empty || res2.AlreadyMerged || res2.SHA == res1.SHA {
		t.Fatalf("second merge = %+v", res2)
	}
	if n := w.integLine("rev-list", "--count", baseSHA+".."+res2.SHA); n != "2" {
		t.Errorf("requirement branch has %s commits, want one per step", n)
	}
	if got, want := w.integLine("rev-parse", res2.SHA+"^{tree}"), w.integLine("rev-parse", ref("s2")+"^{tree}"); got != want {
		t.Errorf("tree after the second step %s != the session head's %s", got, want)
	}
	if got := w.integLine("show", res2.SHA+":one.txt"); got != "1\nfixed" {
		t.Errorf("one.txt = %q", got)
	}

	// A step that adds nothing since the last merged head is empty.
	w.fetchStep("work", "s3")
	if res3, err := w.merge("s3", ref("s3")); err != nil || !res3.Empty {
		t.Errorf("third merge of an unchanged head = %+v, %v; want empty", res3, err)
	}
}
