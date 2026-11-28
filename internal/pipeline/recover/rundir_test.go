package recover

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/pipeline/state"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/spec/model"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

func mkfile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestRemoveRunDirStaysInsideRuns(t *testing.T) {
	home := shortHome(t)
	runs := filepath.Join(home, "runs")
	outside := filepath.Join(filepath.Dir(home), "outside")
	mkfile(t, filepath.Join(runs, "r1", "result.json"))
	mkfile(t, filepath.Join(outside, "keep.txt"))
	if err := os.Symlink(outside, filepath.Join(runs, "r1", "inner")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runs, "lnk")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRunDir(home, "r1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(runs, "r1")) || !exists(filepath.Join(outside, "keep.txt")) {
		t.Error("r1 not removed, or a symlink inside it was followed")
	}
	if err := RemoveRunDir(home, "lnk"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(runs, "lnk")) || !exists(filepath.Join(outside, "keep.txt")) {
		t.Error("the link was not removed as a link, or its target was touched")
	}
	if err := RemoveRunDir(home, "missing"); err != nil {
		t.Errorf("a missing directory is an error: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "../outside", "a/b"} {
		if err := RemoveRunDir(home, bad); err == nil {
			t.Errorf("run id %q accepted", bad)
		}
	}
	if !exists(outside) || !exists(runs) {
		t.Error("a traversal id removed something outside a run directory")
	}
}

func insertRun(t *testing.T, ctx context.Context, st *sqlite.Stores, id, status string) {
	t.Helper()
	err := st.Work.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO runs (id, req_id, role, status, model, scope, spec_version, harness, dir, config_rev, input, started_at)
			VALUES (?, 'q', 'coder', ?, 'm', 'step', 1, 'scripted', ?, '', '{}', '2026-01-01T00:00:00Z')`, id, status, "d/"+id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSweepRunDirsKeepsOnlyRunningRuns(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	home := shortHome(t)
	if _, err := Preflight(home, "p"); err != nil {
		t.Fatal(err)
	}
	stores, err := sqlite.Open(ctx, filepath.Join(home, "db"), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stores.Close() })
	req := model.Requirement{ID: "q", Kind: model.KindFeature, Source: model.SourceOwner, Priority: model.P2, SpecVersion: 1,
		Branch: "foreman/q", Hold: model.HoldNone, Status: model.StatusSubmitted, Size: model.SizeS, Spec: model.Spec{Title: "t", Goal: "g"}}
	if err := state.InsertRequirement(ctx, stores.Work, req); err != nil {
		t.Fatal(err)
	}
	for id, status := range map[string]string{"run1": "running", "pnd": "pending", "end": "ended", "kld": "killed"} {
		insertRun(t, ctx, stores, id, status)
		mkfile(t, filepath.Join(home, "runs", id, "f"))
	}
	mkfile(t, filepath.Join(home, "runs", "norow", "f")) // a directory with no Run at all
	led := ledger.New()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meter.NewStore(), Ledger: led})
	rep, err := Startup(ctx, Deps{Stores: stores, Gate: g, Ledger: led, Project: "p", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"run1": true, "pnd": true, "end": false, "kld": false, "norow": false} {
		if got := exists(filepath.Join(home, "runs", id)); got != want {
			t.Errorf("runs/%s exists = %v, want %v", id, got, want)
		}
	}
	if last := rep.Checks[len(rep.Checks)-1]; last.Name != "run-dirs" || !last.OK {
		t.Errorf("last startup line = %s", last)
	}
}

// TestRemoveRunDirWithReadOnlyModCache: a Go module cache written without
// -modcacherw is read-only; the run directory still goes away (D-086), and a
// symlink inside it is not followed.
func TestRemoveRunDirWithReadOnlyModCache(t *testing.T) {
	home := shortHome(t)
	runs := filepath.Join(home, "runs")
	outside := filepath.Join(filepath.Dir(home), "outside")
	mkfile(t, filepath.Join(outside, "keep.txt"))
	mod := filepath.Join(runs, "r1", "gomodcache", "example.com", "m@v1")
	mkfile(t, filepath.Join(mod, "sub", "f.go"))
	if err := os.Symlink(outside, filepath.Join(mod, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(mod, "sub", "f.go"), filepath.Join(mod, "sub"), mod, filepath.Dir(mod)} {
		if err := os.Chmod(p, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveRunDir(home, "r1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(runs, "r1")) {
		t.Error("run dir still there")
	}
	if !exists(filepath.Join(outside, "keep.txt")) {
		t.Error("a symlink target outside runs/ was removed")
	}
}

// TestPreflightSameVolume: projects/, cache/ and runs/ of a normal home share
// one volume; a path on another one is refused (D-047).
func TestPreflightSameVolume(t *testing.T) {
	home := shortHome(t)
	cs, err := Preflight(home, "p")
	if err != nil || cs[len(cs)-1].Name != "volume" || !cs[len(cs)-1].OK {
		t.Fatalf("Preflight = %v, %v", cs, err)
	}
	if err := checkSameVolume([]string{home, home}); err != nil {
		t.Fatal(err)
	}
	if err := checkSameVolume([]string{home, "/dev"}); err == nil {
		t.Error("home and /dev reported on one volume")
	}
}
