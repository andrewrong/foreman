package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
	"github.com/andrewrong/foreman/internal/workspace/integrate"
	"github.com/andrewrong/foreman/internal/workspace/session"
)

// argvRecorder wraps OSExec and keeps every argv, so the test can prove the
// token never appears there. It never records the environment.
type argvRecorder struct {
	mu   sync.Mutex
	args [][]string
}

func (a *argvRecorder) exec(ctx context.Context, c wsgit.Command) (wsgit.Result, error) {
	a.mu.Lock()
	a.args = append(a.args, append([]string(nil), c.Args...))
	a.mu.Unlock()
	return wsgit.OSExec(ctx, c)
}

// leaks lists every argv that contains one of the secrets.
func (a *argvRecorder) leaks(secrets ...string) [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var bad [][]string
	for _, argv := range a.args {
		for _, s := range secrets {
			if strings.Contains(strings.Join(argv, " "), s) {
				bad = append(bad, argv)
			}
		}
	}
	return bad
}

// filesWith lists the regular files under dirs that contain a secret.
func filesWith(t *testing.T, dirs []string, secrets ...string) []string {
	t.Helper()
	var found []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			for _, s := range secrets {
				if rerr == nil && strings.Contains(string(b), s) {
					found = append(found, p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// livePushEnv is the integration directory of the test repository with a
// commit on a throwaway requirement branch, ready to push.
type livePushEnv struct {
	rec    *argvRecorder
	gr     *wsgit.Runner
	mirror *session.Mirror
	integ  *integrate.Dir
	reqID  string
	branch string
	head   string
	url    string
}

func newLivePushEnv(t *testing.T, repoName, token string) *livePushEnv {
	t.Helper()
	ctx := context.Background()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	e := &livePushEnv{rec: &argvRecorder{}, reqID: "live-" + hex.EncodeToString(suffix[:]), url: "https://github.com/" + repoName + ".git"}
	var err error
	if e.branch, err = wsgit.RequirementBranch(e.reqID); err != nil {
		t.Fatal(err)
	}
	ident := wsgit.Identity{Name: "foreman", Email: "foreman@users.noreply.example"}
	root := t.TempDir()
	if e.gr, err = wsgit.New(wsgit.Config{Home: filepath.Join(root, "githome"), Exec: e.rec.exec, Ident: &ident}); err != nil {
		t.Fatal(err)
	}
	// The test repository is public: the mirror clone needs no credential.
	if e.mirror, err = session.CloneMirror(ctx, e.gr, session.MirrorDir(e.gr.Home(), "p"), e.url); err != nil {
		t.Fatalf("mirror clone: %v", err)
	}
	if e.integ, err = integrate.Init(ctx, e.gr, e.mirror, filepath.Join(root, "integrate"), ident); err != nil {
		t.Fatal(err)
	}
	if err := e.integ.UseToken(token); err != nil {
		t.Fatal(err)
	}
	def, err := e.mirror.DefaultBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// One commit on the requirement branch, as the merge stage leaves it.
	file := filepath.Join(e.integ.Path, "live-push-probe.txt")
	if err := os.WriteFile(file, []byte(e.reqID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"checkout", "--quiet", "-B", e.branch, "refs/remotes/mirror/" + def},
		{"add", "live-push-probe.txt"},
		{"commit", "--quiet", "-m", "live push probe " + e.reqID},
	} {
		if _, err := e.gr.Run(ctx, e.integ.Path, args...); err != nil {
			t.Fatal(err)
		}
	}
	if e.head, err = e.gr.Line(ctx, e.integ.Path, "rev-parse", "HEAD"); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestPushLiveGitHub pushes a throwaway requirement branch to the real test
// repository through integrate.Push (hardened git, token only in the
// environment of the push process), checks the token is in no argv and no
// file under the git directories, and deletes the branch with forge OP-14
// (RefDelete).
//
// Gated by FOREMAN_TEST_GITHUB_TOKEN_FILE (a file holding the PAT) and
// FOREMAN_TEST_GITHUB_REPO (owner/name of a public repository the PAT may
// push to); skipped when either is unset.
func TestPushLiveGitHub(t *testing.T) {
	tokFile := os.Getenv("FOREMAN_TEST_GITHUB_TOKEN_FILE")
	repoName := os.Getenv("FOREMAN_TEST_GITHUB_REPO")
	owner, name, ok := strings.Cut(repoName, "/")
	if tokFile == "" || !ok {
		t.Skip("FOREMAN_TEST_GITHUB_TOKEN_FILE / FOREMAN_TEST_GITHUB_REPO not set")
	}
	raw, err := os.ReadFile(tokFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	ctx := context.Background()

	clk := clock.Real()
	meters := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meters})
	o := ops.New(ops.Config{Client: client.New(client.Config{Token: token, Meters: meters, Clock: clk}), Gate: g, Meters: meters, Clock: clk})

	e := newLivePushEnv(t, repoName, token)
	pushed := false
	t.Cleanup(func() {
		if !pushed {
			return
		}
		// Push can fail after GitHub accepted the ref (the post-push check):
		// delete only when the branch really is there.
		if got, err := e.gr.Line(ctx, e.integ.Path, "ls-remote", "origin", "refs/heads/"+e.branch); err != nil || got == "" {
			return
		}
		if err := o.RefDelete(ctx, ops.Call{Priority: gate.P2}, ops.Repo{Owner: owner, Name: name}, e.branch); err != nil {
			t.Errorf("OP-14 RefDelete of %s failed (delete it by hand if it exists): %v", e.branch, err)
		}
	})
	pushed = true // the cleanup deletes the branch with OP-14 if it reached GitHub
	if err := e.integ.Push(ctx, e.reqID); err != nil {
		t.Fatalf("push: %v", err)
	}

	got, err := e.gr.Line(ctx, e.integ.Path, "ls-remote", "origin", "refs/heads/"+e.branch)
	if err != nil || !strings.HasPrefix(got, e.head) {
		t.Fatalf("remote %s = %q (err %v), want %s", e.branch, got, err, e.head)
	}
	if bad := e.rec.leaks(token, b64); len(bad) > 0 {
		t.Errorf("token in git argv: %v", bad)
	}
	if bad := filesWith(t, []string{e.mirror.Dir, e.integ.Path, e.gr.Home()}, token, b64); len(bad) > 0 {
		t.Errorf("token or its Basic payload stored in %v", bad)
	}
	if u, err := e.gr.Line(ctx, e.integ.Path, "config", "--get", "remote.origin.url"); err != nil || u != e.url {
		t.Errorf("origin URL = %q (err %v), want the plain %s", u, err, e.url)
	}
}
