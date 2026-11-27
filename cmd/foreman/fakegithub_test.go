package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/andrewrong/foreman/internal/forge/ops"
)

// ciOutcome is how one attempt of the L1 workflow ends.
type ciOutcome struct {
	Conclusion string // success|failure|cancelled
	Log        string // job log text served by OP-10
}

// ciHead is the fake CI's memory of one head sha. phase: -1 the failed jobs
// were just rerun (the old check run is still listed, the workflow run is
// queued again), 0 not started reading, 1 finished.
type ciHead struct {
	attempt, phase int
}

// fakeGitHub is an httptest GitHub for owner "acme", repo "widgets": it keeps
// one PR, runs L1 as a small state machine per head sha (in progress on the
// first read, then the outcome plan gives; a rerun starts a new attempt) and
// records every write.
type fakeGitHub struct {
	mu sync.Mutex
	// headOf resolves a branch of the (local bare) remote to its sha.
	headOf func(branch string) string
	// baseConclusion is the conclusion of the latest push run on main.
	baseConclusion string
	// plan gives the outcome of attempt n (0 = first run, 1 = after a rerun) of
	// L1 on a head; nil means success.
	plan func(head string, attempt int) ciOutcome
	// pending keeps every L1 run in progress forever.
	pending bool
	// limited answers the PR list with a primary rate limit (reset one hour
	// after resetUnix).
	limited   bool
	resetUnix int64
	// updateBranch merges the default branch into the PR branch of the remote
	// (what GitHub's update-branch does); nil answers 422.
	updateBranch func(branch string) error

	pr          *ops.PR
	heads       map[string]*ciHead
	logs        map[int64]string
	created     []ops.NewPR
	merges      []map[string]string
	requests    []string
	mergeResult string
}

func newFakeGitHub(t *testing.T, headOf func(string) string) (*fakeGitHub, string) {
	t.Helper()
	f := &fakeGitHub{headOf: headOf, baseConclusion: "success", mergeResult: "m3rged",
		heads: map[string]*ciHead{}, logs: map[int64]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeGitHub) outcome(head string, attempt int) ciOutcome {
	if f.plan == nil {
		return ciOutcome{Conclusion: "success"}
	}
	return f.plan(head, attempt)
}

func (f *fakeGitHub) head(sha string) *ciHead {
	h := f.heads[sha]
	if h == nil {
		h = &ciHead{}
		f.heads[sha] = h
	}
	return h
}

// checkRuns is OP-07 for a head: each read advances the fake CI one phase.
func (f *fakeGitHub) checkRuns(head string) []ops.CheckRun {
	h := f.head(head)
	run := ops.CheckRun{ID: int64(100 + h.attempt), Name: "ci", HeadSHA: head, Status: "in_progress"}
	if f.pending {
		return []ops.CheckRun{run}
	}
	switch h.phase {
	case -1: // stale: the previous attempt's failure is still listed
		old := f.outcome(head, h.attempt-1)
		run.ID, run.Status, run.Conclusion = int64(100+h.attempt-1), "completed", old.Conclusion
		h.phase = 0
	case 0:
		h.phase = 1
	default:
		out := f.outcome(head, h.attempt)
		run.Status, run.Conclusion = "completed", out.Conclusion
		f.logs[run.ID] = out.Log
	}
	return []ops.CheckRun{run}
}

// workflowRuns is OP-08 filtered by head sha: one workflow run per head.
func (f *fakeGitHub) workflowRuns(head string) []ops.Run {
	h, ok := f.heads[head]
	if !ok {
		return []ops.Run{}
	}
	run := ops.Run{ID: 41, Name: "ci", Status: "in_progress", HeadSHA: head}
	if h.phase == 1 && !f.pending {
		run.Status, run.Conclusion = "completed", f.outcome(head, h.attempt).Conclusion
	}
	return []ops.Run{run}
}

func (f *fakeGitHub) refreshPR() {
	if f.pr != nil {
		f.pr.Head.SHA = f.headOf(f.pr.Head.Ref)
	}
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
		if f.limited {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(f.resetUnix+3600, 10))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		list := []ops.PR{}
		if f.pr != nil && f.pr.State == "open" {
			f.refreshPR()
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
		f.refreshPR()
		out(f.pr)
	case "PUT " + base + "/pulls/7/update-branch":
		if f.updateBranch == nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
			return
		}
		if err := f.updateBranch(f.pr.Head.Ref); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"message":%q}`, err.Error())))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		out(map[string]string{"message": "Updating pull request branch."})
	case "PUT " + base + "/pulls/7/merge":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.merges = append(f.merges, body)
		f.refreshPR()
		if body["sha"] != f.pr.Head.SHA { // GitHub pins the merge to the head sha
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Head branch was modified"}`))
			return
		}
		f.pr.State, f.pr.Merged = "closed", true
		out(ops.MergeResult{SHA: f.mergeResult, Merged: true})
	case "GET " + base + "/actions/runs":
		if head := r.URL.Query().Get("head_sha"); head != "" {
			out(map[string]any{"workflow_runs": f.workflowRuns(head)})
			return
		}
		out(map[string]any{"workflow_runs": []ops.Run{{ID: 1, Status: "completed", Conclusion: f.baseConclusion,
			Event: "push", HeadBranch: "main"}}})
	default:
		switch {
		case strings.HasPrefix(r.URL.Path, base+"/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
			sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, base+"/commits/"), "/check-runs")
			out(map[string]any{"check_runs": f.checkRuns(sha)})
		case strings.HasPrefix(r.URL.Path, base+"/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs"):
			id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, base+"/actions/jobs/"), "/logs"), 10, 64)
			if text, ok := f.logs[id]; ok {
				_, _ = w.Write([]byte(text))
				return
			}
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, base+"/actions/runs/") && strings.HasSuffix(r.URL.Path, "/rerun-failed-jobs"):
			for _, h := range f.heads {
				if h.phase == 1 { // the failed jobs start again; the old check run lingers
					h.attempt++
					h.phase = -1
				}
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
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

// reruns is the number of OP-09 calls so far.
func (f *fakeGitHub) reruns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, "POST /repos/acme/widgets/actions/runs/") && strings.HasSuffix(r, "/rerun-failed-jobs") {
			n++
		}
	}
	return n
}

// set runs fn with the fake locked (tests change its knobs between ticks).
func (f *fakeGitHub) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}
