package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// execer is the part of *sql.Tx the package uses.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// timeLayout has fixed-width nanoseconds so text order equals time order.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("state: marshal %T: %v", v, err))
	}
	return string(b)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// stepRec is a step row held in memory while a transaction runs.
type stepRec struct {
	id, reqID, typ, codeRole, goal string
	seq                            int
	state, stage, reason           string
	resume                         string
	deadline                       string
	counters                       map[string]int
	guidance, denial               string
	grants                         []string
	touches                        []string
	acceptance                     []string
	lastReviewed                   string
	dirty                          bool
}

func (s *stepRec) view() StepView {
	v := StepView{State: s.state, Stage: s.stage, Type: s.typ, ResumeStage: s.resume, Counters: map[string]int{}}
	if s.state == "waiting" {
		v.Reason = s.reason
	}
	for k, n := range s.counters {
		v.Counters[k] = n
	}
	return v
}

const stepCols = `id, req_id, seq, type, code_role, goal, state, stage, resume_stage, reason, deadline,
	counters, guidance, denial, access_grants, touches, acceptance, last_reviewed_sha`

func scanStep(row interface{ Scan(...any) error }) (*stepRec, error) {
	var s stepRec
	var resume, reason, deadline, guidance, denial, last sql.NullString
	var counters, grants, touches, acc string
	if err := row.Scan(&s.id, &s.reqID, &s.seq, &s.typ, &s.codeRole, &s.goal, &s.state, &s.stage, &resume,
		&reason, &deadline, &counters, &guidance, &denial, &grants, &touches, &acc, &last); err != nil {
		return nil, err
	}
	s.resume, s.reason, s.deadline = resume.String, reason.String, deadline.String
	s.guidance, s.denial, s.lastReviewed = guidance.String, denial.String, last.String
	s.counters = map[string]int{}
	for _, p := range []struct {
		text string
		dst  any
	}{{counters, &s.counters}, {grants, &s.grants}, {touches, &s.touches}, {acc, &s.acceptance}} {
		if err := json.Unmarshal([]byte(p.text), p.dst); err != nil {
			return nil, fmt.Errorf("state: step %s: bad json: %w", s.id, err)
		}
	}
	return &s, nil
}

func loadStep(ctx context.Context, q execer, id string) (*stepRec, error) {
	s, err := scanStep(q.QueryRowContext(ctx, `SELECT `+stepCols+` FROM steps WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("state: step %q not found", id)
	}
	return s, err
}

// loadSteps returns the steps of a requirement in seq order.
func loadSteps(ctx context.Context, q execer, reqID string) ([]*stepRec, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+stepCols+` FROM steps WHERE req_id = ? ORDER BY seq`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*stepRec
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *txc) saveStep(s *stepRec) error {
	_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET state = ?, stage = ?, resume_stage = ?, reason = ?,
		deadline = ?, counters = ?, guidance = ?, denial = ?, access_grants = ?, last_reviewed_sha = ?
		WHERE id = ?`,
		s.state, s.stage, nullStr(s.resume), nullStr(s.reason), nullStr(s.deadline), jsonText(s.counters),
		nullStr(s.guidance), nullStr(s.denial), jsonText(nonNil(s.grants)), nullStr(s.lastReviewed), s.id)
	s.dirty = false
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// pendingTrigger is a trigger that arrived while a higher-priority hold was
// active (E-REQ.pending_triggers); it keeps the context of the owner ask it
// would have created.
type pendingTrigger struct {
	Trigger string   `json:"trigger"`
	HItem   string   `json:"h_item,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Causes  []string `json:"causes,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// reqRec is a requirement row held in memory while a transaction runs.
type reqRec struct {
	id, project, branch, source string
	hold, holdDeadline          string
	parked                      bool
	specVersion                 int
	spec                        string
	counters                    map[string]int
	pending                     []pendingTrigger
	pendingRun                  string
	plannerInput, rescuerInput  []string
	droppedReason               string
	sessionRepoState            string
	budgetExt                   int
	budgetTokens, budgetPct     int
	dirty                       bool
}

func (r *reqRec) view() ReqView {
	v := ReqView{Hold: r.hold, ReqCounters: map[string]int{}}
	for k, n := range r.counters {
		v.ReqCounters[k] = n
	}
	return v
}

const reqCols = `id, branch, source, hold, hold_deadline, parked, spec_version, spec, req_counters,
	pending_triggers, pending_run, planner_input, rescuer_input, dropped_reason, session_repo_state,
	budget_extensions, budget_tokens, budget_quota_pct`

func loadReq(ctx context.Context, q execer, id string) (*reqRec, error) {
	var r reqRec
	var hd, dropped sql.NullString
	var parked int
	var counters, pending, pin, rin string
	err := q.QueryRowContext(ctx, `SELECT `+reqCols+` FROM requirements WHERE id = ?`, id).Scan(
		&r.id, &r.branch, &r.source, &r.hold, &hd, &parked, &r.specVersion, &r.spec, &counters,
		&pending, &r.pendingRun, &pin, &rin, &dropped, &r.sessionRepoState,
		&r.budgetExt, &r.budgetTokens, &r.budgetPct)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("state: requirement %q not found", id)
	}
	if err != nil {
		return nil, err
	}
	r.holdDeadline, r.parked, r.droppedReason = hd.String, parked == 1, dropped.String
	r.counters = map[string]int{}
	for _, p := range []struct {
		text string
		dst  any
	}{{counters, &r.counters}, {pending, &r.pending}, {pin, &r.plannerInput}, {rin, &r.rescuerInput}} {
		if err := json.Unmarshal([]byte(p.text), p.dst); err != nil {
			return nil, fmt.Errorf("state: requirement %s: bad json: %w", id, err)
		}
	}
	r.project = projectOf(id)
	return &r, nil
}

// projectOf extracts <project> from "R-<project>-<n>".
func projectOf(reqID string) string {
	s := strings.TrimPrefix(reqID, "R-")
	if i := strings.LastIndex(s, "-"); i >= 0 {
		s = s[:i]
	}
	return s
}

func (c *txc) saveReq() error {
	r := c.req
	parked := 0
	if r.parked {
		parked = 1
	}
	_, err := c.tx.ExecContext(c.ctx, `UPDATE requirements SET hold = ?, hold_deadline = ?, parked = ?,
		spec_version = ?, spec = ?, req_counters = ?, pending_triggers = ?, pending_run = ?,
		planner_input = ?, rescuer_input = ?, dropped_reason = ?, session_repo_state = ?, budget_extensions = ?
		WHERE id = ?`,
		r.hold, nullStr(r.holdDeadline), parked, r.specVersion, r.spec, jsonText(r.counters),
		jsonText(nonNilTrig(r.pending)), r.pendingRun, jsonText(nonNil(r.plannerInput)),
		jsonText(nonNil(r.rescuerInput)), nullStr(r.droppedReason), r.sessionRepoState, r.budgetExt, r.id)
	r.dirty = false
	return err
}

func nonNilTrig(p []pendingTrigger) []pendingTrigger {
	if p == nil {
		return []pendingTrigger{}
	}
	return p
}

// LoadStepView reads a step and its requirement into the views Decide and
// DecideReq take. It only reads.
func LoadStepView(ctx context.Context, db *sqlite.DB, stepID string) (StepView, ReqView, error) {
	var sv StepView
	var rv ReqView
	err := db.Read(ctx, func(tx *sql.Tx) error {
		s, err := loadStep(ctx, tx, stepID)
		if err != nil {
			return err
		}
		r, err := loadReq(ctx, tx, s.reqID)
		if err != nil {
			return err
		}
		sv, rv = s.view(), r.view()
		return nil
	})
	return sv, rv, err
}

// LoadReqView reads a requirement into a ReqView. It only reads.
func LoadReqView(ctx context.Context, db *sqlite.DB, reqID string) (ReqView, error) {
	var rv ReqView
	err := db.Read(ctx, func(tx *sql.Tx) error {
		r, err := loadReq(ctx, tx, reqID)
		if err != nil {
			return err
		}
		rv = r.view()
		return nil
	})
	return rv, err
}

// nextSeq returns the next value of the named sequence, starting at 1.
func (c *txc) nextSeq(name string) (int, error) {
	var n int
	err := c.tx.QueryRowContext(c.ctx, `INSERT INTO id_seq (name, next) VALUES (?, 2)
		ON CONFLICT (name) DO UPDATE SET next = next + 1 RETURNING next - 1`, name).Scan(&n)
	return n, err
}
