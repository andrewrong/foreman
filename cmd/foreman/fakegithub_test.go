package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/andrewrong/foreman/internal/forge/ops"
)

// fakeGitHub is an httptest GitHub for owner "acme", repo "widgets": it keeps
// one PR, serves L1 check runs that are in progress on the first read after
// the PR exists and green afterwards, and records every write.
type fakeGitHub struct {
	mu sync.Mutex
	// headOf resolves a branch of the (local bare) remote to its sha.
	headOf func(branch string) string
	// baseConclusion is the conclusion of the latest push run on main.
	baseConclusion string

	pr          *ops.PR
	checkReads  int
	created     []ops.NewPR
	merges      []map[string]string
	requests    []string
	mergeResult string
}

func newFakeGitHub(t *testing.T, headOf func(string) string) (*fakeGitHub, string) {
	t.Helper()
	f := &fakeGitHub{headOf: headOf, baseConclusion: "success", mergeResult: "m3rged"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	out := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	const base = "/repos/acme/widgets"
	switch r.Method + " " + r.URL.Path {
	case "GET /rate_limit":
		out(map[string]any{"resources": map[string]any{
			"core":    map[string]any{"limit": 5000, "remaining": 5000, "reset": 1_800_003_600},
			"graphql": map[string]any{"limit": 5000, "remaining": 5000, "reset": 1_800_003_600}}})
	case "GET " + base + "/pulls":
		list := []ops.PR{}
		if f.pr != nil && f.pr.State == "open" {
			list = append(list, *f.pr)
		}
		out(list)
	case "POST " + base + "/pulls":
		var in ops.NewPR
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.created = append(f.created, in)
		f.pr = &ops.PR{Number: 7, State: "open", Title: in.Title, Head: ops.Ref{Ref: in.Head, SHA: f.headOf(in.Head)},
			Base: ops.Ref{Ref: in.Base}}
		w.WriteHeader(http.StatusCreated)
		out(f.pr)
	case "GET " + base + "/pulls/7":
		out(f.pr)
	case "PUT " + base + "/pulls/7/merge":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.merges = append(f.merges, body)
		f.pr.State, f.pr.Merged = "closed", true
		out(ops.MergeResult{SHA: f.mergeResult, Merged: true})
	case "GET " + base + "/actions/runs":
		out(map[string]any{"workflow_runs": []ops.Run{{ID: 1, Status: "completed", Conclusion: f.baseConclusion,
			Event: "push", HeadBranch: "main"}}})
	default:
		if strings.HasPrefix(r.URL.Path, base+"/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs") {
			f.checkReads++
			run := ops.CheckRun{ID: 1, Name: "ci", HeadSHA: f.pr.Head.SHA, Status: "completed", Conclusion: "success"}
			if f.checkReads == 1 {
				run.Status, run.Conclusion = "in_progress", ""
			}
			out(map[string]any{"check_runs": []ops.CheckRun{run}})
			return
		}
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) count(req string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == req {
			n++
		}
	}
	return n
}
