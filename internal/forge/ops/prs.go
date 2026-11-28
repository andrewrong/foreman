package ops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/go-github/v93/github"

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

func toPR(p *github.PullRequest) PR {
	return PR{
		Number: p.GetNumber(), State: p.GetState(), Title: p.GetTitle(), Draft: p.GetDraft(), Merged: p.GetMerged(),
		Mergeable: p.Mergeable, MergeableState: p.GetMergeableState(), HTMLURL: p.GetHTMLURL(),
		Head: Ref{Ref: p.GetHead().GetRef(), SHA: p.GetHead().GetSHA()},
		Base: Ref{Ref: p.GetBase().GetRef(), SHA: p.GetBase().GetSHA()},
	}
}

// PRCreate is OP-01.
func (o *Ops) PRCreate(ctx context.Context, call Call, r Repo, in NewPR) (PR, error) {
	var pr PR
	err := o.do(ctx, call, opPRCreate, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		p, resp, err := gh.PullRequests.Create(ctx, r.Owner, r.Name, github.CreatePullRequest{
			Title: &in.Title, Head: in.Head, Base: in.Base, Body: &in.Body, Draft: &in.Draft,
		})
		if p != nil {
			pr = toPR(p)
		}
		return resp, err
	})
	return pr, err
}

// PRListOpen is OP-02: one page (100) of open PRs, conditional GET. The list
// has no mergeable or check data (FG-7).
func (o *Ops) PRListOpen(ctx context.Context, call Call, r Repo) ([]PR, error) {
	var prs []PR
	err := o.do(ctx, call, opPRListOpen, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		ps, resp, err := gh.PullRequests.List(ctx, r.Owner, r.Name, &github.PullRequestListOptions{
			State: "open", ListOptions: github.ListOptions{PerPage: 100},
		})
		for _, p := range ps {
			prs = append(prs, toPR(p))
		}
		return resp, err
	})
	return prs, err
}

// PRGet is OP-03.
func (o *Ops) PRGet(ctx context.Context, call Call, r Repo, n int) (PR, error) {
	var pr PR
	err := o.do(ctx, call, opPRGet, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		p, resp, err := gh.PullRequests.Get(ctx, r.Owner, r.Name, n)
		if p != nil {
			pr = toPR(p)
		}
		return resp, err
	})
	return pr, err
}

// PRMerge is OP-04. sha pins the head: a changed head gives client.Conflict
// (409). GitHub's 405 "not mergeable" is also reported as client.Conflict.
func (o *Ops) PRMerge(ctx context.Context, call Call, r Repo, n int, sha, method string) (MergeResult, error) {
	if method == "" {
		method = "squash"
	}
	var res MergeResult
	err := o.do(ctx, call, opPRMerge, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		m, resp, err := gh.PullRequests.Merge(ctx, r.Owner, r.Name, n, "", &github.PullRequestOptions{SHA: sha, MergeMethod: method})
		if m != nil {
			res = MergeResult{SHA: m.GetSHA(), Merged: m.GetMerged(), Message: m.GetMessage()}
		}
		return resp, err
	})
	var api client.APIError
	if errors.As(err, &api) && api.Status == http.StatusMethodNotAllowed {
		return res, client.Conflict{Base: api.Base}
	}
	return res, err
}

// PRUpdateBranch is OP-05; it returns GitHub's message (202 when queued).
func (o *Ops) PRUpdateBranch(ctx context.Context, call Call, r Repo, n int, expectedHead string) (string, error) {
	var msg string
	err := o.do(ctx, call, opPRUpdateBranch, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		body := github.UpdatePullRequestBranchRequest{}
		if expectedHead != "" {
			body.ExpectedHeadSHA = &expectedHead
		}
		out, resp, err := gh.PullRequests.UpdateBranch(ctx, r.Owner, r.Name, n, body)
		var acc *github.AcceptedError // 202: queued, the payload is in Raw
		if errors.As(err, &acc) {
			var m struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(acc.Raw, &m)
			msg, err = m.Message, nil
		} else if out != nil {
			msg = out.GetMessage()
		}
		return resp, err
	})
	return msg, err
}

// PRComment is OP-06.
func (o *Ops) PRComment(ctx context.Context, call Call, r Repo, n int, text string) error {
	return o.do(ctx, call, opPRComment, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		_, resp, err := gh.Issues.CreateComment(ctx, r.Owner, r.Name, n, github.IssueCommentRequest{Body: text})
		return resp, err
	})
}

// PRClose is OP-13.
func (o *Ops) PRClose(ctx context.Context, call Call, r Repo, n int) error {
	return o.do(ctx, call, opPRClose, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		state := "closed"
		_, resp, err := gh.PullRequests.Edit(ctx, r.Owner, r.Name, n, &github.PullRequest{State: &state})
		return resp, err
	})
}

// RefDelete is OP-14: delete the remote branch refs/heads/<branch>.
func (o *Ops) RefDelete(ctx context.Context, call Call, r Repo, branch string) error {
	return o.do(ctx, call, opRefDelete, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		return gh.Git.DeleteRef(ctx, r.Owner, r.Name, "heads/"+branch)
	})
}

// Protection is the OP-12 outcome (D-050): Available is false when GitHub
// answers 403 or 404 ("protection: unavailable").
type Protection struct {
	Available bool
	Raw       []byte
}

// BranchProtectionGet is OP-12.
func (o *Ops) BranchProtectionGet(ctx context.Context, call Call, r Repo, branch string) (Protection, error) {
	var raw []byte
	err := o.do(ctx, call, opBranchProt, func(ctx context.Context, gh *github.Client) (*github.Response, error) {
		p, resp, err := gh.Repositories.GetBranchProtection(ctx, r.Owner, r.Name, branch)
		if err == nil {
			raw, err = json.Marshal(p)
		}
		return resp, err
	})
	var nf client.NotFound
	var ua client.Unauthorized
	if errors.As(err, &nf) || errors.As(err, &ua) || errors.Is(err, github.ErrBranchNotProtected) {
		return Protection{}, nil
	}
	if err != nil {
		return Protection{}, err
	}
	return Protection{Available: true, Raw: raw}, nil
}
