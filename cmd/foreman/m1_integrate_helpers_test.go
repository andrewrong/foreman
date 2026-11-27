package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/andrewrong/foreman/internal/harness/scripted"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/spec/model"
)

// repoGuardrails reads the base list of the repository's own
// spec/guardrails.yaml (the daemon's config module will do this; the test
// project is treated as foreman itself, so the foreman-scoped globs apply).
func repoGuardrails(t *testing.T) []model.Guardrail {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec", "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Base []struct{ ID, Glob string } `yaml:"base"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []model.Guardrail
	for _, g := range doc.Base {
		out = append(out, model.Guardrail{ID: g.ID, Glob: g.Glob})
	}
	if len(out) == 0 {
		t.Fatal("no guardrails in spec/guardrails.yaml")
	}
	return out
}

// pushMain commits files to the remote's default branch, as the owner or
// another requirement's merge would (base movement for update-branch).
func (f *fixture) pushMain(files map[string]string, msg string) {
	f.t.Helper()
	ctx := context.Background()
	for name, body := range files {
		p := filepath.Join(f.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	f.must(f.git.Run(ctx, f.work, "add", "-A"))
	f.must(f.git.Run(ctx, f.work, "commit", "-q", "-m", msg))
	f.must(f.git.Run(ctx, f.work, "push", "-q", f.remote, "HEAD:refs/heads/main"))
}

// githubUpdateBranch is what GitHub's update-branch does, done for real in the
// local bare remote: a merge commit of the default branch into the PR branch.
// A conflicting merge is an error (GitHub answers 422).
func (f *fixture) githubUpdateBranch(branch string) error {
	ctx := context.Background()
	head, err := f.git.Line(ctx, f.remote, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	main, err := f.git.Line(ctx, f.remote, "rev-parse", "refs/heads/main")
	if err != nil {
		return err
	}
	out, err := f.git.Run(ctx, f.remote, "merge-tree", "--write-tree", "--no-messages", head, main)
	if err != nil {
		return err
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	merge, err := f.git.Line(ctx, f.remote, "commit-tree", tree, "-p", head, "-p", main, "-m", "Merge branch 'main' into "+branch)
	if err != nil {
		return err
	}
	_, err = f.git.Run(ctx, f.remote, "update-ref", "refs/heads/"+branch, merge, head)
	return err
}

// coderFor is a coder script that commits files and reports the acceptance
// ids it was given (a fix step's are finding ids, not A1).
func coderFor(msg string, files map[string]string, ids ...string) scripted.Script {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var acts []scripted.Action
	for _, n := range names {
		acts = append(acts, scripted.Action{Write: &scripted.Write{Path: n, Content: files[n]}})
	}
	var acc []string
	for _, id := range ids {
		acc = append(acc, fmt.Sprintf(`{"id":%q,"result":"pass","evidence":"go test"}`, id))
	}
	acts = append(acts,
		scripted.Action{Git: []string{"add", "-A"}},
		scripted.Action{Git: []string{"commit", "-q", "-m", msg}},
		scripted.Action{Write: &scripted.Write{Path: "$RUN_DIR/result.json", Content: `{"schema_version":1,"run_id":"{{RUN_ID}}","role":"coder","status":"ok","commits":["{{HEAD}}"],"acceptance":[` +
			strings.Join(acc, ",") + `]}`}},
		scripted.Action{Finish: &scripted.Finish{Turns: 2, Input: 100, Output: 20, Text: "done"}})
	return scripted.Script{Actions: acts}
}

// pr reads a column of the requirement's E-PR row.
func (s *scenario) pr(col string) string {
	s.t.Helper()
	var v string
	query(s.t, s.app, fmt.Sprintf(`SELECT COALESCE(%s, '') FROM pull_requests WHERE req_id = '%s'`, col, reqID), &v)
	return v
}

func (s *scenario) reqCounter(name string) int {
	s.t.Helper()
	var raw string
	query(s.t, s.app, fmt.Sprintf(`SELECT req_counters FROM requirements WHERE id = '%s'`, reqID), &raw)
	m := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		s.t.Fatal(err)
	}
	return m[name]
}

func (s *scenario) hold() string {
	s.t.Helper()
	var h string
	query(s.t, s.app, fmt.Sprintf(`SELECT hold FROM requirements WHERE id = '%s'`, reqID), &h)
	return h
}

// openAsks lists "<h item> <subject>" of the open asks.
func (s *scenario) openAsks() []string {
	s.t.Helper()
	var out []string
	err := s.app.Stores.Work.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT h_item, subject FROM asks WHERE status = 'open' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h, subj string
			if err := rows.Scan(&h, &subj); err != nil {
				return err
			}
			out = append(out, h+" "+subj)
		}
		return rows.Err()
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

// deadlineOf is the step's deadline (zero when none).
func (s *scenario) deadlineOf(id string) time.Time {
	s.t.Helper()
	var d string
	query(s.t, s.app, fmt.Sprintf(`SELECT COALESCE(deadline, '') FROM steps WHERE id = '%s'`, id), &d)
	if d == "" {
		return time.Time{}
	}
	at, err := time.Parse("2006-01-02T15:04:05.000000000Z07:00", d)
	if err != nil {
		s.t.Fatal(err)
	}
	return at
}

// ownerAnswers is the owner's answer to the open ask (H21 and the other table
// B rows), as the notify transport will deliver it.
func (s *scenario) ownerAnswers(answer string) state.ReqOutcome {
	s.t.Helper()
	out, err := s.app.Engine.TransitionReq(context.Background(), s.app.Stores.Work, reqID, state.Event{Name: "owner_answer", Answer: answer})
	if err != nil {
		s.t.Fatal(err)
	}
	s.rows = append(s.rows, out.RowID)
	s.t.Logf("owner_answer %s -> RowID %s", answer, out.RowID)
	return out
}

// atStep ticks until the step reaches the state/stage/reason.
func (s *scenario) atStep(id, st, stage, reason string, max int) {
	s.t.Helper()
	s.until(max, fmt.Sprintf("%s at %s/%s/%s", id, st, stage, reason), func() bool {
		a, b, c := s.stepPos(id)
		return a == st && b == stage && c == reason
	})
}

// remoteHead is the tip of the requirement branch on the remote.
func (s *scenario) remoteHead() string { return s.f.remoteSHA("refs/heads/foreman/" + reqID) }
