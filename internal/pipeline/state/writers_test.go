package state

import (
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The columns only this package may write (C-01). TestStateColumnsExist ties
// them to the migration.

func reqHoldCols() []string {
	return []string{"hold"}
}

func stepStateCols() []string {
	return []string{"state", "stage", "reason", "resume_stage"}
}

var (
	wsRe = regexp.MustCompile(`\s+`)
	// any INSERT/REPLACE into the two tables creates state/hold values
	insertRe = regexp.MustCompile(`\b(insert(\s+or\s+\w+)?\s+into|replace\s+into)\s+(steps|requirements)\b`)
	updateRe = regexp.MustCompile(`\bupdate\s+(steps|requirements)\s+set\b(.*)`)
)

// writesStateSQL reports whether a SQL string literal writes step state
// columns or the requirement hold column.
func writesStateSQL(lit string) bool {
	s := wsRe.ReplaceAllString(strings.ToLower(lit), " ")
	if insertRe.MatchString(s) {
		return true
	}
	m := updateRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	set, _, _ := strings.Cut(m[2], " where ")
	cols := stepStateCols()
	if m[1] == "requirements" {
		cols = reqHoldCols()
	}
	for _, c := range cols {
		if regexp.MustCompile(`(^|[\s,])` + c + `\s*=`).MatchString(set) {
			return true
		}
	}
	return false
}

// stateWriteViolations scans every Go file under root, except the
// internal/pipeline/state directory, for string literals that write the
// guarded columns.
func stateWriteViolations(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") || rel == "internal/pipeline/state" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil && writesStateSQL(s) {
				out = append(out, fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	sort.Strings(out)
	return out, err
}

// TestOnlyTransitionWritesState: no SQL literal outside this package updates
// or inserts steps.state/stage/reason/resume_stage or requirements.hold.
func TestOnlyTransitionWritesState(t *testing.T) {
	v, err := stateWriteViolations(repoRootDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v {
		t.Errorf("%s: SQL writes step state or requirement hold outside internal/pipeline/state (C-01)", s)
	}
}

func TestOnlyTransitionWritesStateDetectsViolations(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bad := map[string]string{
		"a/upd.go":       "package a\nvar q = `UPDATE steps SET state = 'done' WHERE id = ?`\n",
		"a/multi.go":     "package a\nvar q = `update   steps\n   SET deadline = ?,\n       stage = ?\n WHERE id = ?`\n",
		"a/reason.go":    "package a\nvar q = \"UPDATE steps SET reason=?\"\n",
		"a/resume.go":    "package a\nvar q = \"UPDATE steps SET resume_stage = ? WHERE id = ?\"\n",
		"a/hold.go":      "package a\nvar q = \"UPDATE requirements SET hold = 'owner' WHERE id = ?\"\n",
		"a/ins.go":       "package a\nvar q = \"INSERT INTO steps (id, state) VALUES (?, 'queued')\"\n",
		"a/orrep.go":     "package a\nvar q = \"INSERT OR REPLACE INTO requirements (id, hold) VALUES (?, 'none')\"\n",
		"a/rep.go":       "package a\nvar q = \"REPLACE INTO steps (id) VALUES (?)\"\n",
		"a/test_test.go": "package a\nvar q = \"update steps set state = 'x'\"\n",
	}
	good := map[string]string{
		"b/ok1.go": "package b\nvar q = \"UPDATE steps SET deadline = ?, counters = ? WHERE id = ?\"\n",
		"b/ok2.go": "package b\nvar q = \"UPDATE requirements SET session_repo_state = ?, hold_deadline = ? WHERE id = ?\"\n",
		"b/ok3.go": "package b\nvar q = \"UPDATE pull_requests SET state = 'merged' WHERE req_id = ?\"\n",
		"b/ok4.go": "package b\nvar q = \"UPDATE steps SET commit_sha = ? WHERE state = 'done' AND stage = 'review'\"\n",
		"b/ok5.go": "package b\nvar q = \"SELECT state, stage FROM steps WHERE hold = 1\"\n",
		"b/ok6.go": "package b\nvar q = \"UPDATE asks SET status = 'moot', state = 1\"\n",
		// the state package itself is exempt
		"internal/pipeline/state/x.go": "package state\nvar q = \"UPDATE steps SET state = ?\"\n",
	}
	for n, s := range bad {
		write(n, s)
	}
	for n, s := range good {
		write(n, s)
	}
	v, err := stateWriteViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != len(bad) {
		t.Fatalf("got %d violations, want %d:\n%s", len(v), len(bad), strings.Join(v, "\n"))
	}
	for n := range bad {
		found := false
		for _, s := range v {
			found = found || strings.Contains(filepath.ToSlash(s), n)
		}
		if !found {
			t.Errorf("violation in %s not detected", n)
		}
	}
}

// TestStateColumnsExist keeps the guarded column names in step with the
// migration.
func TestStateColumnsExist(t *testing.T) {
	e := newEnv(t)
	for table, cols := range map[string][]string{"steps": stepStateCols(), "requirements": reqHoldCols()} {
		have := map[string]bool{}
		err := e.db.Read(e.ctx, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				have[n] = true
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			if !have[c] {
				t.Errorf("%s has no column %s", table, c)
			}
		}
	}
}
