package state

import (
	"fmt"
	"strings"
)

// executor runs one effect. arg is the text after the effect name's first
// colon ("check_fail,review_rounds" for grant_one:check_fail,review_rounds).
type executor func(c *txc, arg string) error

// executors maps every effect name of the state-machine.yaml effects catalog
// to its executor. The key is the catalog name without its <...> argument
// placeholder; "add_findings:check" is a catalog name of its own.
// TestEffectsAllExecuted keeps this set equal to the catalog.
func executors() map[string]executor {
	return map[string]executor{
		"inc":                             fxInc,
		"reset":                           fxReset,
		"grant_one":                       fxGrantOne,
		"set":                             fxSet,
		"create_run":                      fxCreateRun,
		"create_run_when_drained":         fxCreateRunWhenDrained,
		"kill_run":                        fxKillRun,
		"kill_runs":                       fxKillRuns,
		"add_findings":                    fxAddFindings,
		"add_findings:check":              fxAddCheckFindings,
		"add_fix_step":                    fxAddFixStep,
		"req_hold":                        fxReqHold,
		"record_resume_stage":             fxRecordResumeStage,
		"extend_deadline":                 fxExtendDeadline,
		"alert_starved":                   fxAlertStarved,
		"inject_denial":                   fxInjectDenial,
		"inject_guidance":                 fxInjectGuidance,
		"grant_access":                    fxGrantAccess,
		"release_steps":                   fxReleaseSteps,
		"drop_all_steps":                  fxDropAllSteps,
		"drop_held_step":                  fxDropHeldStep,
		"apply_plan_diff":                 fxApplyPlanDiff,
		"append_trigger_to_planner_input": fxAppendPlannerInput,
		"append_trigger_to_rescuer_input": fxAppendRescuerInput,
		"create_owner_ask":                fxCreateOwnerAsk,
		"notify":                          fxNotify,
		"release_session_repo":            fxReleaseSessionRepo,
	}
}

// resolveEffect finds the executor of an effect string of a table row.
func resolveEffect(m map[string]executor, eff string) (executor, string, bool) {
	if eff == "add_findings:check" { // a catalog name with a colon, not add_findings with argument check
		x, ok := m[eff]
		return x, "", ok
	}
	key, arg, _ := strings.Cut(eff, ":")
	x, ok := m[key]
	return x, arg, ok
}

// catalogKey turns an effects catalog name into an executors key.
func catalogKey(name string) string {
	key, _, _ := strings.Cut(name, ":<")
	return key
}

func (c *txc) runEffect(eff string) error {
	x, arg, ok := resolveEffect(c.e.exec, eff)
	if !ok {
		return fmt.Errorf("unknown effect %q", eff)
	}
	if err := x(c, arg); err != nil {
		return err
	}
	return c.flush()
}

// flush writes the in-memory rows an executor changed.
func (c *txc) flush() error {
	if c.step != nil && c.step.dirty {
		if err := c.saveStep(c.step); err != nil {
			return err
		}
	}
	if c.subj != nil && c.subj.dirty {
		if err := c.saveStep(c.subj); err != nil {
			return err
		}
	}
	if c.req.dirty {
		return c.saveReq()
	}
	return nil
}

// Payload types carried by Event.

// FindingIn is one review finding of a reviewer result (protocol-run.yaml).
type FindingIn struct {
	Severity  string // blocking|minor
	Path      string
	Line      int
	Problem   string // <= 300 characters
	CloseWhen string // <= 200 characters
}

// AskContent is the text of an OwnerAsk. A nil Event.Ask makes the executor
// generate defaults from the H item.
type AskContent struct {
	HItem       string // H1..H7; used when the effect names none
	What        string
	Changes     string
	Checked     string
	Options     []AskOption // 2..3
	Recommended string      // option id
}

// AskOption is one choice of an OwnerAsk.
type AskOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
