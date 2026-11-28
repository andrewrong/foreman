package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrewrong/foreman/internal/workspace/git"
)

// UseToken sets the credential Update fetches from the remote with (C-08:
// only foremand talks to GitHub). It reaches git the same way the push
// token does (git.AuthEnv): only through the environment of the fetch
// process, never in argv or in the mirror's config file, scoped to the
// origin's https host. Sessions can read neither (access.yaml R0: only
// objects/ is readable).
func (m *Mirror) UseToken(token string) error {
	if !git.ValidToken(token) {
		return errors.New("session: invalid fetch token")
	}
	m.mu.Lock()
	m.token = token
	m.mu.Unlock()
	return nil
}

// fetchRunner is the runner for Update: m.r without a token or for an origin
// that is not https, otherwise a copy carrying the credential environment.
func (m *Mirror) fetchRunner(ctx context.Context) (*git.Runner, error) {
	m.mu.Lock()
	token := m.token
	m.mu.Unlock()
	if token == "" {
		return m.r, nil
	}
	origin, err := m.RemoteURL(ctx)
	if err != nil {
		return nil, err
	}
	env, err := git.AuthEnv(origin, token)
	if errors.Is(err, git.ErrOriginCredentials) {
		return nil, fmt.Errorf("session: origin URL of %s embeds credentials", m.Dir)
	}
	if err != nil || env == nil {
		return m.r, err
	}
	return m.r.WithEnv(env...), nil
}

func (m *Mirror) scrub(err error) error {
	m.mu.Lock()
	token := m.token
	m.mu.Unlock()
	return git.ScrubToken(err, token)
}
