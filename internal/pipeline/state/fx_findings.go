package state

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxProblem   = 300
	maxCloseWhen = 200
	maxLogLines  = 80
	maxLogBytes  = 8000
)

func (c *txc) nextRound(scope, kind string) (int, error) {
	var r int
	err := c.tx.QueryRowContext(c.ctx, `SELECT COALESCE(MAX(round), 0) + 1 FROM findings
		WHERE scope = ? AND kind = ?`, scope, kind).Scan(&r)
	return r, err
}

func (c *txc) insertFinding(kind, scope, severity string, location, tests any, logTail, problem, closeWhen string, round int) error {
	n, err := c.nextSeq("finding")
	if err != nil {
		return err
	}
	id := fmt.Sprintf("F%d", n)
	_, err = c.tx.ExecContext(c.ctx, `INSERT INTO findings (id, req_id, kind, scope, severity, location,
		failing_tests, log_tail, problem, close_when, status, round)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
		id, c.req.id, kind, scope, severity, location, tests, nullIfNil(logTail, kind == "check"), problem, closeWhen, round)
	if err == nil {
		c.newFindings = append(c.newFindings, id)
	}
	return err
}

func nullIfNil(s string, keep bool) any {
	if keep {
		return s
	}
	return nil
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// fxAddFindings inserts the review findings of the Run result (kind=review).
func fxAddFindings(c *txc, _ string) error {
	if c.step == nil {
		return fmt.Errorf("add_findings needs a step")
	}
	if len(c.ev.Findings) == 0 {
		return fmt.Errorf("add_findings: event %s carries no findings", c.ev.Name)
	}
	round, err := c.nextRound(c.step.id, "review")
	if err != nil {
		return err
	}
	for i, f := range c.ev.Findings {
		if f.Severity != "blocking" && f.Severity != "minor" {
			return fmt.Errorf("finding %d: severity %q", i, f.Severity)
		}
		if f.Problem == "" || f.CloseWhen == "" || f.Path == "" {
			return fmt.Errorf("finding %d: problem, close_when and path are required", i)
		}
		if utf8.RuneCountInString(f.Problem) > maxProblem || utf8.RuneCountInString(f.CloseWhen) > maxCloseWhen {
			return fmt.Errorf("finding %d: problem/close_when too long", i)
		}
		loc := jsonText(map[string]any{"path": f.Path, "line": f.Line})
		if err := c.insertFinding("review", c.step.id, f.Severity, loc, nil, "", f.Problem, f.CloseWhen, round); err != nil {
			return err
		}
	}
	return nil
}

// tailLines keeps the last maxLogLines lines and at most maxLogBytes bytes.
func tailLines(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	s = strings.Join(lines, "\n")
	if len(s) > maxLogBytes {
		s = s[len(s)-maxLogBytes:]
		for !utf8.ValidString(s) { // do not start in the middle of a rune
			s = s[1:]
		}
	}
	return s
}

// fxAddCheckFindings inserts one kind=check finding: failing tests and the
// tail of the log (PF-CTX renders it as "previous failure").
func fxAddCheckFindings(c *txc, _ string) error {
	if c.step == nil {
		return fmt.Errorf("add_findings:check needs a step")
	}
	round, err := c.nextRound(c.step.id, "check")
	if err != nil {
		return err
	}
	tests := nonNil(c.ev.FailingTests)
	problem := "check failed"
	if len(tests) > 0 {
		problem = "check failed: " + strings.Join(tests, ", ")
	}
	return c.insertFinding("check", c.step.id, "blocking", nil, jsonText(tests), tailLines(c.ev.LogTail),
		truncRunes(problem, maxProblem), "the failing checks pass", round)
}

// closeFindings closes the step's open findings of one kind: a passing check
// closes the check finding (the next context.md must not show a failure that
// was fixed) and an approving review closes the review findings it was shown.
func (c *txc) closeFindings(kind string) error {
	_, err := c.tx.ExecContext(c.ctx, `UPDATE findings SET status = 'closed'
		WHERE scope = ? AND kind = ? AND status = 'open'`, c.step.id, kind)
	return err
}

// fxAddFixStep inserts the fix step for the integrate step: queued(code),
// acceptance = the findings this event just wrote, touches of the integrate
// step.
func fxAddFixStep(c *txc, _ string) error {
	in := c.step
	if in == nil || in.typ != "integrate" {
		return fmt.Errorf("add_fix_step needs an integrate step")
	}
	if len(c.newFindings) == 0 {
		return fmt.Errorf("add_fix_step: no findings written before it")
	}
	var seq int
	if err := c.tx.QueryRowContext(c.ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM steps WHERE req_id = ?`, c.req.id).Scan(&seq); err != nil {
		return err
	}
	var problem string
	if err := c.tx.QueryRowContext(c.ctx, `SELECT problem FROM findings WHERE id = ?`, c.newFindings[0]).Scan(&problem); err != nil {
		return err
	}
	goal := truncRunes(fmt.Sprintf("fix for %s: %s", in.id, problem), 300)
	_, err := c.tx.ExecContext(c.ctx, `INSERT INTO steps (id, req_id, seq, type, code_role, goal, acceptance,
		depends_on, touches, state, stage, deadline, counters, fix_for)
		VALUES (?, ?, ?, 'fix', 'coder', ?, ?, '[]', ?, 'queued', 'code', ?, '{}', ?)`,
		fmt.Sprintf("%s/S%d", c.req.id, seq), c.req.id, seq, goal, jsonText(c.newFindings),
		jsonText(in.touches), fmtTime(c.now.Add(c.e.cfg.Deadlines.Queued)), in.id)
	return err
}
