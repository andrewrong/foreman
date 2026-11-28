package state

import (
	"fmt"
	"strconv"
)

func aiRoles() map[string]bool {
	return map[string]bool{"intake": true, "planner": true, "coder": true, "reviewer": true,
		"rescuer": true, "diagnoser": true, "improver": true}
}

// modelFor picks Run.model: the configured model of the role, upgraded for a
// coder Run once check_fail or review_rounds reached cap-1 (D-044 / skills.yaml
// coder.retry_upgrade).
func (c *txc) modelFor(role string, s *stepRec) string {
	if !aiRoles()[role] {
		return ""
	}
	if (role == "coder" || role == "diagnoser") && s != nil {
		for _, n := range []string{"check_fail", "review_rounds"} {
			if s.counters[n] >= dimCounters[n]-1 {
				return c.e.cfg.RetryUpgrade
			}
		}
	}
	return c.e.cfg.Models[role]
}

// createRun inserts a pending E-RUN; the reconciler starts it. step is the
// step the Run belongs to (nil for requirement-scoped Runs).
func (c *txc) createRun(role string, step *stepRec) (string, error) {
	n, err := c.nextSeq("run")
	if err != nil {
		return "", err
	}
	id := "r" + strconv.FormatInt(int64(n), 36)
	var stepID, scope, snapshot, model, harness any
	var input []string
	if step != nil {
		stepID = step.id
	}
	if role == "rescuer" {
		scope = "requirement"
		if step != nil {
			scope = "step"
		}
		input = c.req.rescuerInput
	}
	if role == "planner" {
		input = c.req.plannerInput
		if snapshot, err = c.snapshot(id); err != nil {
			return "", err
		}
	}
	if aiRoles()[role] {
		harness = c.e.cfg.Harness
	}
	model = nullStr(c.modelFor(role, step))
	now := fmtTime(c.now)
	_, err = c.tx.ExecContext(c.ctx, `INSERT INTO runs (id, req_id, step_id, role, status, model, scope,
		spec_version, snapshot_id, harness, dir, config_rev, input, started_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.req.id, stepID, role, model, scope, c.req.specVersion, snapshot, harness,
		c.e.cfg.RunsDir+"/"+id, c.e.cfg.ConfigRev, jsonText(nonNil(input)), now)
	return id, err
}

// snapshot stores the planner's step snapshot (GEM-4) and returns its id.
func (c *txc) snapshot(runID string) (string, error) {
	steps, err := loadSteps(c.ctx, c.tx, c.req.id)
	if err != nil {
		return "", err
	}
	type snap struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		State string `json:"state"`
		Stage string `json:"stage"`
	}
	list := []snap{}
	for _, s := range steps {
		list = append(list, snap{s.id, s.typ, s.state, s.stage})
	}
	id := "snap-" + runID
	_, err = c.tx.ExecContext(c.ctx, `INSERT INTO plan_snapshots (id, req_id, created_at, steps) VALUES (?, ?, ?, ?)`,
		id, c.req.id, fmtTime(c.now), jsonText(list))
	return id, err
}

func fxCreateRun(c *txc, role string) error {
	switch role {
	case "planner", "rescuer":
		var step *stepRec
		if c.step != nil && !c.inReq {
			step = c.step // step-scoped rescuer from the escalate expansion
		}
		if role == "planner" {
			step = nil
		}
		_, err := c.createRun(role, step)
		return err
	case "coder":
		if c.step == nil {
			return fmt.Errorf("create_run:coder needs a step")
		}
		role = c.step.codeRole // create_run:coder means create_run:<code_role>
	case "check", "reviewer", "ci_watch", "merge":
		if c.step == nil {
			return fmt.Errorf("create_run:%s needs a step", role)
		}
	default:
		return fmt.Errorf("create_run: unknown role %q", role)
	}
	_, err := c.createRun(role, c.step)
	return err
}

func fxCreateRunWhenDrained(c *txc, role string) error {
	if role != "planner" && role != "rescuer" {
		return fmt.Errorf("create_run_when_drained: unsupported role %q", role)
	}
	c.req.pendingRun, c.req.dirty = role, true
	return nil
}

// drain creates the pending Run once the requirement is drained: no running
// step and no waiting(rescue|ci) step (R2-M3). Every Transition re-checks it.
func (c *txc) drain() error {
	if c.req.pendingRun == "none" {
		return nil
	}
	var busy int
	if err := c.tx.QueryRowContext(c.ctx, `SELECT COUNT(*) FROM steps WHERE req_id = ?
		AND (state = 'running' OR (state = 'waiting' AND reason IN ('rescue', 'ci')))`, c.req.id).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return nil
	}
	if _, err := c.createRun(c.req.pendingRun, nil); err != nil {
		return err
	}
	c.req.pendingRun, c.req.dirty = "none", true
	return c.flush()
}

func (c *txc) killRuns(where string, arg any) error {
	// A started Run is marked for the reconciler to kill; one that never
	// started is simply closed.
	if _, err := c.tx.ExecContext(c.ctx, `UPDATE runs SET status = 'killing' WHERE status = 'running' AND `+where, arg); err != nil {
		return err
	}
	_, err := c.tx.ExecContext(c.ctx, `UPDATE runs SET status = 'killed' WHERE status = 'pending' AND `+where, arg)
	return err
}

func fxKillRun(c *txc, _ string) error {
	if c.step == nil {
		return fmt.Errorf("kill_run needs a step")
	}
	return c.killRuns(`step_id = ?`, c.step.id)
}

func fxKillRuns(c *txc, _ string) error { return c.killRuns(`req_id = ?`, c.req.id) }
