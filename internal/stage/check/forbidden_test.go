package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/spec/model"
)

// check:forbidden-paths (G-05): a head touching .github/** fails before L0
// runs; the log says to write a discovery. Another project's list is honoured.
func TestForbiddenPaths(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.commit(map[string]string{"sum.go": sumGo("a + b"), "sum_test.go": sumTest})
	if err := os.MkdirAll(filepath.Join(w.sess.Dir, ".github", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.commit(map[string]string{".github/workflows/ci.yml": "name: ci\n"})
	r := w.req()
	// An L0 command that cannot start would give infra: check_fail proves the
	// forbidden-paths check ran first.
	r.Commands = [][]string{{filepath.Join(w.root, "no-such-binary")}}
	ev, err := Run(ctx, w.env, r)
	if err != nil || ev.Name != model.EventCheckFail {
		t.Fatalf("event %+v err %v, want check_fail", ev, err)
	}
	if len(ev.FailingTests) != 1 || ev.FailingTests[0] != "forbidden-paths" ||
		!strings.Contains(ev.LogTail, ".github/workflows/ci.yml") || !strings.Contains(ev.LogTail, "discovery") {
		t.Errorf("failing tests %q, log tail %q", ev.FailingTests, ev.LogTail)
	}

	w.env.Forbidden = []string{"docs/**"} // another project's list: .github is allowed there
	if ev, err := Run(ctx, w.env, w.req()); err != nil || ev.Name != model.EventCheckPass {
		t.Errorf("event %+v err %v, want check_pass with a different forbidden list", ev, err)
	}
}
