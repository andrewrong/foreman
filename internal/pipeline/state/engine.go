package state

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// defaultQueued is how long a step may sit queued before alert_starved (S09).
const defaultQueued = 24 * time.Hour

// Deadlines are the durations behind the deadline column (state-machine.yaml
// dims.waiting_reasons). Zero values take the defaults below.
type Deadlines struct {
	Queued       time.Duration // queued longer than this: alert_starved (S09), default 24h
	Running      time.Duration // a running Run's deadline, default 60m
	QuotaUnknown time.Duration // waiting(quota) without a known reset time, default 30m
	QuotaMargin  time.Duration // added to resets_at, default 5m
	Infra        []time.Duration
	Access       time.Duration // default 10m
	Rescue       time.Duration // rescuer timeout (CF-02), default 2h
	CI           time.Duration // default 35m
	Ask          time.Duration // OwnerAsk deadline, default 72h
	Parked       time.Duration // parked reminder period, default 7d
	StarvedAlert time.Duration // alert_starved dedupe window, default 6h
}

// Config is what the effect executors need beyond the database.
type Config struct {
	Models       map[string]string // role -> model; defaults follow skills.yaml
	RetryUpgrade string            // coder model after check_fail/review_rounds reach cap-1 (D-044), default opus
	Harness      string            // Run.harness of AI roles, default claude
	RunsDir      string            // Run.dir = RunsDir/<run id>, default "runs"
	ConfigRev    int               // Run.config_rev
	Deadlines    Deadlines
}

func (c Config) withDefaults() Config {
	models := map[string]string{"planner": "opus", "coder": "sonnet", "reviewer": "opus",
		"rescuer": "opus", "diagnoser": "opus", "intake": "sonnet", "improver": "opus"}
	for k, v := range c.Models {
		models[k] = v
	}
	c.Models = models
	if c.RetryUpgrade == "" {
		c.RetryUpgrade = "opus"
	}
	if c.Harness == "" {
		c.Harness = "claude"
	}
	if c.RunsDir == "" {
		c.RunsDir = "runs"
	}
	d := &c.Deadlines
	def := func(p *time.Duration, v time.Duration) {
		if *p == 0 {
			*p = v
		}
	}
	def(&d.Queued, defaultQueued)
	def(&d.Running, 60*time.Minute)
	def(&d.QuotaUnknown, 30*time.Minute)
	def(&d.QuotaMargin, 5*time.Minute)
	def(&d.Access, 10*time.Minute)
	def(&d.Rescue, 2*time.Hour)
	def(&d.CI, 35*time.Minute)
	def(&d.Ask, 72*time.Hour)
	def(&d.Parked, 7*24*time.Hour)
	def(&d.StarvedAlert, 6*time.Hour)
	if len(d.Infra) == 0 {
		d.Infra = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 2 * time.Hour, 2 * time.Hour}
	}
	return c
}

// Engine runs transitions. The package-level Transition and TransitionReq
// use an Engine with the real clock and default Config; callers that need an
// injected clock (C-05) or other settings build one with NewEngine.
type Engine struct {
	clk  clock.Clock
	cfg  Config
	exec map[string]executor
}

// NewEngine returns an Engine. A nil clk means the real clock.
func NewEngine(clk clock.Clock, cfg Config) *Engine {
	if clk == nil {
		clk = clock.Real()
	}
	return &Engine{clk: clk, cfg: cfg.withDefaults(), exec: executors()}
}

// Transition applies one event to a step: in ONE write transaction it loads
// the step and its requirement, calls Decide, writes the new state and runs
// every effect of the row (TX-1). It is the only writer of step state and
// requirement hold (C-01). An error or panic rolls everything back.
func Transition(ctx context.Context, db *sqlite.DB, stepID string, ev Event) (Outcome, error) {
	return NewEngine(nil, Config{}).Transition(ctx, db, stepID, ev)
}

// TransitionReq applies one event to a requirement's hold (table B) in one
// write transaction.
func TransitionReq(ctx context.Context, db *sqlite.DB, reqID string, ev Event) (ReqOutcome, error) {
	return NewEngine(nil, Config{}).TransitionReq(ctx, db, reqID, ev)
}

// Transition is the method form of the package-level Transition.
func (e *Engine) Transition(ctx context.Context, db *sqlite.DB, stepID string, ev Event) (Outcome, error) {
	var out Outcome
	err := db.Write(ctx, func(tx *sql.Tx) error {
		c := e.newTxc(ctx, tx, ev)
		o, err := c.transitionStep(stepID)
		out = o
		return err
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// TransitionReq is the method form of the package-level TransitionReq.
func (e *Engine) TransitionReq(ctx context.Context, db *sqlite.DB, reqID string, ev Event) (ReqOutcome, error) {
	var out ReqOutcome
	err := db.Write(ctx, func(tx *sql.Tx) error {
		c := e.newTxc(ctx, tx, ev)
		o, err := c.transitionReq(reqID)
		out = o
		return err
	})
	if err != nil {
		return ReqOutcome{}, err
	}
	return out, nil
}

// txc is the state of one transaction.
type txc struct {
	ctx context.Context
	tx  *sql.Tx
	e   *Engine
	now time.Time
	ev  Event

	req  *reqRec
	step *stepRec // the step being transitioned; nil in TransitionReq
	ask  *askRec  // the ask answered by owner_answer / owner_retry
	subj *stepRec // subject step of that ask, loaded on demand

	rowID     string // table row being executed, for generated texts
	out       *Outcome
	prevStage string

	inReq       bool     // effects of a table B row are running
	hitem       string   // H item from req_hold:owner:<H> of the originating step row
	askSubject  string   // subject of asks created by nested triggers
	causes      []string // causes of the originating step row
	newFindings []string // findings inserted by this step event, in order
	nested      []ReqOutcome
	depth       int
}

func (e *Engine) newTxc(ctx context.Context, tx *sql.Tx, ev Event) *txc {
	return &txc{ctx: ctx, tx: tx, e: e, now: e.clk.Now(), ev: ev}
}

func (c *txc) transitionStep(stepID string) (Outcome, error) {
	step, err := loadStep(c.ctx, c.tx, stepID)
	if err != nil {
		return Outcome{}, err
	}
	req, err := loadReq(c.ctx, c.tx, step.reqID)
	if err != nil {
		return Outcome{}, err
	}
	c.step, c.req = step, req
	if c.ev.Name == "hold_released" && holdIsSet(req.hold) {
		// T7a: hold_released while the hold is still set would bounce the step
		// straight back to waiting(hold); the sender must clear the hold first.
		return Outcome{}, fmt.Errorf("state: hold_released for %s while requirement %s hold=%s", stepID, req.id, req.hold)
	}
	out, err := Decide(step.view(), req.view(), c.ev)
	if err != nil {
		return Outcome{}, err
	}
	c.out, c.rowID, c.prevStage = &out, out.RowID, step.stage
	if !out.Ignored {
		c.autoResetCounters()
		if err := c.closeOnL1Pass(); err != nil {
			return Outcome{}, fmt.Errorf("%s: %w", out.RowID, err)
		}
		if err := c.applyStepTarget(out); err != nil {
			return Outcome{}, fmt.Errorf("%s: %w", out.RowID, err)
		}
		for _, eff := range out.Effects {
			if err := c.runEffect(eff); err != nil {
				return Outcome{}, fmt.Errorf("%s: effect %q: %w", out.RowID, eff, err)
			}
		}
		if err := c.finishStep(); err != nil {
			return Outcome{}, fmt.Errorf("%s: %w", out.RowID, err)
		}
	}
	if err := c.drain(); err != nil {
		return Outcome{}, err
	}
	out.Reqs = c.nested
	return out, nil
}

// applyStepTarget writes the state part of the outcome to the step row.
func (c *txc) applyStepTarget(out Outcome) error {
	s := c.step
	s.state, s.stage = out.To.State, out.To.Stage
	if s.stage == "" { // done and dropped keep the stage they ended in
		s.stage = c.prevStage
	}
	s.reason, s.resume = out.To.Reason, out.To.ResumeStage
	if out.To.State == "dropped" {
		s.reason = c.ev.Name
		if c.ev.Decision != "" {
			s.reason += ":" + c.ev.Decision
		}
		if c.ev.DropReason != "" {
			s.reason = c.ev.DropReason
		}
	}
	return c.saveStep(s)
}

// finishStep fixes the deadline (after effects such as inc:infra changed the
// counters it depends on), checks the E-STEP invariants and settles asks of a
// step that became terminal.
func (c *txc) finishStep() error {
	s := c.step
	s.deadline = c.deadlineFor(s)
	if s.state == "waiting" && s.reason == "hold" && s.resume == "" {
		return fmt.Errorf("waiting(hold) without resume_stage")
	}
	if s.state != "waiting" || s.reason != "hold" {
		s.resume = ""
	}
	if err := c.saveStep(s); err != nil {
		return err
	}
	if s.state == "done" || s.state == "dropped" {
		return c.mootAsks(`subject = ?`, s.id)
	}
	return nil
}

// deadlineFor computes the deadline of a step in its current state.
func (c *txc) deadlineFor(s *stepRec) string {
	d := c.e.cfg.Deadlines
	var dur time.Duration
	switch s.state {
	case "queued":
		dur = d.Queued
	case "running":
		dur = d.Running
	case "waiting":
		switch s.reason {
		case "quota":
			if !c.ev.ResetsAt.IsZero() {
				return fmtTime(c.ev.ResetsAt.Add(d.QuotaMargin))
			}
			dur = d.QuotaUnknown
		case "infra":
			i := max(s.counters["infra"]-1, 0)
			dur = d.Infra[min(i, len(d.Infra)-1)]
		case "access":
			dur = d.Access
		case "rescue":
			dur = d.Rescue
		case "ci":
			dur = d.CI
		default: // deps, hold, fix are bounded by other rows
			return ""
		}
	default:
		return ""
	}
	return fmtTime(c.now.Add(dur))
}

func (c *txc) transitionReq(reqID string) (ReqOutcome, error) {
	req, err := loadReq(c.ctx, c.tx, reqID)
	if err != nil {
		return ReqOutcome{}, err
	}
	c.req = req
	ro, err := DecideReq(req.view(), c.ev)
	if err != nil {
		return ReqOutcome{}, err
	}
	if ro.Ignored {
		if isTriggerEvent(c.ev.Name) && holdIsSet(req.hold) {
			if err := c.recordPending(c.ev.Name, "", c.ev.Subject, nil); err != nil {
				return ReqOutcome{}, err
			}
		}
	} else {
		c.rowID = ro.RowID
		if err := c.resolveAskFor(ro); err != nil {
			return ReqOutcome{}, fmt.Errorf("%s: %w", ro.RowID, err)
		}
		c.askSubject = c.ev.Subject
		fu, err := c.applyReqOutcome(&ro)
		if err != nil {
			return ReqOutcome{}, fmt.Errorf("%s: %w", ro.RowID, err)
		}
		ro.Followups = fu
	}
	if err := c.drain(); err != nil {
		return ReqOutcome{}, err
	}
	return ro, nil
}

func isTriggerEvent(name string) bool {
	return slices.Contains([]string{"replan_trigger", "rescue_trigger", "owner_trigger"}, name)
}

// holdPriority orders triggers: owner > rescue > replan.
func holdPriority(trigger string) int {
	switch trigger {
	case "owner_trigger":
		return 3
	case "rescue_trigger":
		return 2
	}
	return 1
}

func joinCauses(cs []string) string { return strings.Join(cs, ",") }
