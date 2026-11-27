package code

import (
	"context"
	"testing"

	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// A Run whose session already ended with a result.json (the Transition after
// it failed, or foremand restarted) is read, not relaunched: a second launch
// would pay for the same work twice and could overwrite the result.
func TestCodeStageReadsExistingResultInsteadOfRelaunching(t *testing.T) {
	w := newWorld(t, "scripted")
	ctx := context.Background()
	first := w.request(
		scripted.Action{Git: []string{"commit", "-q", "--allow-empty", "-m", "work"}},
		scripted.Action{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: okResult}},
	)
	if ev, err := Run(ctx, w.env, first); err != nil || ev.Name != model.EventRunOK {
		t.Fatalf("first run: %+v %v", ev, err)
	}
	commits, _ := w.git.Line(ctx, w.repo, "rev-list", "--count", "HEAD")

	three := 3
	again := w.request(scripted.Action{Exit: &three}) // would be infra if it launched
	ev, err := Run(ctx, w.env, again)
	if err != nil || ev.Name != model.EventRunOK {
		t.Fatalf("second run: %+v %v", ev, err)
	}
	if n, _ := w.git.Line(ctx, w.repo, "rev-list", "--count", "HEAD"); n != commits {
		t.Errorf("session ran again: %s commits, was %s", n, commits)
	}
}
