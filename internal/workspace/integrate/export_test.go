package integrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The exported checkout is a separate repository at the requested commit; the
// integration directory is untouched and bad destinations are refused.
func TestExportCheckout(t *testing.T) {
	w := newWorld(t, "main")
	ctx := context.Background()
	w.commitOnBranch("step/s1", w.sess.Branch, map[string]string{"one.txt": "1\n"}, "s1")
	sha := w.fetchStep("step/s1", "r1/S1")
	headBefore := w.integLine("rev-parse", "HEAD")
	dst := filepath.Join(t.TempDir(), "runs", "r1", "l0")

	mustErr(t, w.integ.ExportCheckout(ctx, sha, dst))
	if got := must(w.r.Line(ctx, dst, "rev-parse", "HEAD")); got != sha {
		t.Fatalf("export HEAD %s, want %s", got, sha)
	}
	if _, err := os.Stat(filepath.Join(dst, "one.txt")); err != nil {
		t.Fatal(err)
	}
	if got := w.integLine("rev-parse", "HEAD"); got != headBefore {
		t.Errorf("integration HEAD moved to %s", got)
	}
	for name, c := range map[string][2]string{
		"existing dst": {sha, dst},
		"inside integ": {sha, filepath.Join(w.integ.Path, "sub")},
		"relative dst": {sha, "l0"},
		"not a sha":    {"refs/heads/main", filepath.Join(t.TempDir(), "x")},
	} {
		if err := w.integ.ExportCheckout(ctx, c[0], c[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
