package integrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// memHalts is a HaltStore that keeps the flag in a variable that outlives the
// Dir, standing in for global.db across a "restart" (the sqlite-backed store
// is exercised end to end in cmd/foreman).
type memHalts struct {
	info  HaltInfo
	set   bool
	saves int
}

func (m *memHalts) LoadHalt(context.Context) (HaltInfo, bool, error) { return m.info, m.set, nil }
func (m *memHalts) SaveHalt(_ context.Context, h HaltInfo) error {
	m.info, m.set = h, true
	m.saves++
	return nil
}
func (m *memHalts) ClearHalt(context.Context) error { m.set = false; return nil }

func TestOpenReusesAnInitialisedDirectory(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	d, err := Open(ctx, w.r, w.mirror, w.integ.Path, integIdent())
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != w.integ.Path || d.Default != "main" {
		t.Errorf("Open = %+v", d)
	}
	// It works as the original did: a requirement branch is pushed from it.
	readyToPush(t, w)
	if err := d.Push(ctx, "r1"); err != nil {
		t.Fatalf("push through the reopened directory: %v", err)
	}
}

func TestOpenRefusesAnythingInitDidNotMake(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	if _, err := Open(ctx, w.r, w.mirror, filepath.Join(t.TempDir(), "absent"), integIdent()); err == nil {
		t.Error("Open accepted a missing directory")
	}
	empty := t.TempDir()
	if _, err := Open(ctx, w.r, w.mirror, empty, integIdent()); err == nil {
		t.Error("Open accepted a directory that is not a git checkout")
	}
	if err := os.WriteFile(filepath.Join(empty, ".git"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, w.r, w.mirror, empty, integIdent()); err == nil {
		t.Error("Open accepted a .git file")
	}
	// A checkout whose origin was re-pointed (push destination changed) is not ours.
	must(w.r.Run(ctx, w.integ.Path, "remote", "set-url", "origin", "/somewhere/else.git"))
	if _, err := Open(ctx, w.r, w.mirror, w.integ.Path, integIdent()); err == nil {
		t.Error("Open accepted a checkout whose origin is not the mirror's origin")
	}
}

func TestOpenRefusesWeakenedPushDefault(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	must(w.r.Run(ctx, w.integ.Path, "config", "push.default", "current"))
	if _, err := Open(ctx, w.r, w.mirror, w.integ.Path, integIdent()); err == nil {
		t.Error("Open accepted push.default other than nothing")
	}
}

func TestPushHaltIsPersistedAndReloaded(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "main")
	store := &memHalts{}
	mustErr(t, w.integ.UseHaltStore(ctx, store))
	readyToPush(t, w)
	// The remote moves its default branch during the push.
	hook := "#!/bin/sh\ngit update-ref refs/heads/main refs/heads/foreman/r1\n"
	mustErr(t, os.WriteFile(filepath.Join(w.remote, "hooks", "post-receive"), []byte(hook), 0o700))
	var moved *DefaultBranchMovedError
	if err := w.integ.Push(ctx, "r1"); !errors.As(err, &moved) {
		t.Fatalf("Push = %v, want DefaultBranchMovedError", err)
	}
	if !store.set || store.info.Branch != "main" || store.info.After != moved.After || store.saves != 1 {
		t.Fatalf("stored halt %+v (saves %d), want branch main, after %s", store.info, store.saves, moved.After)
	}

	d2, err := Open(ctx, w.r, w.mirror, w.integ.Path, integIdent())
	mustErr(t, err)
	if d2.Halted() {
		t.Fatal("halted before the store was attached")
	}
	mustErr(t, d2.UseHaltStore(ctx, store))
	if !d2.Halted() {
		t.Fatal("halt lost across reopen")
	}
	if err := d2.Push(ctx, "r1"); !errors.Is(err, ErrPushHalted) {
		t.Errorf("push on a halted reopened dir = %v, want ErrPushHalted", err)
	}
	mustErr(t, d2.ResumePushes(ctx))
	if d2.Halted() || store.set {
		t.Error("ResumePushes left the halt in force")
	}
}
