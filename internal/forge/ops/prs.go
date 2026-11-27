package ops

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/andrewrong/foreman/internal/forge/client"
)

// PR is the subset of a pull request foreman reads.
type PR struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	HTMLURL        string `json:"html_url"`
	Head           Ref    `json:"head"`
	Base           Ref    `json:"base"`
}

// Ref is a PR head or base.
type Ref struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// NewPR is the OP-01 request.
type NewPR struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body,omitempty"`
	Draft bool   `json:"draft,omitempty"`
}

// MergeResult is the OP-04 response.
type MergeResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// PRCreate is OP-01.
func (o *Ops) PRCreate(ctx context.Context, call Call, r Repo, in NewPR) (PR, error) {
	var pr PR
	_, err := o.sendJSON(ctx, call, opPRCreate, http.MethodPost, r.path("/pulls"), in, &pr)
	return pr, err
}

// PRListOpen is OP-02: one page (100) of open PRs, conditional GET. The list
// has no mergeable or check data (FG-7).
func (o *Ops) PRListOpen(ctx context.Context, call Call, r Repo) ([]PR, error) {
	var prs []PR
	q := url.Values{"state": {"open"}, "per_page": {"100"}}
	_, err := o.getJSON(ctx, call, opPRListOpen, r.path("/pulls"), q, &prs)
	return prs, err
}

// PRGet is OP-03.
func (o *Ops) PRGet(ctx context.Context, call Call, r Repo, n int) (PR, error) {
	var pr PR
	_, err := o.getJSON(ctx, call, opPRGet, r.path("/pulls/%d", n), nil, &pr)
	return pr, err
}

// PRMerge is OP-04. sha pins the head: a changed head gives client.Conflict
// (409). GitHub's 405 "not mergeable" is also reported as client.Conflict.
func (o *Ops) PRMerge(ctx context.Context, call Call, r Repo, n int, sha, method string) (MergeResult, error) {
	if method == "" {
		method = "squash"
	}
	var res MergeResult
	body := map[string]string{"sha": sha, "merge_method": method}
	resp, err := o.sendJSON(ctx, call, opPRMerge, http.MethodPut, r.path("/pulls/%d/merge", n), body, &res)
	var api client.APIError
	if errors.As(err, &api) && resp.Status == http.StatusMethodNotAllowed {
		return res, client.Conflict{Base: api.Base}
	}
	return res, err
}

// PRUpdateBranch is OP-05; it returns GitHub's message (202 when queued).
func (o *Ops) PRUpdateBranch(ctx context.Context, call Call, r Repo, n int, expectedHead string) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	body := map[string]string{}
	if expectedHead != "" {
		body["expected_head_sha"] = expectedHead
	}
	_, err := o.sendJSON(ctx, call, opPRUpdateBranch, http.MethodPut, r.path("/pulls/%d/update-branch", n), body, &out)
	return out.Message, err
}

// PRComment is OP-06.
func (o *Ops) PRComment(ctx context.Context, call Call, r Repo, n int, text string) error {
	_, err := o.sendJSON(ctx, call, opPRComment, http.MethodPost, r.path("/issues/%d/comments", n), map[string]string{"body": text}, nil)
	return err
}

// PRClose is OP-13.
func (o *Ops) PRClose(ctx context.Context, call Call, r Repo, n int) error {
	_, err := o.sendJSON(ctx, call, opPRClose, http.MethodPatch, r.path("/pulls/%d", n), map[string]string{"state": "closed"}, nil)
	return err
}

// RefDelete is OP-14: delete the remote branch refs/heads/<branch>.
func (o *Ops) RefDelete(ctx context.Context, call Call, r Repo, branch string) error {
	_, err := o.sendJSON(ctx, call, opRefDelete, http.MethodDelete, r.path("/git/refs/heads/%s", escapeRef(branch)), nil, nil)
	return err
}

// Protection is the OP-12 outcome (D-050): Available is false when GitHub
// answers 403 or 404 ("protection: unavailable").
type Protection struct {
	Available bool
	Raw       []byte
}

// BranchProtectionGet is OP-12.
func (o *Ops) BranchProtectionGet(ctx context.Context, call Call, r Repo, branch string) (Protection, error) {
	resp, err := o.run(ctx, call, opBranchProt, client.Request{Method: http.MethodGet, Path: r.path("/branches/%s/protection", escapeRef(branch))})
	var nf client.NotFound
	var ua client.Unauthorized
	if errors.As(err, &nf) || errors.As(err, &ua) {
		return Protection{}, nil
	}
	if err != nil {
		return Protection{}, err
	}
	return Protection{Available: true, Raw: resp.Body}, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
