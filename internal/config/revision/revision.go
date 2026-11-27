// Package revision records every save of the owner's config files as a
// revision in global.db (config_revisions), restores any revision, keeps AI
// changes as proposals that are never applied by themselves (config.yaml,
// D-022), and copies the effective config into a run directory (C-13).
package revision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
	"github.com/andrewrong/foreman/internal/config/schema"
	"github.com/andrewrong/foreman/internal/store/sqlite"
)

// Actor is who made a change.
type Actor string

const (
	Owner Actor = "owner" // applied immediately
	AI    Actor = "ai"    // improver / rescuer: proposal only
)

// Errors.
var (
	ErrNoChange       = errors.New("revision: content identical to the current file")
	ErrUnknownActor   = errors.New("revision: unknown actor")
	ErrNoSuchRev      = errors.New("revision: no such revision")
	ErrNoSuchProposal = errors.New("revision: no such pending proposal")
)

// Saved is the outcome of Save, Rollback or Accept.
type Saved struct {
	Rev        int64 // new revision; 0 for a proposal
	ProposalID int64 // set when actor=ai
	Applied    bool
}

// Proposal is a pending AI change.
type Proposal struct {
	ID    int64
	File  string
	Diff  string
	Actor string
	Time  time.Time
}

// Store manages the config directory under home and its revisions.
type Store struct {
	db   *sqlite.DB
	home string
	clk  clock.Clock
	mu   sync.Mutex
}

// New returns a Store for FOREMAN_HOME home, recording revisions in db
// (global.db).
func New(db *sqlite.DB, home string, clk clock.Clock) *Store {
	return &Store{db: db, home: home, clk: clk}
}

func (s *Store) root() string { return filepath.Join(s.home, schema.Dir) }

func (s *Store) now() string { return s.clk.Now().UTC().Format(time.RFC3339Nano) }

// scanDisk returns the recognised config files (relative slash path ->
// content). Other files in the directory are ignored.
func scanDisk(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if k, _ := schema.Classify(rel); k == schema.KindUnknown || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	return out, err
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func insertRev(tx *sql.Tx, diff string, actor Actor, at string, files map[string]string) (int64, error) {
	snap, err := json.Marshal(files)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO config_revisions(diff, actor, time, snapshot) VALUES (?,?,?,?)`,
		diff, string(actor), at, string(snap))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Save validates content as config file rel (e.g. "roles.toml") and, for
// actor=owner, writes it and records a revision. For actor=ai it records a
// proposal and leaves the config untouched. Invalid content is rejected and
// nothing is written.
func (s *Store) Save(ctx context.Context, actor Actor, rel string, content []byte) (Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := schema.ValidateFile(rel, content); err != nil {
		return Saved{}, err
	}
	switch actor {
	case Owner:
		return s.apply(ctx, rel, content)
	case AI:
		before, err := scanDisk(s.root())
		if err != nil {
			return Saved{}, err
		}
		diff := diffFiles(before, withFile(before, rel, string(content)))
		var id int64
		err = s.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO config_proposals(file, content, diff, actor, time) VALUES (?,?,?,?,?)`,
				rel, string(content), diff, string(actor), s.now())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		})
		return Saved{ProposalID: id}, err
	}
	return Saved{}, fmt.Errorf("%w: %q", ErrUnknownActor, actor)
}

func withFile(m map[string]string, rel, content string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[rel] = content
	return out
}

// apply writes rel and inserts the revision in one transaction: if the file
// cannot be written the revision row is rolled back.
func (s *Store) apply(ctx context.Context, rel string, content []byte) (Saved, error) {
	before, err := scanDisk(s.root())
	if err != nil {
		return Saved{}, err
	}
	if cur, ok := before[rel]; ok && cur == string(content) {
		return Saved{}, fmt.Errorf("%w: %s", ErrNoChange, rel)
	}
	after := withFile(before, rel, string(content))
	var rev int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if rev, err = insertRev(tx, diffFiles(before, after), Owner, s.now(), after); err != nil {
			return err
		}
		return writeAtomic(filepath.Join(s.root(), filepath.FromSlash(rel)), content)
	})
	if err != nil {
		return Saved{}, err
	}
	return Saved{Rev: rev, Applied: true}, nil
}

// Rollback restores the config directory to the state saved in revision rev
// and records that as a new revision (history is never rewritten).
func (s *Store) Rollback(ctx context.Context, rev int64) (Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT snapshot FROM config_revisions WHERE rev = ?`, rev).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Saved{}, fmt.Errorf("%w: %d", ErrNoSuchRev, rev)
	}
	if err != nil {
		return Saved{}, err
	}
	var target map[string]string
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		return Saved{}, fmt.Errorf("revision: corrupt snapshot of revision %d: %w", rev, err)
	}
	for rel, body := range target {
		if err := schema.ValidateFile(rel, []byte(body)); err != nil {
			return Saved{}, fmt.Errorf("revision %d no longer valid under the current schema: %w", rev, err)
		}
	}
	before, err := scanDisk(s.root())
	if err != nil {
		return Saved{}, err
	}
	var newRev int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var err error
		diff := fmt.Sprintf("# rollback to revision %d\n%s", rev, diffFiles(before, target))
		if newRev, err = insertRev(tx, diff, Owner, s.now(), target); err != nil {
			return err
		}
		for rel, body := range target {
			if before[rel] == body {
				continue
			}
			if err := writeAtomic(filepath.Join(s.root(), filepath.FromSlash(rel)), []byte(body)); err != nil {
				return err
			}
		}
		for rel := range before {
			if _, keep := target[rel]; !keep {
				if err := os.Remove(filepath.Join(s.root(), filepath.FromSlash(rel))); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Saved{}, err
	}
	return Saved{Rev: newRev, Applied: true}, nil
}

// Current returns the latest revision number (0 if none).
func (s *Store) Current(ctx context.Context) (int64, error) {
	var rev sql.NullInt64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT MAX(rev) FROM config_revisions`).Scan(&rev)
	})
	return rev.Int64, err
}

// Verify is foremand's startup step 4 (D-047): the config on disk must equal
// the latest revision, and must load. Without any revision only validity is
// checked. A mismatch means the files were edited outside `foreman config`.
func (s *Store) Verify(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := schema.Load(s.home); err != nil {
		return err
	}
	var raw string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT snapshot FROM config_revisions ORDER BY rev DESC LIMIT 1`).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var want map[string]string
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		return fmt.Errorf("revision: corrupt latest snapshot: %w", err)
	}
	have, err := scanDisk(s.root())
	if err != nil {
		return err
	}
	if names := changedNames(want, have); len(names) > 0 {
		return fmt.Errorf("revision: config changed outside foreman config (%s); save it through foreman config", strings.Join(names, ", "))
	}
	return nil
}
