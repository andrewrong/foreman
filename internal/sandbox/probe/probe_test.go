package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

// realConfig builds a probe config against the real srt, or skips.
func realConfig(t *testing.T) Config {
	t.Helper()
	srt := os.Getenv("FOREMAN_TEST_SRT")
	if srt == "" {
		t.Skip("FOREMAN_TEST_SRT not set")
	}
	if _, err := os.Stat(srt); err != nil {
		t.Skip("FOREMAN_TEST_SRT does not exist")
	}
	// Parent-env canaries: they must never reach the sandbox.
	t.Setenv("FOREMAN_CANARY_TOKEN", "canary-token")
	t.Setenv("GITHUB_TOKEN", "canary-gh")
	// srt creates a unix socket inside the run tmp; sun_path is short on
	// macOS, so the root must be a short path (not t.TempDir()).
	base, err := os.MkdirTemp("/tmp", "fmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "secrets")
	for _, d := range []string{denied, root + "/repo", root + "/run", root + "/home"} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(denied, "tok"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.New(policy.Inputs{
		SessionRepo: root + "/repo",
		RunTmp:      root + "/run",
		ForemanHome: root + "/home",
		DenyRead:    []string{denied},
		Families:    []policy.Family{policy.FamilyGo},
		LocalBind:   true,
		Path:        os.Getenv("PATH"),
		Model:       &policy.ModelToken{Name: "FOREMAN_PROBE_MODEL_TOKEN", Value: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return Config{Launcher: launch.Launcher{SrtPath: srt}, Policy: pol, DeniedPath: denied}
}

func requirePass(t *testing.T, r Report, name string) {
	t.Helper()
	c, ok := r.Get(name)
	if !ok {
		t.Fatalf("no result for %s", name)
	}
	if c.Status != Pass {
		t.Errorf("%s: %s (%s)", name, c.Status, c.Detail)
	}
}

func TestSandboxProbeGitCommit(t *testing.T) {
	cfg := realConfig(t)
	r := Run(context.Background(), cfg)
	requirePass(t, r, CheckGitCommit)
	requirePass(t, r, CheckDeniedRead)
}

func TestSandboxProbeNoCredentials(t *testing.T) {
	cfg := realConfig(t)
	r := Run(context.Background(), cfg)
	requirePass(t, r, CheckEnv)
	requirePass(t, r, CheckHostBlock)
	requirePass(t, r, CheckLocalBind)
}

func TestReportOK(t *testing.T) {
	r := Report{Results: []Result{{Name: "a", Status: Pass}}}
	if !r.OK() {
		t.Error("expected ok")
	}
	r.Results = append(r.Results, Result{Name: "b", Status: Fail})
	if r.OK() {
		t.Error("expected failure")
	}
	if (Report{}).OK() {
		t.Error("empty report must not be ok")
	}
}
