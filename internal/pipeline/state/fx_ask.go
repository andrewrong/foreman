package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// askRec is the answered OwnerAsk of an owner_answer / owner_retry event.
type askRec struct {
	id, subject, cause string
	options            []AskOption
}

// resolveAskFor finds and settles the ask an owner_answer / owner_retry
// event answers (status answered, answer = the chosen option). It runs
// before the row's effects, which read the ask (reset:ask.cause_counter,
// grant_access, ...).
func (c *txc) resolveAskFor(ro ReqOutcome) error {
	if c.ev.Name != "owner_answer" && c.ev.Name != "owner_retry" {
		return nil
	}
	ask, err := c.findAsk()
	if err != nil {
		return err
	}
	if ask == nil {
		if c.ev.Name == "owner_answer" {
			return fmt.Errorf("owner_answer: no open ask for %s", c.req.id)
		}
		return nil
	}
	answer := firstNonEmpty(c.ev.AnswerOption, c.ev.Answer)
	if c.ev.Name == "owner_answer" && !slices.ContainsFunc(ask.options, func(o AskOption) bool { return o.ID == answer }) {
		return fmt.Errorf("owner_answer: %q is not an option of ask %s", answer, ask.id)
	}
	if answer == "" {
		answer = "retry"
	}
	c.ask = ask
	_, err = c.tx.ExecContext(c.ctx, `UPDATE asks SET status = 'answered', answer = ? WHERE id = ?`, answer, ask.id)
	return err
}

func (c *txc) findAsk() (*askRec, error) {
	q := `SELECT id, subject, cause_counter, options FROM asks WHERE req_id = ? AND status = 'open'`
	args := []any{c.req.id}
	if c.ev.AskID != "" {
		q += ` AND id = ?`
		args = append(args, c.ev.AskID)
	}
	rows, err := c.tx.QueryContext(c.ctx, q+` ORDER BY sent_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var asks []*askRec
	for rows.Next() {
		var a askRec
		var cause sql.NullString
		var opts string
		if err := rows.Scan(&a.id, &a.subject, &cause, &opts); err != nil {
			return nil, err
		}
		a.cause = cause.String
		if err := json.Unmarshal([]byte(opts), &a.options); err != nil {
			return nil, err
		}
		asks = append(asks, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(asks) {
	case 0:
		return nil, nil
	case 1:
		return asks[0], nil
	}
	return nil, fmt.Errorf("%d open asks for %s: the event must name one (AskID)", len(asks), c.req.id)
}

// mootAsks sets the open asks matching where to moot (ACR-06).
func (c *txc) mootAsks(where string, args ...any) error {
	_, err := c.tx.ExecContext(c.ctx, `UPDATE asks SET status = 'moot' WHERE status = 'open' AND `+where, args...)
	return err
}

// defaultOptions are the choices offered when the event gives none.
func defaultOptions(hitem, subject, reqID string) []AskOption {
	resume := AskOption{"resume", "resume"}
	drop := AskOption{"drop", "terminate requirement"}
	switch hitem {
	case "H6":
		return []AskOption{{"grant", "grant"}, {"deny", "deny"}}
	case "H5":
		return []AskOption{resume, {"replan", "replan"}, drop}
	case "H7":
		if subject != reqID {
			return []AskOption{resume, {"drop_step", "drop this step"}, drop}
		}
	}
	return []AskOption{resume, drop}
}

// fxCreateOwnerAsk inserts one open E-ASK and moves the owner deadline. The
// H item comes from the effect (create_owner_ask:H5), else from the
// req_hold:owner:<H> effect of the step row that raised the trigger, else
// from Event.Ask, else H5. The subject is that step (or the requirement);
// cause_counter lists the counters that were at_cap in the rows involved, or
// "budget" for H4.
func fxCreateOwnerAsk(c *txc, arg string) error {
	content := c.ev.Ask
	if content == nil {
		content = &AskContent{}
	}
	hitem := firstNonEmpty(arg, c.hitem, content.HItem, "H5")
	subject := firstNonEmpty(c.askSubject, c.req.id)
	cause := joinCauses(c.causes)
	if hitem == "H4" {
		cause = "budget"
	}
	opts := content.Options
	if len(opts) == 0 {
		opts = defaultOptions(hitem, subject, c.req.id)
	}
	if len(opts) < 2 || len(opts) > 3 {
		return fmt.Errorf("create_owner_ask: %d options (want 2..3)", len(opts))
	}
	recommended := firstNonEmpty(content.Recommended, opts[0].ID)
	if !slices.ContainsFunc(opts, func(o AskOption) bool { return o.ID == recommended }) {
		return fmt.Errorf("create_owner_ask: recommended %q is not an option", recommended)
	}
	n, err := c.nextSeq("ask:" + c.req.project)
	if err != nil {
		return err
	}
	deadline := c.now.Add(c.e.cfg.Deadlines.Ask)
	_, err = c.tx.ExecContext(c.ctx, `INSERT INTO asks (id, req_id, h_item, subject, what, changes, checked,
		options, recommended, status, cause_counter, sent_at, deadline)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		fmt.Sprintf("Q-%s-%d", c.req.project, n), c.req.id, hitem, subject,
		firstNonEmpty(content.What, fmt.Sprintf("%s: %s needs a decision (%s)", hitem, subject, c.rowID)),
		firstNonEmpty(content.Changes, "nothing changes until you answer"),
		firstNonEmpty(content.Checked, "state machine row "+c.rowID),
		jsonText(opts), recommended, nullStr(cause), fmtTime(c.now), fmtTime(deadline))
	if err != nil {
		return err
	}
	c.req.holdDeadline, c.req.dirty = fmtTime(deadline.Truncate(time.Nanosecond)), true
	return nil
}
