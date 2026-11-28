package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// env is a real temp sqlite work.db with a fake clock and an Engine.
type env struct {
	t   *testing.T
	ctx context.Context
	db  *sqlite.DB
	clk *clock.Fake
	eng *Engine
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	s, err := sqlite.Open(context.Background(), t.TempDir(), clk)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &env{t: t, ctx: context.Background(), db: s.Work, clk: clk, eng: NewEngine(clk, Config{})}
}

func testSpec() model.Spec {
	return model.Spec{
		Title: "t", Goal: "g", Change: []string{"c"},
		Outcome: []model.Acceptance{{ID: "A1", EARS: "e", Verify: model.VerifyTest, VerifyDetail: "d", SandboxOK: true}},
	}
}

func (e *env) req(id string, mut ...func(*model.Requirement)) {
	e.t.Helper()
	r := model.Requirement{ID: id, Kind: model.KindFeature, Source: model.SourceOwner, Priority: model.P2,
		SpecVersion: 1, Spec: testSpec(), Branch: "foreman/" + id, Hold: model.HoldNone,
		Status: model.StatusSubmitted, Size: model.SizeS}
	for _, m := range mut {
		m(&r)
	}
	if err := InsertRequirement(e.ctx, e.db, r); err != nil {
		e.t.Fatalf("insert requirement: %v", err)
	}
}

// step inserts a step "<req>/S<n>" (n = next free) and returns its id.
func (e *env) step(req string, typ model.StepType, state model.StepState, stage model.Stage, mut ...func(*model.Step)) string {
	e.t.Helper()
	s := model.Step{ReqID: req, Type: typ, Goal: "goal", Acceptance: []string{"A1"}, Touches: []string{"pipeline"},
		State: state, Stage: stage}
	if state == model.StateWaiting {
		s.Reason = "deps"
	}
	for _, m := range mut {
		m(&s)
	}
	if err := InsertStep(e.ctx, e.db, e.clk, s); err != nil {
		e.t.Fatalf("insert step: %v", err)
	}
	var id string
	e.read(`SELECT id FROM steps WHERE req_id = ? ORDER BY seq DESC LIMIT 1`, []any{req}, &id)
	return id
}

func (e *env) read(q string, args []any, dst ...any) {
	e.t.Helper()
	err := e.db.Read(e.ctx, func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(dst...) })
	if err != nil {
		e.t.Fatalf("query %q: %v", q, err)
	}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	err := e.db.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err })
	if err != nil {
		e.t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	e.read(q, args, &n)
	return n
}

// stepState returns "state/stage[/reason]" plus resume_stage and counters.
type stepInfo struct {
	State, Stage, Reason, Resume string
	Counters                     map[string]int
}

func (e *env) info(id string) stepInfo {
	e.t.Helper()
	var s stepInfo
	var reason, resume sql.NullString
	var counters string
	e.read(`SELECT state, stage, reason, resume_stage, counters FROM steps WHERE id = ?`, []any{id},
		&s.State, &s.Stage, &reason, &resume, &counters)
	s.Reason, s.Resume = reason.String, resume.String
	if err := json.Unmarshal([]byte(counters), &s.Counters); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) hold(reqID string) string {
	e.t.Helper()
	var h string
	e.read(`SELECT hold FROM requirements WHERE id = ?`, []any{reqID}, &h)
	return h
}

func (e *env) reqCounter(reqID, name string) int {
	e.t.Helper()
	var n int
	e.read(`SELECT COALESCE(json_extract(req_counters, '$.' || ?), 0) FROM requirements WHERE id = ?`, []any{name, reqID}, &n)
	return n
}

// runs returns "role" (and model) of all runs in creation order.
type runInfo struct{ ID, Role, Model, Status, StepID, Scope string }

func (e *env) runs() []runInfo {
	e.t.Helper()
	var out []runInfo
	err := e.db.Read(e.ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, role, COALESCE(model, ''), status, COALESCE(step_id, ''), COALESCE(scope, '')
			FROM runs ORDER BY length(id), id`) // base36 ids: shorter is older
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r runInfo
			if err := rows.Scan(&r.ID, &r.Role, &r.Model, &r.Status, &r.StepID, &r.Scope); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) transition(stepID string, ev Event) Outcome {
	e.t.Helper()
	out, err := e.eng.Transition(e.ctx, e.db, stepID, ev)
	if err != nil {
		e.t.Fatalf("Transition(%s, %s): %v", stepID, ev.Name, err)
	}
	e.t.Logf("Transition %-14s %-22s -> RowID %-9s to %+v nested=%v", stepID, ev.Name, out.RowID, out.To, rowIDs(out.Reqs))
	return out
}

func (e *env) transitionReq(reqID string, ev Event) ReqOutcome {
	e.t.Helper()
	out, err := e.eng.TransitionReq(e.ctx, e.db, reqID, ev)
	if err != nil {
		e.t.Fatalf("TransitionReq(%s, %s): %v", reqID, ev.Name, err)
	}
	e.t.Logf("TransitionReq %-12s %-22s -> RowID %-9s to %s followups=%v", reqID, ev.Name, out.RowID, out.To, rowIDsReq(out.Followups))
	return out
}

func rowIDs(rs []ReqOutcome) []string { return rowIDsReq(rs) }

func rowIDsReq(rs []ReqOutcome) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.RowID)
	}
	return out
}

func wantRow(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("RowID = %q, want %q", got, want)
	}
}

func wantInfo(t *testing.T, got stepInfo, state, stage, reason string) {
	t.Helper()
	if got.State != state || got.Stage != stage || got.Reason != reason {
		t.Fatalf("step is %s/%s/%s, want %s/%s/%s", got.State, got.Stage, got.Reason, state, stage, reason)
	}
}

func dispatchOK() Event {
	return Event{Name: "dispatch", Deps: "done", Lease: "ok", Budget: "ok"}
}
