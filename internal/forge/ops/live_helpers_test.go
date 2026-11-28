package ops

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/meter"
)

// Live-test plumbing for TestForgeLiveSmoke. Gated by the env variables the
// smoke test documents; nothing here runs without them.

// statusRecorder notes the HTTP status of every request so each operation can
// be logged with the statuses it produced. It never touches headers or bodies.
type statusRecorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	seen []string
}

func (s *statusRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := s.next.RoundTrip(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.seen = append(s.seen, req.Method+" transport-error")
		return resp, err
	}
	s.seen = append(s.seen, fmt.Sprintf("%s %d", req.Method, resp.StatusCode))
	return resp, nil
}

func (s *statusRecorder) drain() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parts []string
	for i := 0; i < len(s.seen); {
		j := i
		for j < len(s.seen) && s.seen[j] == s.seen[i] {
			j++
		}
		if j-i > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", s.seen[i], j-i))
		} else {
			parts = append(parts, s.seen[i])
		}
		i = j
	}
	s.seen = nil
	return strings.Join(parts, ", ")
}

// liveEnv is a real Ops against the sandbox repository, on the real clock.
type liveEnv struct {
	t       *testing.T
	o       *Ops
	clk     clock.Clock
	rec     *statusRecorder
	repo    Repo
	base    string // default branch
	covered []string
	skipped []string

	mu       sync.Mutex
	branches []string
	openPRs  map[int]bool
}

// newLiveEnv skips the test when the gate variables are unset. The token is
// read from the file and only handed to the client.
func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	tokFile := os.Getenv("FOREMAN_TEST_GITHUB_TOKEN_FILE")
	owner, name, ok := strings.Cut(os.Getenv("FOREMAN_TEST_GITHUB_REPO"), "/")
	if tokFile == "" || !ok {
		t.Skip("FOREMAN_TEST_GITHUB_TOKEN_FILE / FOREMAN_TEST_GITHUB_REPO not set")
	}
	raw, err := os.ReadFile(tokFile)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	clk := clock.Real() // the live flow waits for real CI; only via internal/clock
	m := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m})
	rec := &statusRecorder{next: http.DefaultTransport}
	c := client.New(client.Config{Token: strings.TrimSpace(string(raw)), Meters: m, Clock: clk, HTTP: &http.Client{Transport: rec}})
	e := &liveEnv{
		t: t, clk: clk, rec: rec, repo: Repo{owner, name}, openPRs: map[int]bool{},
		o: New(Config{Client: c, Gate: g, Meters: m, Clock: clk}),
	}
	t.Cleanup(e.cleanup)
	return e
}

// step runs one operation and logs its id, name and the HTTP statuses seen.
// A successful step counts as covered.
func (e *liveEnv) step(id, name string, fn func() error) error {
	e.t.Helper()
	err := fn()
	e.t.Logf("%-5s %-20s http=[%s] err=%v", id, name, e.rec.drain(), err)
	if err == nil && !slices.Contains(e.covered, id) {
		e.covered = append(e.covered, id)
	}
	return err
}

// must is step that fails the test.
func (e *liveEnv) must(id, name string, fn func() error) {
	e.t.Helper()
	if err := e.step(id, name, fn); err != nil {
		e.t.Fatalf("%s %s: %v", id, name, err)
	}
}

// skip records an operation that could not run, with the reason.
func (e *liveEnv) skip(id, why string) {
	e.t.Logf("%-5s NOT COVERED: %s", id, why)
	e.skipped = append(e.skipped, id+": "+why)
}

// waitFor polls fn every interval until it reports done or limit passes on
// the clock. An fn error ends the wait.
func (e *liveEnv) waitFor(ctx context.Context, what string, limit, every time.Duration, fn func() (bool, error)) error {
	start := e.clk.Now()
	for {
		done, err := fn()
		if err != nil || done {
			return err
		}
		if e.clk.Now().Sub(start) >= limit {
			return fmt.Errorf("timed out after %s waiting for %s", limit, what)
		}
		select {
		case <-e.clk.After(every):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Fixture calls have no OP in forge.yaml (branches and commits are made by
// sessions through git, not by foreman). They go through the same Gate wrapper
// as every OP, using go-github directly: the only direct go-github use here.
func fixRead() spec  { return spec{id: "FIXTURE-read"} }
func fixWrite() spec { return spec{id: "FIXTURE-write", essential: true, write: true} }

// newBranch creates branch name from the default branch tip with one commit
// adding a file, and returns the commit sha.
func (e *liveEnv) newBranch(ctx context.Context, name string) (string, error) {
	r := e.repo
	var tip string
	err := e.o.do(ctx, call(), fixRead(), func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		ref, resp, err := gh.Git.GetRef(ctx, r.Owner, r.Name, "heads/"+e.base)
		if err == nil {
			tip = ref.GetObject().GetSHA()
		}
		return resp, err
	})
	if err != nil {
		return "", err
	}
	err = e.o.do(ctx, call(), fixWrite(), func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		_, resp, err := gh.Git.CreateRef(ctx, r.Owner, r.Name, github.CreateRef{Ref: "refs/heads/" + name, SHA: tip})
		return resp, err
	})
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.branches = append(e.branches, name)
	e.mu.Unlock()
	var sha string
	err = e.o.do(ctx, call(), fixWrite(), func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		msg := "test: foreman live fixture " + name
		out, resp, err := gh.Repositories.CreateFile(ctx, r.Owner, r.Name, ".foreman-live/"+name+".txt", &github.RepositoryContentFileOptions{
			Message: &msg, Content: []byte(name + "\n"), Branch: &name,
		})
		if err == nil {
			sha = out.GetSHA()
		}
		return resp, err
	})
	return sha, err
}

// defaultBranch reads the repository's default branch.
func (e *liveEnv) defaultBranch(ctx context.Context) error {
	return e.o.do(ctx, call(), fixRead(), func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		rp, resp, err := gh.Repositories.Get(ctx, e.repo.Owner, e.repo.Name)
		if err == nil {
			e.base = rp.GetDefaultBranch()
		}
		return resp, err
	})
}

func (e *liveEnv) uniqueName(tag string) string {
	return fmt.Sprintf("foreman-live-%d-%04x-%s", e.clk.Now().Unix(), rand.IntN(1<<16), tag)
}

// trackPR and untrackPR keep the set of PRs cleanup must close.
func (e *liveEnv) trackPR(n int) { e.mu.Lock(); e.openPRs[n] = true; e.mu.Unlock() }
func (e *liveEnv) untrackPR(n int) {
	e.mu.Lock()
	delete(e.openPRs, n)
	e.mu.Unlock()
}

// untrackBranch drops a branch the flow already deleted.
func (e *liveEnv) untrackBranch(b string) {
	e.mu.Lock()
	e.branches = slices.DeleteFunc(e.branches, func(x string) bool { return x == b })
	e.mu.Unlock()
}

// cleanup closes leftover PRs and deletes leftover branches. Errors are only
// logged.
func (e *liveEnv) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e.mu.Lock()
	prs, branches := slices.Collect(maps.Keys(e.openPRs)), slices.Clone(e.branches)
	e.mu.Unlock()
	for _, n := range prs {
		if err := e.o.PRClose(ctx, call(), e.repo, n); err != nil {
			e.t.Logf("cleanup: close PR %d: %v", n, err)
		}
	}
	for _, b := range branches {
		if err := e.o.RefDelete(ctx, call(), e.repo, b); err != nil {
			e.t.Logf("cleanup: delete branch %s: %v", b, err)
		}
	}
}

// statusOf is the HTTP status inside a typed forge error, 0 if there is none.
func statusOf(err error) int {
	var (
		nf client.NotFound
		cf client.Conflict
		un client.Unprocessable
		ua client.Unauthorized
		ap client.APIError
	)
	switch {
	case errors.As(err, &nf):
		return nf.Status
	case errors.As(err, &cf):
		return cf.Status
	case errors.As(err, &un):
		return un.Status
	case errors.As(err, &ua):
		return ua.Status
	case errors.As(err, &ap):
		return ap.Status
	}
	return 0
}

// mergeRetry merges with the pinned head sha. GitHub computes mergeability
// lazily and may refuse (405/409, reported as client.Conflict) for a while, so
// it retries until limit; each round tries squash, then a merge commit in
// case the repository forbids squash.
func (e *liveEnv) mergeRetry(ctx context.Context, n int, sha string, limit time.Duration) error {
	return e.waitFor(ctx, "merge to be accepted", limit, 10*time.Second, func() (bool, error) {
		var err error
		for _, method := range []string{"squash", "merge"} {
			var res MergeResult
			res, err = e.o.PRMerge(ctx, call(), e.repo, n, sha, method)
			if err == nil {
				if !res.Merged {
					return false, fmt.Errorf("merge answered without merged: %s", res.Message)
				}
				return true, nil
			}
			var cf client.Conflict
			if !errors.As(err, &cf) {
				return false, err
			}
		}
		return false, nil
	})
}
