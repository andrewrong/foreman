package integrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// UseToken sets the credential Push and its ls-remote checks authenticate
// with (C-08: only foremand pushes). The token reaches git only through the
// environment of those processes (git.AuthEnv): never in argv, never in a
// config file, scoped to the origin's host; an origin that is not https gets
// no credential at all.
func (d *Dir) UseToken(token string) error {
	if !git.ValidToken(token) {
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
	env, err := git.AuthEnv(origin, token)
	if errors.Is(err, git.ErrOriginCredentials) {
		return nil, fmt.Errorf("integrate: origin URL of %s embeds credentials", d.Path)
	}
	if err != nil || env == nil {
		return d.r, err
	}
	return d.r.WithEnv(env...), nil
}

// scrub removes the token from a git error before it can reach a log or an event.
func (d *Dir) scrub(err error) error {
	d.mu.Lock()
	token := d.token
	d.mu.Unlock()
	return git.ScrubToken(err, token)
}
