package state

import (
	"fmt"
	"slices"
	"strings"
)

// triggerEvents maps the req_hold:<x> effect of a step row to its table B
// event (req_hold.triggers).
func triggerEvents() map[string]string {
	return map[string]string{
		"replan": "replan_trigger",
		"rescue": "rescue_trigger",
		"owner":  "owner_trigger",
	}
}

// fxReqHold runs table B for the trigger a step row raises: req_hold:<hold>
// [:<H item>]. The hold is not written blindly: caps, rescue_or_owner and
// the precedence rules all apply.
func fxReqHold(c *txc, arg string) error {
	hold, hitem, _ := strings.Cut(arg, ":")
	ev, ok := triggerEvents()[hold]
	if !ok {
		return fmt.Errorf("req_hold: unknown hold %q", hold)
	}
	if c.depth > 0 {
		return fmt.Errorf("req_hold nested inside a table B row")
	}
	c.hitem, c.askSubject = hitem, c.step.id
	c.causes = c.out.Causes
	return c.trigger(ev, pendingTrigger{Trigger: ev, HItem: hitem, Subject: c.step.id,
		Causes: c.out.Causes, Detail: c.triggerText()})
}

// trigger decides and applies one table B trigger event. Triggers that table
// B ignores while a hold is set are kept in pending_triggers.
func (c *txc) trigger(event string, p pendingTrigger) error {
	ro, err := DecideReq(c.req.view(), Event{Name: event})
	if err != nil {
		return err
	}
	if ro.Ignored {
		return c.recordPending(event, p.HItem, p.Subject, p.Causes)
	}
	prev := c.rowID
	c.rowID = ro.RowID
	c.depth++
	_, err = c.applyReqOutcome(&ro)
	c.depth--
	c.rowID = prev
	c.nested = append(c.nested, ro)
	return err
}

func (c *txc) recordPending(trigger, hitem, subject string, causes []string) error {
	c.req.pending = append(c.req.pending, pendingTrigger{Trigger: trigger, HItem: hitem,
		Subject: subject, Causes: causes, Detail: c.ev.Detail})
	c.req.dirty = true
	return c.flush()
}

// applyReqOutcome writes the new hold, then runs the row's effects (so that
// release_steps sees the cleared hold), then processes pending triggers if
// the hold is back to none. The returned outcomes are those pending triggers.
func (c *txc) applyReqOutcome(ro *ReqOutcome) ([]ReqOutcome, error) {
	r := c.req
	old := r.hold
	r.hold = ro.To
	r.dirty = true
	if ro.To != old {
		if err := c.holdChanged(old, ro.To); err != nil {
			return nil, err
		}
	}
	if err := c.flush(); err != nil {
		return nil, err
	}
	prevInReq, prevCauses := c.inReq, c.causes
	c.inReq = true
	c.causes = append(slices.Clone(c.causes), ro.Causes...)
	for _, eff := range ro.Effects {
		if err := c.runEffect(eff); err != nil {
			return nil, fmt.Errorf("effect %q: %w", eff, err)
		}
	}
	c.inReq, c.causes = prevInReq, prevCauses
	if c.depth > 0 {
		return nil, nil
	}
	return c.drainPending()
}

// holdChanged does the bookkeeping that goes with a hold change.
func (c *txc) holdChanged(old, now string) error {
	r := c.req
	switch now {
	case HoldParked:
		r.holdDeadline = fmtTime(c.now.Add(c.e.cfg.Deadlines.Parked))
	case HoldNone, HoldReplan, HoldRescue:
		r.holdDeadline = ""
	}
	if old == HoldReplan && now != HoldReplan {
		r.pendingRun, r.plannerInput = "none", nil // the planner Run is no longer wanted
	}
	if old == HoldRescue && now != HoldRescue {
		r.rescuerInput = nil
	}
	owner := func(h string) bool { return h == HoldOwner || h == HoldParked }
	if owner(old) && !owner(now) {
		// the answered ask was settled by resolveAskFor; the rest are moot
		if err := c.mootAsks(`req_id = ?`, r.id); err != nil {
			return err
		}
	}
	if now == old {
		return nil
	}
	// The reason that started the new hold is the first line of the input of
	// the planner / rescuer Run it is about to get.
	switch now {
	case HoldReplan:
		text := c.triggerText()
		if c.ev.Name == "rescue_decision" || c.ev.Name == "owner_answer" {
			text = firstNonEmpty(c.ev.Guidance, c.ev.Detail, text)
		}
		r.plannerInput = append(r.plannerInput, text)
		return c.holdSteps()
	case HoldRescue:
		r.rescuerInput = append(r.rescuerInput, c.triggerText())
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// holdSteps implements replan_effects_on_steps: when a replan hold starts,
// queued and waiting(deps|quota|infra|access) steps become waiting(hold) and
// remember their stage. The step being transitioned is left alone.
func (c *txc) holdSteps() error {
	skip := ""
	if c.step != nil {
		skip = c.step.id
	}
	_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET resume_stage = stage, state = 'waiting', reason = 'hold',
		deadline = NULL WHERE req_id = ? AND id <> ?
		AND (state = 'queued' OR (state = 'waiting' AND reason IN ('deps', 'quota', 'infra', 'access')))`,
		c.req.id, skip)
	return err
}

// drainPending runs the highest-priority pending trigger while the hold is
// none (precedence: owner > rescue > replan).
func (c *txc) drainPending() ([]ReqOutcome, error) {
	var out []ReqOutcome
	for c.req.hold == HoldNone && len(c.req.pending) > 0 {
		best := 0
		for i, p := range c.req.pending {
			if holdPriority(p.Trigger) > holdPriority(c.req.pending[best].Trigger) {
				best = i
			}
		}
		p := c.req.pending[best]
		c.req.pending = slices.Delete(slices.Clone(c.req.pending), best, best+1)
		c.req.dirty = true
		before := len(c.nested)
		c.hitem, c.askSubject, c.causes = p.HItem, p.Subject, p.Causes
		if err := c.trigger(p.Trigger, p); err != nil {
			return nil, err
		}
		out = append(out, c.nested[before:]...)
	}
	return out, nil
}

func fxReleaseSteps(c *txc, _ string) error {
	if holdIsSet(c.req.hold) {
		return fmt.Errorf("release_steps while hold=%s", c.req.hold)
	}
	c.subj = nil
	_, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET state = 'queued', stage = resume_stage, reason = NULL,
		resume_stage = NULL, deadline = ? WHERE req_id = ? AND state = 'waiting' AND reason = 'hold'`,
		fmtTime(c.now.Add(c.e.cfg.Deadlines.Queued)), c.req.id)
	return err
}

// dropSteps sets steps to dropped with a reason and kills their Runs.
func (c *txc) dropSteps(where string, reason string, args ...any) error {
	rows, err := c.tx.QueryContext(c.ctx, `SELECT id FROM steps WHERE state NOT IN ('done', 'dropped') AND `+where, args...)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	c.subj = nil
	for _, id := range ids {
		if _, err := c.tx.ExecContext(c.ctx, `UPDATE steps SET state = 'dropped', reason = ?, resume_stage = NULL,
			deadline = NULL WHERE id = ?`, reason, id); err != nil {
			return err
		}
		if err := c.mootAsks(`subject = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// fxDropAllSteps drops every non-terminal step and records dropped_reason
// (ACR-05); queued triggers and Runs of the dropped requirement are cleared.
func fxDropAllSteps(c *txc, _ string) error {
	if c.ev.DropReason == "" {
		return fmt.Errorf("drop_all_steps: dropped_reason is required (ACR-05)")
	}
	if err := c.dropSteps(`req_id = ?`, c.ev.DropReason, c.req.id); err != nil {
		return err
	}
	r := c.req
	r.droppedReason, r.pendingRun, r.pending = c.ev.DropReason, "none", nil
	r.plannerInput, r.rescuerInput, r.dirty = nil, nil, true
	return c.mootAsks(`req_id = ?`, r.id)
}

// fxDropHeldStep drops the work step the answered ask is about. An integrate
// subject means terminating the requirement (H23 note: handled as H16).
func fxDropHeldStep(c *txc, _ string) error {
	s, err := c.subjectStep()
	if err != nil {
		return err
	}
	if s.typ == "integrate" {
		if err := fxKillRuns(c, ""); err != nil {
			return err
		}
		return fxDropAllSteps(c, "")
	}
	reason := firstNonEmpty(c.ev.DropReason, "dropped by owner (drop_step)")
	if err := c.killRuns(`step_id = ?`, s.id); err != nil {
		return err
	}
	s.dirty = false
	return c.dropSteps(`id = ?`, reason, s.id)
}
