package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
	"github.com/andrewrong/foreman/internal/sandbox/policy"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

// TestLayer5OtherTempDeniedWithoutTokens runs the sandbox part of the layer 5
// test alone (real srt, no model, no credentials); it skips without srt.
func TestLayer5OtherTempDeniedWithoutTokens(t *testing.T) {
	e := live5Env{srt: os.Getenv("FOREMAN_TEST_SRT"), nodeDir: os.Getenv("FOREMAN_TEST_NODE_DIR")}
	if e.srt == "" || e.nodeDir == "" {
		t.Skip("FOREMAN_TEST_SRT and FOREMAN_TEST_NODE_DIR must be set")
	}
	tmp, err := os.MkdirTemp("/tmp", "fm5h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	home, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(home, "owner")
	if err := os.MkdirAll(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	live5AssertOtherTempDenied(t, e, home, owner)
}

// live5AssertOtherTempDenied is the D-090 ⑥ check of the layer 5 test, with
// the real srt and a policy built the way a session's is: a file another
// program left in a different system temp directory cannot be read, while a
// file in the session repository (itself under the system temp area, like the
// test's FOREMAN_HOME) can. No model is involved.
func live5AssertOtherTempDenied(t *testing.T, e live5Env, home, ownerHome string) {
	t.Helper()
	other, err := os.MkdirTemp("/tmp", "fm5other")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(other) })
	secret := filepath.Join(other, "secret.txt")
	if err := os.WriteFile(secret, []byte("other-program"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, run := filepath.Join(home, "probe", "repo"), filepath.Join(home, "probe", "run")
	for _, d := range []string{repo, run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	own := filepath.Join(repo, "own.txt")
	if err := os.WriteFile(own, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.New(policy.Inputs{
		SessionRepo: repo, RunTmp: run, DenyWrite: []string{filepath.Join(home, "db")}, OwnerHome: ownerHome,
		SessionGit: policy.SessionGit{Name: "foreman-session", Email: "foreman-session@users.noreply.example", DefaultBranch: "main"},
		Path:       wsgit.SystemPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	l, err := launch.Launcher{SrtPath: e.srt, NodeDir: e.nodeDir}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	runArgv := func(argv ...string) (int, string, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var out, errb bytes.Buffer
		ex, err := l.Run(ctx, pol, argv, launch.IO{Stdout: &out, Stderr: &errb})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		return ex.Code, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
	}
	// With the Command Line Tools on PATH git is not the xcrun shim, which
	// printed a cache error on every call inside the sandbox.
	if _, err := os.Stat("/Library/Developer/CommandLineTools/usr/bin/git"); err == nil {
		if code, _, stderr := runArgv("git", "--version"); code != 0 || stderr != "" {
			t.Errorf("git --version in the sandbox: code %d, stderr %q", code, stderr)
		}
	}
	cat := func(path string) (int, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var out bytes.Buffer
		ex, err := l.Run(ctx, pol, []string{"/bin/sh", "-c", `cat -- "$1"`, "sh", path}, launch.IO{Stdout: &out, Stderr: &bytes.Buffer{}})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		return ex.Code, strings.TrimSpace(out.String())
	}
	if code, out := cat(own); code != 0 || out != "mine" {
		t.Fatalf("a file in the session repo is not readable in the sandbox: code %d %q", code, out)
	}
	if code, out := cat(secret); code == 0 || strings.Contains(out, "other-program") {
		t.Errorf("a file in another system temp directory is readable in the sandbox: code %d %q", code, out)
	}
}
