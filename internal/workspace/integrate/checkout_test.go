package integrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// L0 runs on a checkout of the fetched head: files appear, stale untracked
// files disappear, and a step id in the minted "<req>/S<n>" form merges back.
func TestCheckoutDetachedAndMintedStepID(t *testing.T) {
	w := newWorld(t, "main")
	ctx := context.Background()
	w.commitOnBranch("step/s1", w.sess.Branch, map[string]string{"one.txt": "1\n"}, "s1")
	sha := w.fetchStep("step/s1", "r1/S1")

	if _, err := w.integ.CheckoutDetached(ctx, "refs/heads/main"); err == nil {
		t.Fatal("ref outside refs/foreman accepted")
	}
	if err := os.WriteFile(filepath.Join(w.integ.Path, "stale.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := w.integ.CheckoutDetached(ctx, StepRefPrefix+"steps/r1/S1")
	if err != nil || got != sha {
		t.Fatalf("checkout = %q, %v; want %s", got, err, sha)
	}
	if _, err := os.Stat(filepath.Join(w.integ.Path, "one.txt")); err != nil {
		t.Fatalf("one.txt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.integ.Path, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale untracked file survived: %v", err)
	}

	res, err := w.integ.SquashMergeBack(ctx, MergeSpec{ReqID: "r1", StepID: "r1/S1", StepRef: StepRefPrefix + "steps/r1/S1", Subject: "build"})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := w.integ.BranchSHA(ctx, "r1")
	if err != nil || tip != res.SHA {
		t.Fatalf("BranchSHA = %q, %v; want %s", tip, err, res.SHA)
	}
	if _, err := w.integ.BranchSHA(ctx, "../x"); err == nil {
		t.Fatal("invalid req id accepted")
	}
}
