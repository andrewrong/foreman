package recover

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewrong/foreman/internal/resource/gate"
	"github.com/andrewrong/foreman/internal/resource/ledger"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// MaxHomeLen keeps "<home>/runs/<run id>" within the 70 bytes srt's unix
// socket path allows (access.yaml layout): 70 - len("/runs/") - a run id of up
// to 9 bytes.
const MaxHomeLen = 55

// Check is one startup line (D-047: one log line per step; `foreman status`
// can print them).
type Check struct {
	Name   string
	OK     bool
	Detail string
}

func (c Check) String() string {
	state := "ok"
	if !c.OK {
		state = "FAIL"
	}
	return fmt.Sprintf("%s: %s: %s", c.Name, state, c.Detail)
}

// layoutDirs are created under FOREMAN_HOME when missing (D-068 layout).
func layoutDirs(project string) []string {
	return []string{"db", "runs", "githome", "cache", filepath.Join("cache", project),
		"projects", filepath.Join("projects", project)}
}

// Preflight checks and prepares FOREMAN_HOME before any database is opened: the
// path rules and the directory layout. A failing check returns an error naming
// it; the Checks list is returned either way.
func Preflight(home, project string) ([]Check, error) {
	var out []Check
	fail := func(name, format string, a ...any) ([]Check, error) {
		c := Check{Name: name, Detail: fmt.Sprintf(format, a...)}
		out = append(out, c)
		return out, fmt.Errorf("recover: startup check failed: %s", c)
	}
	if !filepath.IsAbs(home) || len(home) > MaxHomeLen {
		return fail("home-path", "FOREMAN_HOME must be absolute and at most %d bytes (srt socket path limit), got %d", MaxHomeLen, len(home))
	}
	if project == "" || strings.ContainsAny(project, "/\\") || project == "." || project == ".." {
		return fail("project-name", "invalid project name %q", project)
	}
	out = append(out, Check{Name: "home-path", OK: true, Detail: fmt.Sprintf("%d of %d bytes", len(home), MaxHomeLen)})
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fail("layout", "%v", err)
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o002 != 0 {
		return fail("layout", "%s must be a directory that is not world-writable", home)
	}
	for _, d := range layoutDirs(project) {
		p := filepath.Join(home, d)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return fail("layout", "%s exists and is not a directory", p)
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fail("layout", "%v", err)
		}
	}
	return append(out, Check{Name: "layout", OK: true, Detail: strings.Join(layoutDirs(project), " ")}), nil
}

// Deps is what Startup works on. The directory lock (D-047 step 1) is held by
// Stores already: sqlite.Open fails with ErrLocked otherwise.
type Deps struct {
	Stores  *sqlite.Stores
	Gate    *gate.Gate
	Ledger  *ledger.Ledger
	Project string
	Home    string
	// Procs kills the processes of Runs marked killing.
	Procs Procs
}

// Report is what Startup found and rebuilt.
type Report struct {
	Checks []Check
	// Leases are the reservations re-adopted from Runs that were pending or
	// running at the crash, by step id. sched takes them over.
	Leases map[string]*gate.Lease
}

// Startup runs the checks and rebuilds that can be done once the stores are
// open (D-047 steps 1-3, RT-5): the lock, PRAGMA integrity_check and schema
// versions, orphan reclaim of Runs marked killing, and the ledger rebuild. The
// config revision check (step 4) needs the config module and is not here.
func Startup(ctx context.Context, d Deps) (*Report, error) {
	rep := &Report{Leases: map[string]*gate.Lease{}}
	add := func(c Check) { rep.Checks = append(rep.Checks, c) }
	add(Check{Name: "lock", OK: true, Detail: sqlite.LockFile + " held"})
	for _, e := range []struct {
		name string
		db   *sqlite.DB
	}{{"global", d.Stores.Global}, {"work", d.Stores.Work}, {"ci", d.Stores.CI}} {
		if err := e.db.IntegrityCheck(ctx); err != nil {
			add(Check{Name: "integrity-" + e.name, Detail: err.Error()})
			return rep, err
		}
		v, err := e.db.SchemaVersion(ctx)
		if err != nil {
			return rep, err
		}
		add(Check{Name: "integrity-" + e.name, OK: true, Detail: fmt.Sprintf("integrity_check ok, schema version %d", v)})
	}
	killed, err := reclaimOrphans(ctx, d)
	if err != nil {
		return rep, err
	}
	add(Check{Name: "orphans", OK: true, Detail: fmt.Sprintf("%d killing run(s) finished", killed)})
	n, err := rebuildLedger(ctx, d, rep)
	if err != nil {
		return rep, err
	}
	add(Check{Name: "ledger", OK: true, Detail: fmt.Sprintf("%d reservation(s) rebuilt from Runs", n)})
	swept, err := SweepRunDirs(ctx, d)
	if err != nil {
		return rep, err
	}
	add(Check{Name: "run-dirs", OK: true, Detail: fmt.Sprintf("%d run directory(ies) of ended Runs removed", swept)})
	return rep, nil
}

// reclaimOrphans finishes the kill_run of Runs a crash interrupted: status
// killing means foremand decided to stop the session but did not finish.
func reclaimOrphans(ctx context.Context, d Deps) (int, error) {
	var dirs []string
	var ids []string
	err := d.Stores.Work.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, dir FROM runs WHERE status = 'killing'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, dir string
			if err := rows.Scan(&id, &dir); err != nil {
				return err
			}
			ids, dirs = append(ids, id), append(dirs, dir)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	for i, dir := range dirs {
		if _, err := d.Procs.KillRun(dir); err != nil {
			return i, err
		}
		if err := d.Stores.Work.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'killed' WHERE id = ? AND status = 'killing'`, ids[i])
			return err
		}); err != nil {
			return i, err
		}
	}
	return len(dirs), nil
}

// rebuildLedger empties the ledger and re-adopts the reservation of every Run
// that was pending or running (RT-5). The adopted id is "run-<run id>", which
// cannot collide with the "lease-<n>" ids a fresh Gate hands out.
func rebuildLedger(ctx context.Context, d Deps, rep *Report) (int, error) {
	if err := d.Ledger.Rebuild(nil); err != nil {
		return 0, err
	}
	type held struct {
		run, step, req, role, stage string
		items                       []gate.Item
	}
	var all []held
	err := d.Stores.Work.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.id, COALESCE(r.step_id, ''), r.req_id, r.role, COALESCE(s.stage, ''), r.reserved
			FROM runs r LEFT JOIN steps s ON s.id = r.step_id
			WHERE r.status IN ('pending', 'running') AND r.reserved <> '[]' ORDER BY r.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h held
			var raw string
			if err := rows.Scan(&h.run, &h.step, &h.req, &h.role, &h.stage, &raw); err != nil {
				return err
			}
			var items []struct {
				Resource string `json:"resource"`
				Amount   int64  `json:"amount"`
			}
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				return fmt.Errorf("recover: run %s reserved: %w", h.run, err)
			}
			for _, it := range items {
				h.items = append(h.items, gate.Item{Resource: it.Resource, Amount: it.Amount})
			}
			all = append(all, h)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	for _, h := range all {
		lease, err := d.Gate.Adopt(ledger.Reservation{ID: "run-" + h.run, Items: toLedgerItems(h.items), Tag: d.Project},
			gate.Caller{Project: d.Project, RequirementID: h.req, StepID: h.step, Stage: h.stage, Role: h.role})
		if err != nil {
			return 0, err
		}
		rep.Leases[h.step] = lease
	}
	return len(all), nil
}

func toLedgerItems(in []gate.Item) []ledger.Item {
	out := make([]ledger.Item, len(in))
	for i, it := range in {
		out[i] = ledger.Item{Resource: it.Resource, Amount: it.Amount}
	}
	return out
}

// ReservedJSON is the runs.reserved encoding of a lease's items.
func ReservedJSON(items []gate.Item) string {
	type item struct {
		Resource string `json:"resource"`
		Amount   int64  `json:"amount"`
	}
	out := make([]item, len(items))
	for i, it := range items {
		out[i] = item{it.Resource, it.Amount}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}
