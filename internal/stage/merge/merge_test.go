package merge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/forge/client"
	"github.com/andrewrong/foreman/internal/forge/ops"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/spec/model"
)

type fake struct {
	mu        sync.Mutex
	merged    bool
	mergeCode int // 0 = success
	body      map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /rate_limit":
		_ = json.NewEncoder(w).Encode(map[string]any{"resources": map[string]any{"core": map[string]any{"limit": 5000, "remaining": 5000, "reset": 1_800_003_600}}})
	case "GET /repos/acme/widgets/pulls/7":
		_ = json.NewEncoder(w).Encode(ops.PR{Number: 7, State: "open", Merged: f.merged})
	case "PUT /repos/acme/widgets/pulls/7/merge":
		_ = json.NewDecoder(r.Body).Decode(&f.body)
		if f.mergeCode != 0 {
			w.WriteHeader(f.mergeCode)
			_, _ = w.Write([]byte(`{"message":"Head branch was modified"}`))
			return
		}
		f.merged = true
		_ = json.NewEncoder(w).Encode(ops.MergeResult{SHA: "m3rge", Merged: true})
	default:
		http.NotFound(w, r)
	}
}

func newEnv(t *testing.T, f *fake) Env {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	m := meter.NewStore()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: m, Ledger: ledger.New()})
	o := ops.New(ops.Config{Client: client.New(client.Config{BaseURL: srv.URL, Token: "tok", Meters: m, Clock: clk}), Gate: g, Meters: m, Clock: clk})
	if err := o.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return Env{Ops: o, Repo: ops.Repo{Owner: "acme", Name: "widgets"}}
}

func TestMergeStagePinsHeadAndIsIdempotent(t *testing.T) {
	f := &fake{}
	env := newEnv(t, f)
	call := ops.Call{Priority: gate.P2}
	ev, err := Merge(context.Background(), env, call, 7, "abc123")
	if err != nil || ev == nil || ev.Name != model.EventMerged || ev.Head != "m3rge" {
		t.Fatalf("event %+v err %v", ev, err)
	}
	if f.body["sha"] != "abc123" {
		t.Errorf("merge was not pinned to the head sha: %v", f.body)
	}
	// Already merged on GitHub (crash after the merge): merged again, no 2nd PUT.
	f.body = nil
	ev, err = Merge(context.Background(), env, call, 7, "abc123")
	if err != nil || ev == nil || ev.Name != model.EventMerged || f.body != nil {
		t.Fatalf("repeat: %+v err %v body %v", ev, err, f.body)
	}
}

func TestMergeStageConflictBecomesEvent(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusMethodNotAllowed} {
		f := &fake{mergeCode: code}
		ev, err := Merge(context.Background(), newEnv(t, f), ops.Call{Priority: gate.P2}, 7, "abc123")
		if err != nil || ev == nil || ev.Name != model.EventMergeConflict {
			t.Fatalf("status %d: %+v err %v", code, ev, err)
		}
	}
}
