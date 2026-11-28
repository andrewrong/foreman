package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/workspace/integrate"
)

// TestPushHaltSurvivesRestart: a push that moves the default branch halts all
// pushes of the project (D-050). The flag lives in global.db, so a restarted
// daemon refuses to push until the owner clears it.
func TestPushHaltSurvivesRestart(t *testing.T) {
	t.Parallel()
	e := newCrashEnv(t, false)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	// A hostile remote: whatever is pushed, main moves along with it.
	hook := "#!/bin/sh\n" + gitBin + " update-ref refs/heads/main \"$(" + gitBin + " rev-parse refs/heads/foreman/" + reqID + ")\"\n"
	if err := os.WriteFile(filepath.Join(e.f.remote, "hooks", "post-receive"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}

	app1, _ := e.open()
	seed(t, app1, e.clk)
	var moved *integrate.DefaultBranchMovedError
	for i := 0; i < 60 && moved == nil; i++ {
		e.clk.Advance(time.Second)
		if _, err := app1.Sched.Tick(context.Background()); err != nil && !errors.As(err, &moved) {
			t.Fatal(err)
		}
		app1.Sched.WaitSessions()
	}
	if moved == nil {
		t.Fatal("the push never moved the default branch")
	}
	if !app1.Integ.Halted() {
		t.Fatal("pushes not halted after the default branch moved")
	}
	var branch, after string
	err = app1.Stores.Global.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT branch, after_sha FROM push_halt WHERE project = 'p'`).Scan(&branch, &after)
	})
	if err != nil || branch != "main" || after != moved.After {
		t.Fatalf("persisted halt: branch %q after %q err %v, want main %q", branch, after, err, moved.After)
	}
	e.crash(app1)

	app2, _ := e.open()
	if !app2.Integ.Halted() {
		t.Fatal("the halt did not survive the restart")
	}
	tip := e.f.remoteSHA("refs/heads/foreman/" + reqID)
	e.clk.Advance(time.Second)
	if _, err := app2.Sched.Tick(context.Background()); !errors.Is(err, integrate.ErrPushHalted) {
		t.Fatalf("Tick after restart = %v, want ErrPushHalted", err)
	}
	if got := e.f.remoteSHA("refs/heads/foreman/" + reqID); got != tip {
		t.Errorf("a halted daemon pushed: %s -> %s", tip, got)
	}
	if err := app2.Integ.ResumePushes(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.crash(app2)

	app3, _ := e.open()
	if app3.Integ.Halted() {
		t.Error("halt still in force after the owner cleared it")
	}
}
