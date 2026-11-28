package claude

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/harness/api"
)

// TestLiveClaude runs the real CLI once with the cheapest settings. It is
// skipped unless FOREMAN_TEST_CLAUDE names the claude binary; it spends the
// owner's quota, so it uses haiku, one turn and a trivial prompt.
func TestLiveClaude(t *testing.T) {
	bin := os.Getenv("FOREMAN_TEST_CLAUDE")
	if bin == "" {
		t.Skip("FOREMAN_TEST_CLAUDE not set")
	}
	dir := t.TempDir()
	// The adapter finds claude on PATH (D-084): expose the named binary so.
	linkDir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(linkDir, Command)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", linkDir)
	a, err := New(testLease(t), Config{ReadFile: files(map[string]string{"p.md": "Reply with the single word: pong"})})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.BuildInvocation(api.RunSpec{
		RunID: "live", Role: api.RoleReviewer, Model: "haiku", MaxTurns: 1, Timeout: 2 * time.Minute,
		PromptFiles: []string{"p.md"}, Tools: []string{"Read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), inv.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, inv.Argv[0], inv.Argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(inv.Stdin)
	cmd.Env = os.Environ() // live test only: the CLI needs the owner's login
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("claude: %v\n%s", err, out.String())
	}
	sum := api.Collect(a.ParseStream(&out), nil)
	if sum.Result == nil || sum.Result.IsError {
		t.Fatalf("result %+v errors %+v", sum.Result, sum.Errors)
	}
	if sum.Result.Usage.Total() == 0 || sum.Init == nil {
		t.Fatalf("missing usage/init: %+v", sum)
	}
}
