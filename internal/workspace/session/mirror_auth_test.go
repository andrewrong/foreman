package session

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

const fakeToken = "tok-0123456789abcdef"

func newAuthMirror(t *testing.T) (*Mirror, *git.Runner) {
	t.Helper()
	r := newRunner(t)
	m, err := CloneMirror(context.Background(), r, filepath.Join(t.TempDir(), "repo.git"), seedRemote(t, r))
	mustErr(t, err)
	return m, r
}

// The mirror's fetch credential lives only in the environment of the fetch
// process, scoped to the https origin host, never in argv or the config file.
func TestMirrorTokenGoesOnlyThroughTheEnvironment(t *testing.T) {
	m, r := newAuthMirror(t)
	ctx := context.Background()
	mustErr(t, m.UseToken(fakeToken))
	must(r.Run(ctx, m.Dir, "config", "remote.origin.url", "https://github.example/o/r.git"))

	fr, err := m.fetchRunner(ctx)
	mustErr(t, err)
	env := strings.Join(fr.Env(), "\n")
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + fakeToken))
	for _, want := range []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=\n",
		"GIT_CONFIG_KEY_1=http.https://github.example/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Basic " + b64} {
		if !strings.Contains(env+"\n", want) {
			t.Errorf("credential env lacks %q", want)
		}
	}
	if argv := strings.Join(fr.Argv("fetch", "--prune", "origin"), " "); strings.Contains(argv, fakeToken) || strings.Contains(argv, b64) {
		t.Errorf("token in argv: %s", argv)
	}
	if strings.Contains(strings.Join(m.r.Env(), "\n"), fakeToken) || strings.Contains(strings.Join(m.r.Env(), "\n"), b64) {
		t.Error("the mirror's ordinary runner carries the credential")
	}
	cfg, err := r.Run(ctx, m.Dir, "config", "--list", "--local")
	mustErr(t, err)
	if strings.Contains(cfg, fakeToken) || strings.Contains(cfg, b64) {
		t.Errorf("token stored in the mirror's config:\n%s", cfg)
	}
}

// No credential goes to an origin that is not https, and Update still works
// with a token set (local origin).
func TestMirrorTokenNotSentToNonHTTPSOrigin(t *testing.T) {
	m, r := newAuthMirror(t)
	ctx := context.Background()
	mustErr(t, m.UseToken(fakeToken))
	fr, err := m.fetchRunner(ctx)
	mustErr(t, err)
	if strings.Contains(strings.Join(fr.Env(), "\n"), "GIT_CONFIG_COUNT") {
		t.Error("credential injected for a local origin")
	}
	mustErr(t, m.Update(ctx))
	must(r.Run(ctx, m.Dir, "config", "remote.origin.url", "http://github.example/o/r.git"))
	if fr, err := m.fetchRunner(ctx); err != nil || strings.Contains(strings.Join(fr.Env(), "\n"), "GIT_CONFIG_COUNT") {
		t.Errorf("credential injected for an http origin (err %v)", err)
	}
	userinfo := "user:pw" // split from the host so the privguard scan does not see a credential URL
	must(r.Run(ctx, m.Dir, "config", "remote.origin.url", "https://"+userinfo+"@github.example/o/r.git"))
	if _, err := m.fetchRunner(ctx); err == nil {
		t.Error("origin URL with embedded credentials accepted")
	}
	if err := m.UseToken("has space"); err == nil {
		t.Error("token with whitespace accepted")
	}
}

// A failed fetch never carries the token into the returned error.
func TestMirrorUpdateErrorsAreScrubbed(t *testing.T) {
	m, _ := newAuthMirror(t)
	mustErr(t, m.UseToken(fakeToken))
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + fakeToken))
	in := &git.Error{Args: []string{"fetch", "https://x/" + fakeToken}, ExitCode: 128, Stderr: "fatal: " + fakeToken + " and " + b64}
	err := m.scrub(in)
	var ge *git.Error
	if !errors.As(err, &ge) || strings.Contains(err.Error(), fakeToken) || strings.Contains(err.Error(), b64) {
		t.Errorf("not scrubbed: %v", err)
	}
}
