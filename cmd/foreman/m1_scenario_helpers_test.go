package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/harness/api"
	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/state"
)

// scriptQueue hands out one scripted-model script per Run: the n-th launch of
// a role gets the n-th pushed script, and the last one repeats.
type scriptQueue struct {
	t      *testing.T
	dir    string
	byRole map[api.Role][]string
	launch map[api.Role]int
}

func newScriptQueue(t *testing.T) *scriptQueue {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &scriptQueue{t: t, dir: dir, byRole: map[api.Role][]string{}, launch: map[api.Role]int{}}
}

func (q *scriptQueue) push(role api.Role, sc scripted.Script) {
	q.t.Helper()
	raw, err := json.Marshal(sc)
	if err != nil {
		q.t.Fatal(err)
	}
	p := filepath.Join(q.dir, fmt.Sprintf("%s%d.json", role, len(q.byRole[role])))
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		q.t.Fatal(err)
	}
	q.byRole[role] = append(q.byRole[role], p)
}

// pick is Config.ScriptFor.
func (q *scriptQueue) pick(role api.Role, _ string) string {
	list := q.byRole[role]
	if len(list) == 0 {
		return ""
	}
	i := q.launch[role]
	q.launch[role]++
	return list[min(i, len(list)-1)]
}

// coderCommits writes files, commits them and reports ok.
func coderCommits(msg string, files map[string]string) scripted.Script {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var acts []scripted.Action
	for _, n := range names {
		acts = append(acts, scripted.Action{Write: &scripted.Write{Path: n, Content: files[n]}})
	}
	acts = append(acts,
		scripted.Action{Git: []string{"add", "-A"}},
		scripted.Action{Git: []string{"commit", "-q", "-m", msg}},
		scripted.Action{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],` +
			`"acceptance":[{"id":"A1","result":"pass","evidence":"go test"}]}`}},
		scripted.Action{Finish: &scripted.Finish{Turns: 2, Input: 100, Output: 20, Text: "done"}})
	return scripted.Script{Actions: acts}
}

// coderBadResult exits cleanly after writing a result.json that fails
// validation (PP-4: bad_result, not infra).
func coderBadResult() scripted.Script {
	return scripted.Script{Actions: []scripted.Action{
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"role":"coder","status":"ok"}`}},
		{Finish: &scripted.Finish{Turns: 1, Input: 10, Output: 2, Text: "oops"}},
	}}
}

// reviewerSays writes a reviewer result; findings are blocking, at path:line.
func reviewerSays(verdict, path string, line int, problems ...string) scripted.Script {
	var fs []string
	for _, p := range problems {
		fs = append(fs, fmt.Sprintf(`{"scope":%q,"severity":"blocking","location":{"path":%q,"line":%d},"problem":%q,"close_when":"the finding no longer applies"}`,
			step1, path, line, p))
	}
	return scripted.Script{Actions: []scripted.Action{
		{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"reviewer","status":"ok","verdict":"` +
			verdict + `","findings":[` + strings.Join(fs, ",") + `]}`}},
		{Finish: &scripted.Finish{Turns: 1, Input: 50, Output: 5, Text: verdict}},
	}}
}

// scenario is one composed daemon with the fast-lane requirement seeded and
// a script queue per role; tests drive it tick by tick.
type scenario struct {
	t    *testing.T
	f    *fixture
	app  *App
	clk  *clock.Fake
	gh   *fakeGitHub
	q    *scriptQueue
	rows []string
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	f := newFixture(t)
	s := &scenario{t: t, f: f, clk: clock.NewFake(time.Unix(1_800_000_000, 0)), q: newScriptQueue(t)}
	gh, ghURL := newFakeGitHub(t, func(branch string) string { return f.remoteSHA("refs/heads/" + branch) })
	s.gh = gh
	gh.updateBranch = f.githubUpdateBranch
	cfg := f.config(s.clk, ghURL, func(stepID string, ev state.Event, out state.Outcome) {
		s.rows = append(s.rows, out.RowID)
		t.Logf("transition %-8s %-14s -> RowID %-8s state=%s stage=%s reason=%s", stepID, ev.Name, out.RowID, out.To.State, out.To.Stage, out.To.Reason)
	})
	cfg.Models = map[string]string{"coder": "sonnet", "reviewer": "opus"}
	cfg.ScriptFor = s.q.pick
	cfg.Guardrails = repoGuardrails(t)
	app, err := Compose(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	s.app = app
	seed(t, app, s.clk)
	return s
}

func (s *scenario) tick() {
	s.t.Helper()
	s.clk.Advance(time.Second)
	if _, err := s.app.Sched.Tick(context.Background()); err != nil {
		s.t.Fatalf("tick: %v", err)
	}
	s.app.Sched.WaitSessions() // a real process exit, not a sleep
}

// until ticks until cond holds, failing after max ticks.
func (s *scenario) until(max int, what string, cond func() bool) {
	s.t.Helper()
	for i := 0; i < max; i++ {
		if cond() {
			return
		}
		s.tick()
	}
	if !cond() {
		s.t.Fatalf("%s: not reached after %d ticks; rows %v", what, max, s.rows)
	}
}

func (s *scenario) allDone() bool {
	var open int
	query(s.t, s.app, `SELECT COUNT(*) FROM steps WHERE state <> 'done'`, &open)
	return open == 0
}

func (s *scenario) stepPos(id string) (st, stage, reason string) {
	query(s.t, s.app, fmt.Sprintf(`SELECT state, stage, COALESCE(reason, '') FROM steps WHERE id = '%s'`, id), &st, &stage, &reason)
	return
}

func (s *scenario) counter(id, name string) int {
	var raw string
	query(s.t, s.app, fmt.Sprintf(`SELECT counters FROM steps WHERE id = '%s'`, id), &raw)
	m := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		s.t.Fatal(err)
	}
	return m[name]
}

type runInfo struct{ ID, Model, Status, Outcome string }

// runsOf lists the Runs of a role, oldest first (run ids are not ordered, so
// by start time then rowid).
func (s *scenario) runsOf(role string) []runInfo {
	s.t.Helper()
	var out []runInfo
	err := s.app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, COALESCE(model, ''), status, COALESCE(outcome, '') FROM runs WHERE role = ? ORDER BY started_at, LENGTH(id), id`, role)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r runInfo
			if err := rows.Scan(&r.ID, &r.Model, &r.Status, &r.Outcome); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func models(rs []runInfo) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Model)
	}
	return out
}

// contextOf reads the context.md the Run's session was given.
func (s *scenario) contextOf(runID string) map[string]string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.f.home, "runs", runID, "context.md"))
	if err != nil {
		s.t.Fatal(err)
	}
	return sections(string(raw))
}

// sections splits a context.md into its "## title" sections.
func sections(md string) map[string]string {
	out := map[string]string{}
	var title string
	var body []string
	flush := func() {
		if title != "" {
			out[title] = strings.TrimSpace(strings.Join(body, "\n"))
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if t, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			title, body = t, nil
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

// invocationModel is the model the harness invocation of a Run carried: the
// scripted child echoes it in the stream's init line.
func (s *scenario) invocationModel(runID string) string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.f.home, "runs", runID, "stdout.log"))
	if err != nil {
		s.t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(raw), "\n")
	var init struct {
		Subtype string `json:"subtype"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal([]byte(first), &init); err != nil || init.Subtype != "init" {
		s.t.Fatalf("stdout.log of %s starts with %q", runID, first)
	}
	return init.Model
}

// wantRows checks that want is a subsequence of the transitions so far and
// logs the RowIDs.
func (s *scenario) wantRows(want ...string) {
	s.t.Helper()
	s.t.Logf("RowIDs hit: %s", strings.Join(s.rows, " "))
	pos := 0
	for _, r := range s.rows {
		if pos < len(want) && r == want[pos] {
			pos++
		}
	}
	if pos != len(want) {
		s.t.Errorf("rows %v do not contain %v in order", s.rows, want)
	}
}

func (s *scenario) count(row string) int {
	n := 0 // not strings.Count: " S50 S50 " has overlapping matches
	for _, r := range s.rows {
		if r == row {
			n++
		}
	}
	return n
}

func (s *scenario) sessionHead() string {
	s.t.Helper()
	out, err := s.f.git.Line(context.Background(), filepath.Join(s.f.home, "projects", "p", "sessions", reqID), "rev-parse", "HEAD")
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *scenario) rawContext(runID string) string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.f.home, "runs", runID, "context.md"))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(raw)
}

// diffFiles lists the files of a rendered diff section ("diff --git a/x b/x").
func diffFiles(section string) []string {
	var out []string
	for _, line := range strings.Split(section, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			name, _, _ := strings.Cut(rest, " b/")
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
