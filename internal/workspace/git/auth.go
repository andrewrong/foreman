package git

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// ErrOriginCredentials means the origin URL embeds a user or password.
var ErrOriginCredentials = errors.New("git: origin URL embeds credentials")

// ValidToken reports whether token can be used as a credential.
func ValidToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, " \t\r\n\x00")
}

// AuthEnv returns the environment entries (for Runner.WithEnv) that make git
// authenticate to origin with token (C-08: only foremand talks to GitHub):
//
//	http.https://<host>/.extraHeader = Authorization: Basic base64(x-access-token:<token>)
//
// injected through GIT_CONFIG_COUNT/KEY/VALUE, so the token is never in argv
// (visible to ps), never in a config file (git's credential helpers and
// "remote add" would store it in .git/config) and never in a script on disk.
// The header is scoped to the origin's host. It returns nil, nil for an
// origin that is not https: such an origin gets no credential at all.
func AuthEnv(origin, token string) ([]string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, nil
	}
	if u.User != nil {
		return nil, ErrOriginCredentials
	}
	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	return []string{
		"GIT_CONFIG_COUNT=2",
		// An empty value resets the helper list: no keychain or helper is consulted.
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=http.https://" + u.Host + "/.extraHeader", "GIT_CONFIG_VALUE_1=" + header,
	}, nil
}

// ScrubToken removes the token (raw and as the Basic payload) from a git
// error before it can reach a log or an event. Errors that are not *Error,
// or an empty token, pass through.
func ScrubToken(err error, token string) error {
	var ge *Error
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
