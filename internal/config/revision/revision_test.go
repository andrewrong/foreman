package revision

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/config/schema"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

type env struct {
	t    *testing.T
	s    *Store
	home string
	db   *sqlite.DB
	clk  *clock.Fake
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "db"), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	home := t.TempDir()
	return &env{t: t, s: New(st.Global, home, clk), home: home, db: st.Global, clk: clk}
}

func (e *env) save(actor Actor, rel, body string) Saved {
	e.t.Helper()
	e.clk.Advance(time.Minute)
	r, err := e.s.Save(context.Background(), actor, rel, []byte(body))
	if err != nil {
		e.t.Fatalf("save %s: %v", rel, err)
	}
	return r
}

func (e *env) file(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.home, schema.Dir, rel))
	return string(b), err == nil
}

const projBody = "repo_path = \"/work/demo\"\ngithub_repo = \"example/demo\"\ndefault_branch = \"main\"\nl0 = [[\"go\", \"test\", \"./...\"]]\n"

// rt is a complete runtime.toml (owner keys set) with the given worker count.
func rt(workers int) string {
	return "secrets_dir = \"/srv/example/secrets\"\n[parallel]\nworkers = " + strconv.Itoa(workers) + "\n"
}

// rolesBody is a complete roles.toml: the owner-required timeout of every
// role, plus extra lines for the coder.
func rolesBody(coderExtra string) string {
	var b strings.Builder
	for _, n := range schema.RoleNames {
		b.WriteString("[role." + n + "]\ntimeout = \"30m\"\n")
		if n == "coder" {
			b.WriteString(coderExtra)
		}
	}
	return b.String()
}

func TestConfigRevisionRollback(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r1 := e.save(Owner, "runtime.toml", rt(4))
	r2 := e.save(Owner, "roles.toml", rolesBody("model = \"opus\"\n"))
	r3 := e.save(Owner, "projects/demo.toml", projBody)
	r4 := e.save(Owner, "runtime.toml", rt(8))
	if !(r1.Rev < r2.Rev && r2.Rev < r3.Rev && r3.Rev < r4.Rev) || !r1.Applied {
		t.Fatalf("revs %v %v %v %v", r1, r2, r3, r4)
	}

	// Revision rows carry rev, diff, actor, time.
	var diff, actor, at string
	err := e.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT diff, actor, time FROM config_revisions WHERE rev=?`, r4.Rev).Scan(&diff, &actor, &at)
	})
	if err != nil {
		t.Fatal(err)
	}
	if actor != "owner" || at != "2026-11-28T09:04:00Z" {
		t.Errorf("actor=%q time=%q", actor, at)
	}
	if !strings.Contains(diff, "-workers = 4") || !strings.Contains(diff, "+workers = 8") || strings.Contains(diff, "roles.toml") {
		t.Errorf("diff = %q", diff)
	}

	// Roll back to r2: runtime.toml returns to workers=4, project file is gone.
	rb, err := e.s.Rollback(ctx, r2.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Rev <= r4.Rev || !rb.Applied {
		t.Errorf("rollback recorded as %+v", rb)
	}
	if got, _ := e.file("runtime.toml"); got != rt(4) {
		t.Errorf("runtime.toml = %q", got)
	}
	if _, ok := e.file("projects/demo.toml"); ok {
		t.Error("project file should be removed by rollback to r2")
	}
	if got, _ := e.file("roles.toml"); !strings.Contains(got, "opus") {
		t.Errorf("roles.toml = %q", got)
	}
	eff, err := schema.Load(e.home)
	if err != nil || eff.Runtime.Parallel.Workers != 4 {
		t.Fatalf("load after rollback: %v %+v", err, eff)
	}
	if cur, _ := e.s.Current(ctx); cur != rb.Rev {
		t.Errorf("Current = %d, want %d", cur, rb.Rev)
	}
	if err := e.s.Verify(ctx); err != nil {
		t.Errorf("Verify after rollback: %v", err)
	}
	if _, err := e.s.Rollback(ctx, 9999); !errors.Is(err, ErrNoSuchRev) {
		t.Errorf("unknown rev: %v", err)
	}
}

func TestConfigSaveRejectsInvalid(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, c := range []struct{ rel, body string }{
		{"runtime.toml", "workerz = 1\n"},
		{"roles.toml", "[role.coder]\neffort = \"turbo\"\n"},
		{"projects/demo.toml", "default_branch = \"main\"\n"},
		{"projects/demo.toml", "repo_path = \"/r\"\ngithub_token = \"gh" + "p_abcdefghijklmnopqrstuvwxyz0123456789\"\n"},
		{"../escape.toml", "x = 1\n"},
		{"projects/../runtime.toml", "x = 1\n"},
	} {
		if _, err := e.s.Save(ctx, Owner, c.rel, []byte(c.body)); err == nil {
			t.Errorf("%s accepted", c.rel)
		} else if strings.Contains(err.Error(), "ghp_") {
			t.Errorf("error echoes a secret: %v", err)
		}
		if _, err := e.s.Save(ctx, AI, c.rel, []byte(c.body)); err == nil {
			t.Errorf("%s accepted as proposal", c.rel)
		}
	}
	if cur, _ := e.s.Current(ctx); cur != 0 {
		t.Errorf("invalid saves created revision %d", cur)
	}
	if _, err := os.Stat(filepath.Join(e.home, schema.Dir)); err == nil {
		t.Error("config dir created by rejected saves")
	}
	if _, err := e.s.Save(ctx, "robot", "runtime.toml", []byte(rt(3))); !errors.Is(err, ErrUnknownActor) {
		t.Errorf("unknown actor: %v", err)
	}
	e.save(Owner, "runtime.toml", rt(3))
	_, err := e.s.Save(ctx, Owner, "runtime.toml", []byte(rt(3)))
	if !errors.Is(err, ErrNoChange) {
		t.Errorf("identical save: %v", err)
	}
}

func TestConfigAIChangesAreProposals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.save(Owner, "runtime.toml", rt(2))
	before, _ := e.s.Current(ctx)

	p := e.save(AI, "runtime.toml", rt(6))
	if p.Applied || p.Rev != 0 || p.ProposalID == 0 {
		t.Fatalf("proposal result %+v", p)
	}
	if got, _ := e.file("runtime.toml"); got != rt(2) {
		t.Fatalf("AI save changed the file: %q", got)
	}
	if cur, _ := e.s.Current(ctx); cur != before {
		t.Fatalf("AI save created a revision")
	}
	pending, err := e.s.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].File != "runtime.toml" || !strings.Contains(pending[0].Diff, "+workers = 6") {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	// Owner rejects one, accepts another.
	p2 := e.save(AI, "runtime.toml", rt(7))
	if err := e.s.Reject(ctx, p2.ProposalID); err != nil {
		t.Fatal(err)
	}
	acc, err := e.s.Accept(ctx, p.ProposalID)
	if err != nil || !acc.Applied || acc.Rev <= before {
		t.Fatalf("accept = %+v, %v", acc, err)
	}
	if got, _ := e.file("runtime.toml"); got != rt(6) {
		t.Errorf("after accept: %q", got)
	}
	if _, err := e.s.Accept(ctx, p.ProposalID); !errors.Is(err, ErrNoSuchProposal) {
		t.Errorf("double accept: %v", err)
	}
	if pending, _ := e.s.Pending(ctx); len(pending) != 0 {
		t.Errorf("still pending: %+v", pending)
	}
}

func TestConfigVerifyDetectsOutOfBandEdit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.s.Verify(ctx); err == nil || !strings.Contains(err.Error(), "parallel.workers") {
		t.Fatalf("empty config must be refused for its missing owner keys: %v", err)
	}
	e.save(Owner, "roles.toml", rolesBody(""))
	e.save(Owner, "runtime.toml", rt(2))
	if err := e.s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(e.home, schema.Dir, "runtime.toml")
	if err := os.WriteFile(p, []byte(rt(9)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.s.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "runtime.toml") {
		t.Fatalf("Verify = %v", err)
	}
	if err := os.WriteFile(p, []byte("nonsense = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Verify(ctx); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("invalid config: %v", err)
	}
}

func TestRunConfigSnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.save(Owner, "runtime.toml", rt(5))
	e.save(Owner, "roles.toml", rolesBody("effort = \"high\"\n"))
	r := e.save(Owner, "projects/demo.toml", projBody)

	runDir := filepath.Join(t.TempDir(), "runs", "run-1")
	rev, err := e.s.Snapshot(ctx, runDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveSnapshot(runDir) })
	if rev != r.Rev {
		t.Errorf("snapshot rev = %d, want %d", rev, r.Rev)
	}
	if got, err := ReadSnapshotRevision(runDir); err != nil || got != rev {
		t.Errorf("recorded rev = %d, %v", got, err)
	}

	// Content is a copy, and every file and directory is read-only.
	for _, rel := range []string{"runtime.toml", "roles.toml", "projects/demo.toml", RevisionFile} {
		p := filepath.Join(runDir, schema.Dir, rel)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o222 != 0 {
			t.Errorf("%s is writable: %v", rel, fi.Mode())
		}
		if os.Geteuid() != 0 {
			if err := os.WriteFile(p, []byte("x"), 0o600); err == nil {
				t.Errorf("%s: write succeeded", rel)
			}
		}
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(runDir, schema.Dir, "new.toml"), nil, 0o600); err == nil {
			t.Error("created a file in the snapshot directory")
		}
		if err := os.WriteFile(filepath.Join(runDir, schema.Dir, "projects", "x.toml"), nil, 0o600); err == nil {
			t.Error("created a file in the snapshot projects directory")
		}
	}

	// The run reads its own copy and is unaffected by later saves.
	e.save(Owner, "runtime.toml", rt(1))
	snap, err := schema.Load(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Runtime.Parallel.Workers != 5 || snap.Roles["coder"].Effort != "high" || snap.Projects["demo"].RepoPath != "/work/demo" {
		t.Errorf("snapshot config = %+v", snap.Runtime.Parallel)
	}
	live, _ := schema.Load(e.home)
	if live.Runtime.Parallel.Workers != 1 {
		t.Error("live config not updated")
	}

	// A second snapshot into the same run directory is refused.
	if _, err := e.s.Snapshot(ctx, runDir); err == nil {
		t.Error("overwrote an existing snapshot")
	}
	// An invalid live config produces no snapshot.
	if err := os.WriteFile(filepath.Join(e.home, schema.Dir, "runtime.toml"), []byte("bad = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "run-2")
	if _, err := e.s.Snapshot(ctx, bad); err == nil {
		t.Error("snapshot of invalid config")
	}
	if _, err := os.Stat(bad); err == nil {
		t.Error("partial snapshot left behind")
	}
}
