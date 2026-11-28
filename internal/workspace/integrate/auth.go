package integrate

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// UseToken sets the credential Push and its ls-remote checks authenticate
// with (C-08: only foremand pushes). The token reaches git only through the
// environment of those processes, as config injected by GIT_CONFIG_COUNT/
// KEY/VALUE:
//
//	http.https://<host>/.extraHeader = Authorization: Basic base64(x-access-token:<token>)
//
// so it is never in argv (visible to ps), never in a config file (git's own
// credential helpers and "remote add" would store it in .git/config), and
// never in a helper script on disk. The header is scoped to the origin's
// host; an origin that is not https gets no credential at all.
func (d *Dir) UseToken(token string) error {
	if token == "" || strings.ContainsAny(token, " \t\r\n\x00") {
		return errors.New("integrate: invalid push token")
	}
	d.mu.Lock()
	d.token = token
	d.mu.Unlock()
	return nil
}

// authRunner returns the runner for the push: d.r when no token is set or the
// origin is not an https URL, otherwise a copy carrying the credential
// environment. Only the copy has the credential; every other git call of the
// directory (fetch from sessions, merge, rebase) runs without it.
func (d *Dir) authRunner(ctx context.Context) (*git.Runner, error) {
	d.mu.Lock()
	token := d.token
	d.mu.Unlock()
	if token == "" {
		return d.r, nil
	}
	origin, err := d.r.Line(ctx, d.Path, "config", "--get", "remote.origin.url")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return d.r, nil
	}
	if u.User != nil {
		return nil, fmt.Errorf("integrate: origin URL of %s embeds credentials", d.Path)
	}
	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	return d.r.WithEnv(
		"GIT_CONFIG_COUNT=2",
		// An empty value resets the helper list: no keychain or helper is consulted.
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=http.https://"+u.Host+"/.extraHeader", "GIT_CONFIG_VALUE_1="+header,
	), nil
}

// scrub removes the token (raw and as the Basic payload) from a git error
// before it can reach a log or an event.
func (d *Dir) scrub(err error) error {
	d.mu.Lock()
	token := d.token
	d.mu.Unlock()
	var ge *git.Error
	if token == "" || !errors.As(err, &ge) {
		return err
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	clean := strings.NewReplacer(token, "[redacted]", b64, "[redacted]").Replace
	c := *ge
	c.Stderr = clean(ge.Stderr)
	c.Args = make([]string, len(ge.Args))
	for i, a := range ge.Args {
		c.Args[i] = clean(a)
	}
	return &c
}
