package recover

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/resource/meter"
	"github.com/andrewrong/foreman/internal/store/migrate"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

func shortHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fmr") // t.TempDir is too long for MaxHomeLen on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return filepath.Join(root, "home")
}

func names(cs []Check) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, " ")
}

// TestStartupIntegrityCheck covers what D-047 and D-068 allow to check now:
// the home path against the 70-byte run directory limit, the directory
// layout, the directory lock, PRAGMA integrity_check and the schema version.
func TestStartupIntegrityCheck(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))

	t.Run("path rules", func(t *testing.T) { startupPathRules(t) })

	home := shortHome(t)
	t.Run("layout is created, and refused when a file is in the way", func(t *testing.T) { startupLayout(t, home) })

	dbDir := filepath.Join(home, "db")
	stores, err := sqlite.Open(ctx, dbDir, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lock is exclusive", func(t *testing.T) {
		if _, err := sqlite.Open(ctx, dbDir, clk); !errors.Is(err, sqlite.ErrLocked) {
			t.Errorf("second Open = %v, want ErrLocked", err)
		}
	})
	t.Run("checks report integrity and schema version of every database", func(t *testing.T) { startupChecks(t, ctx, clk, stores, home) })
	if err := stores.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("a database from a newer binary is refused", func(t *testing.T) { startupNewerSchema(t, ctx, dbDir, clk) })
	t.Run("a damaged database file is refused", func(t *testing.T) { startupDamaged(t, ctx, dbDir, clk) })
}

func startupPathRules(t *testing.T) {
	for _, home := range []string{"relative/home", "/tmp/" + strings.Repeat("x", MaxHomeLen)} {
		cs, err := Preflight(home, "p")
		if err == nil || len(cs) == 0 || cs[len(cs)-1].OK {
			t.Errorf("home %q accepted: %v", home, cs)
		}
	}
	if _, err := Preflight(shortHome(t), "../escape"); err == nil {
		t.Error("project name with a path separator accepted")
	}
}

func startupLayout(t *testing.T, home string) {
	cs, err := Preflight(home, "p")
	if err != nil || names(cs) != "home-path layout" {
		t.Fatalf("Preflight = %v, %v", cs, err)
	}
	for _, d := range layoutDirs("p") {
		if fi, err := os.Stat(filepath.Join(home, d)); err != nil || !fi.IsDir() {
			t.Errorf("layout dir %s missing", d)
		}
	}
	other := shortHome(t)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Preflight(other, "p"); err == nil {
		t.Error("a regular file where db/ belongs was accepted")
	}
}

func startupChecks(t *testing.T, ctx context.Context, clk clock.Clock, stores *sqlite.Stores, home string) {
	led := ledger.New()
	g := gate.New(gate.Options{Clock: clk, Config: meter.DefaultConfig(), Meters: meter.NewStore(), Ledger: led})
	rep, err := Startup(ctx, Deps{Stores: stores, Gate: g, Ledger: led, Project: "p", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(rep.Checks); got != "lock integrity-global integrity-work integrity-ci orphans ledger" {
		t.Errorf("startup lines: %s", got)
	}
	for _, c := range rep.Checks {
		if !c.OK {
			t.Errorf("check failed on a fresh home: %s", c)
		}
	}
	if !strings.Contains(rep.Checks[2].Detail, "schema version") {
		t.Errorf("work line lacks the schema version: %s", rep.Checks[2])
	}
}

func startupNewerSchema(t *testing.T, ctx context.Context, dbDir string, clk clock.Clock) {
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dbDir, "work.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 'x')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := sqlite.Open(ctx, dbDir, clk); !errors.Is(err, migrate.ErrNewerSchema) {
		t.Errorf("Open on a newer schema = %v, want ErrNewerSchema", err)
	}
}

func startupDamaged(t *testing.T, ctx context.Context, dbDir string, clk clock.Clock) {
	if err := os.WriteFile(filepath.Join(dbDir, "global.db"), []byte(strings.Repeat("not sqlite ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(ctx, dbDir, clk); err == nil {
		t.Error("Open accepted a file that is not a database")
	}
}
