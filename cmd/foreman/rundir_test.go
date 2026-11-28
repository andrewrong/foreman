package main

import (
	"os"
	"path/filepath"
	"testing"
)

func runEntries(t *testing.T, home string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(home, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunDirRemovedAfterRun (D-086): a run directory lives only while its
// session runs. After a full scripted run runs/ is empty; a directory a crash
// left behind is removed by the next startup (a link in runs/ as a link, its
// target untouched) while the directory of a session that is still running is
// kept.
func TestRunDirRemovedAfterRun(t *testing.T) {
	t.Parallel()
	t.Run("runs is empty after a full run", runDirsEmptyAfterRun)
	t.Run("startup removes leftovers and keeps a running session", runDirsSweptAtStartup)
}

func runDirsEmptyAfterRun(t *testing.T) {
	e := newCrashEnv(t, false)
	app, _ := e.open()
	seed(t, app, e.clk)
	e.drive(app, e.allDone(app))
	if n := e.count(app, `SELECT COUNT(*) FROM runs`); n < 4 {
		t.Fatalf("only %d runs in the store: the lane did not really run", n)
	}
	if got := runEntries(t, e.f.home); len(got) != 0 {
		t.Errorf("runs/ after the run holds %v, want it empty", got)
	}
	// The results are in the store, not in the directories.
	if n := e.count(app, `SELECT COUNT(*) FROM runs WHERE status = 'ended' AND outcome <> ''`); n == 0 {
		t.Error("no ended run with an outcome in the store")
	}
}

func runDirsSweptAtStartup(t *testing.T) {
	e := newCrashEnv(t, true)
	app1, _ := e.open()
	seed(t, app1, e.clk)
	runID, dir, _ := e.coderStarted(app1)
	runs := filepath.Join(e.f.home, "runs")
	if got := runEntries(t, e.f.home); len(got) != 1 || got[0] != runID {
		t.Fatalf("runs/ while one session runs holds %v, want only %s", got, runID)
	}
	// What a crash between the commit of a result and the delete leaves, and
	// what an attacker-controlled session could plant: a directory of an
	// ended Run and a symlink pointing outside runs/.
	outside := filepath.Join(filepath.Dir(e.f.home), "outside")
	writeFile(t, filepath.Join(outside, "keep.txt"), "must survive")
	leftover := filepath.Join(runs, "gone")
	writeFile(t, filepath.Join(leftover, "result.json"), "{}")
	if err := os.Symlink(outside, filepath.Join(runs, "lnk")); err != nil {
		t.Fatal(err)
	}
	// A symlink inside a leftover directory must not be followed either.
	if err := os.Symlink(outside, filepath.Join(leftover, "inner")); err != nil {
		t.Fatal(err)
	}
	e.crash(app1)

	app2, _ := e.open() // startup sweep
	if got := runEntries(t, e.f.home); len(got) != 1 || got[0] != runID {
		t.Errorf("runs/ after the startup sweep holds %v, want only the running session %s", got, runID)
	}
	if _, err := os.Stat(filepath.Join(dir, "pid.json")); err != nil {
		t.Errorf("the running session's directory lost its files: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(b) != "must survive" {
		t.Errorf("the target of a symlink in runs/ was touched: %q %v", b, err)
	}
	// And the lane still completes: the sweep did not disturb the session.
	e.release()
	app1.Sched.WaitSessions()
	e.drive(app2, e.allDone(app2))
	if got := runEntries(t, e.f.home); len(got) != 0 {
		t.Errorf("runs/ at the end holds %v, want it empty", got)
	}
}
