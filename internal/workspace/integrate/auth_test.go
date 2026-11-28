package integrate

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

const fakeToken = "tok-0123456789abcdef"

func TestPushTokenGoesOnlyThroughTheEnvironment(t *testing.T) {
	w := newWorld(t, "main")
	ctx := context.Background()
	mustErr(t, w.integ.UseToken(fakeToken))
	must(w.r.Run(ctx, w.integ.Path, "config", "remote.origin.url", "https://github.example/o/r.git"))

	pr, err := w.integ.authRunner(ctx)
	mustErr(t, err)
	env := strings.Join(pr.Env(), "\n")
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + fakeToken))
	for _, want := range []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=\n",
		"GIT_CONFIG_KEY_1=http.https://github.example/.extraHeader",
		"GIT_CONFIG_VALUE_1=Authorization: Basic " + b64,
	} {
		if !strings.Contains(env+"\n", want) {
			t.Errorf("credential env lacks %q", want)
		}
	}
	// Argv never carries the token, and only the push runner has it.
	if argv := strings.Join(pr.Argv("push", "origin", "HEAD:refs/heads/foreman/r1"), " "); strings.Contains(argv, fakeToken) || strings.Contains(argv, b64) {
		t.Errorf("token in argv: %s", argv)
	}
	if strings.Contains(strings.Join(w.r.Env(), "\n"), fakeToken) || strings.Contains(strings.Join(w.integ.r.Env(), "\n"), b64) {
		t.Error("the directory's ordinary runner carries the credential")
	}
}

// A credential is never offered to an origin that is not https.
func TestPushTokenNotSentToNonHTTPSOrigin(t *testing.T) {
	w := newWorld(t, "main") // origin is a local path
	mustErr(t, w.integ.UseToken(fakeToken))
	pr, err := w.integ.authRunner(context.Background())
	mustErr(t, err)
	if strings.Contains(strings.Join(pr.Env(), "\n"), "GIT_CONFIG_COUNT") {
		t.Error("credential injected for a local origin")
	}
	for _, u := range []string{"http://github.example/o/r.git", "ssh://git@github.example/o/r.git"} {
		must(w.r.Run(context.Background(), w.integ.Path, "config", "remote.origin.url", u))
		pr, err := w.integ.authRunner(context.Background())
		mustErr(t, err)
		if strings.Contains(strings.Join(pr.Env(), "\n"), "GIT_CONFIG_COUNT") {
			t.Errorf("credential injected for %s", u)
		}
	}
	must(w.r.Run(context.Background(), w.integ.Path, "config", "remote.origin.url", "https://user:pw@github.example/o/r.git"))
	if _, err := w.integ.authRunner(context.Background()); err == nil {
		t.Error("origin URL with embedded credentials accepted")
	}
}

func TestPushErrorsAreScrubbed(t *testing.T) {
	w := newWorld(t, "main")
	mustErr(t, w.integ.UseToken(fakeToken))
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + fakeToken))
	in := &git.Error{Args: []string{"push", "https://x/" + fakeToken}, ExitCode: 128, Stderr: "fatal: " + fakeToken + " and " + b64}
	err := w.integ.scrub(in)
	var ge *git.Error
	if !errors.As(err, &ge) || strings.Contains(err.Error(), fakeToken) || strings.Contains(err.Error(), b64) {
		t.Errorf("not scrubbed: %v", err)
	}
	if err := w.integ.UseToken("has space"); err == nil {
		t.Error("token with whitespace accepted")
	}
}
