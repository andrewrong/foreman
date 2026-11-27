package state

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// subjectStep is the step a step-level effect applies to: the transitioning
// step, or in a table B row the subject of the answered ask.
func (c *txc) subjectStep() (*stepRec, error) {
	if c.step != nil {
		return c.step, nil
	}
	if c.subj != nil {
		return c.subj, nil
	}
	if c.ask == nil {
		return nil, fmt.Errorf("no step in scope (no ask answered)")
	}
	s, err := loadStep(c.ctx, c.tx, c.ask.subject)
	if err != nil {
		return nil, fmt.Errorf("ask %s subject is not a step: %w", c.ask.id, err)
	}
	if s.reqID != c.req.id {
		return nil, fmt.Errorf("ask %s subject %s belongs to another requirement", c.ask.id, s.id)
	}
	c.subj = s
	return s, nil
}

// counterMap returns the map holding the named counter, its cap, and marks
// the owner row dirty.
func (c *txc) counterMap(name string) (m map[string]int, limit int, err error) {
	if l, ok := dimCounters[name]; ok {
		s, err := c.subjectStep()
		if err != nil {
			return nil, 0, fmt.Errorf("step counter %s: %w", name, err)
		}
		s.dirty = true
		return s.counters, l, nil
	}
	if l, ok := dimReqCounters[name]; ok {
		c.req.dirty = true
		return c.req.counters, l, nil
	}
	return nil, 0, fmt.Errorf("unknown counter %q", name)
}

func splitList(arg string) []string {
	var out []string
	for _, p := range strings.Split(arg, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fxInc(c *txc, arg string) error {
	m, _, err := c.counterMap(arg)
	if err != nil {
		return err
	}
	m[arg]++
	return nil
}

func fxGrantOne(c *txc, arg string) error {
	for _, name := range splitList(arg) {
		m, limit, err := c.counterMap(name)
		if err != nil {
			return err
		}
		m[name] = min(m[name], limit-1)
	}
	return nil
}

func fxReset(c *txc, arg string) error {
	names := splitList(arg)
	if arg == "ask.cause_counter" {
		if c.ask == nil {
			return nil // owner_retry with no open ask: nothing to clear
		}
		names = splitList(c.ask.cause)
	}
	for _, name := range names {
		if name == "budget" { // H4: resume adds one original budget
			c.req.budgetExt++
			c.req.dirty = true
			continue
		}
		if _, ok := dimCounters[name]; ok && c.step == nil && c.ask != nil && c.ask.subject == c.req.id {
			continue // step-level cause but the ask is about the requirement
		}
		m, _, err := c.counterMap(name)
		if err != nil {
			return err
		}
		m[name] = 0
	}
	return nil
}

func fxSet(c *txc, arg string) error {
	field, val, ok := strings.Cut(arg, "=")
	if !ok {
		return fmt.Errorf("set: want <field>=<value>, got %q", arg)
	}
	switch field {
	case "parked":
		if val != "true" && val != "false" {
			return fmt.Errorf("set:parked=%s", val)
		}
		c.req.parked, c.req.dirty = val == "true", true
	case "last_reviewed_sha":
		if val != "head" {
			return fmt.Errorf("set:last_reviewed_sha=%s (only head)", val)
		}
		return c.setLastReviewed()
	default:
		return fmt.Errorf("set: unknown field %q", field)
	}
	return nil
}

// setLastReviewed writes E-STEP.last_reviewed_sha for a work step and
// E-PR.last_reviewed_sha for an integrate step.
func (c *txc) setLastReviewed() error {
	s := c.step
	if s == nil {
		return fmt.Errorf("set:last_reviewed_sha=head needs a step")
	}
	head := c.ev.Head
	if s.typ != "integrate" {
		if head == "" {
			return fmt.Errorf("event %s carries no head sha", c.ev.Name)
		}
		s.lastReviewed, s.dirty = head, true
		return nil
	}
	if _, err := c.tx.ExecContext(c.ctx, `INSERT INTO pull_requests (req_id, head_branch) VALUES (?, ?)
		ON CONFLICT (req_id) DO NOTHING`, c.req.id, c.req.branch); err != nil {
		return err
	}
	if head == "" {
		var h sql.NullString
		if err := c.tx.QueryRowContext(c.ctx, `SELECT head_sha FROM pull_requests WHERE req_id = ?`, c.req.id).Scan(&h); err != nil {
			return err
		}
		head = h.String
	}
	if head == "" {
		return fmt.Errorf("event %s carries no head sha and the pull request has none", c.ev.Name)
	}
	_, err := c.tx.ExecContext(c.ctx, `UPDATE pull_requests SET last_reviewed_sha = ? WHERE req_id = ?`, head, c.req.id)
	return err
}

func fxRecordResumeStage(c *txc, _ string) error {
	if c.step == nil {
		return fmt.Errorf("record_resume_stage needs a step")
	}
	if c.out.HoldRewritten {
		c.step.resume = c.out.To.ResumeStage
	} else {
		c.step.resume = c.prevStage
	}
	c.step.dirty = true
	return nil
}

// parseDays parses "7d" style durations; other durations use time.ParseDuration.
func parseDays(arg string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(arg, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 {
			return 0, fmt.Errorf("bad duration %q", arg)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(arg)
}

func fxExtendDeadline(c *txc, arg string) error {
	if c.step == nil || c.inReq { // requirement level (H18): the hold deadline
		dur := c.e.cfg.Deadlines.Parked
		if arg != "" {
			d, err := parseDays(arg)
			if err != nil {
				return err
			}
			dur = d
		}
		c.req.holdDeadline, c.req.dirty = fmtTime(c.now.Add(dur)), true
		return nil
	}
	c.step.deadline, c.step.dirty = c.deadlineFor(c.step), true
	return nil
}

func (c *txc) enqueue(kind, subject, message string) error {
	_, err := c.tx.ExecContext(c.ctx, `INSERT INTO notify_queue (kind, req_id, subject, message, created_at)
		VALUES (?, ?, ?, ?, ?)`, kind, c.req.id, subject, message, fmtTime(c.now))
	return err
}

func (c *txc) subjectID() string {
	if c.step != nil {
		return c.step.id
	}
	return c.req.id
}

func fxAlertStarved(c *txc, _ string) error {
	if c.step == nil {
		return fmt.Errorf("alert_starved needs a step")
	}
	since := fmtTime(c.now.Add(-c.e.cfg.Deadlines.StarvedAlert))
	var n int
	if err := c.tx.QueryRowContext(c.ctx, `SELECT COUNT(*) FROM notify_queue
		WHERE kind = 'starved' AND subject = ? AND created_at > ?`, c.step.id, since).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // at most one alert per step per window
	}
	return c.enqueue("starved", c.step.id, "step queued longer than "+c.e.cfg.Deadlines.Queued.String())
}

func fxNotify(c *txc, _ string) error {
	return c.enqueue("notify", c.subjectID(), fmt.Sprintf("%s: %s", c.rowID, c.ev.Name))
}

func fxInjectDenial(c *txc, _ string) error {
	s, err := c.subjectStep()
	if err != nil {
		return err
	}
	text := c.ev.Detail
	if text == "" {
		text = "access denied"
		if c.ev.Resource != "" {
			text += ": " + c.ev.Resource
		}
	}
	s.denial, s.dirty = text, true
	return nil
}

func fxInjectGuidance(c *txc, _ string) error {
	g := c.ev.Guidance
	if n := utf8.RuneCountInString(g); n == 0 || n > 1000 {
		return fmt.Errorf("guidance must be 1..1000 characters, got %d", n)
	}
	if c.step != nil { // S21: the step the rescuer decided about
		c.step.guidance, c.step.dirty = g, true
		return nil
	}
	// H07: every step of the requirement that the release brings back.
	_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET guidance = ?
		WHERE req_id = ? AND state = 'waiting' AND reason = 'hold'`, g, c.req.id)
	return err
}

func fxGrantAccess(c *txc, _ string) error {
	if c.ev.Resource == "" {
		return fmt.Errorf("grant_access: event carries no resource")
	}
	s, err := c.subjectStep()
	if err != nil {
		return err
	}
	if !slices.Contains(s.grants, c.ev.Resource) {
		s.grants = append(s.grants, c.ev.Resource)
	}
	s.dirty = true
	return nil
}

func fxAppendPlannerInput(c *txc, _ string) error {
	c.req.plannerInput = append(c.req.plannerInput, c.triggerText())
	c.req.dirty = true
	return nil
}

func fxAppendRescuerInput(c *txc, _ string) error {
	c.req.rescuerInput = append(c.req.rescuerInput, c.triggerText())
	c.req.dirty = true
	return nil
}

// triggerText is the reason recorded in planner/rescuer input.
func (c *txc) triggerText() string {
	if c.ev.Detail != "" {
		return c.ev.Detail
	}
	if c.step != nil {
		return fmt.Sprintf("%s on %s (%s)", c.ev.Name, c.step.id, c.out.RowID)
	}
	return c.ev.Name
}

func fxReleaseSessionRepo(c *txc, _ string) error {
	c.req.sessionRepoState, c.req.dirty = "releasing", true
	return nil
}
