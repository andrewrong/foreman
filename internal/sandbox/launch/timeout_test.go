package launch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/andrewrong/foreman/internal/clock"
)

func mustDirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// D-091: the launcher itself ends a session at its time limit, with no
// scheduler tick involved; the handle says it was a timeout.
func TestDetachedSessionIsKilledAtTimeout(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	p := newPolicy(t)
	mustDirs(t, p.SessionRepo(), p.RunTmp())
	fake := clock.NewFake(time.Unix(1_000_000, 0))
	d := Detached{RunDir: p.RunTmp(), StdoutPath: filepath.Join(p.RunTmp(), "stdout.log"),
		StderrPath: filepath.Join(p.RunTmp(), "stderr.log"), Timeout: 10 * time.Minute, Clock: fake}
	h, err := Launcher{PassThrough: true}.StartDetached(p, []string{"/bin/sh", "-c", "read x < " + fifo}, d)
	if err != nil {
		t.Fatal(err)
	}
	fake.Advance(9 * time.Minute)
	select {
	case <-h.Done():
		t.Fatal("session ended before its time limit")
	default:
	}
	pf, _, _ := ReadPidFile(p.RunTmp())
	if !pf.Alive() {
		t.Fatal("session not alive before the time limit")
	}
	fake.Advance(2 * time.Minute)
	<-h.Done()
	if !h.Exit().Signaled || !h.TimedOut() {
		t.Errorf("exit %+v timedOut %v, want killed and reported as a timeout", h.Exit(), h.TimedOut())
	}
}

// D-091 on the synchronous path: cancelling the context kills the children of
// the process too. The child holds the write end of a pipe; reading EOF means
// every process of the group is gone.
func TestExecRunnerCancelKillsTheWholeGroup(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := ExecRunner{}.Start(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 300 & echo started; wait"},
		Env: []string{"PATH=/bin:/usr/bin"}, Stdout: pw})
	if err != nil {
		t.Fatal(err)
	}
	_ = pw.Close() // the child's copy is the only one left
	line := make([]byte, 8)
	if _, err := io.ReadFull(pr, line); err != nil || string(line) != "started\n" {
		t.Fatalf("child did not start: %q %v", line, err)
	}
	cancel()
	if ex, _ := proc.Wait(); !ex.Signaled {
		t.Errorf("exit %+v, want killed by signal", ex)
	}
	if rest, err := io.ReadAll(pr); err != nil || len(rest) != 0 {
		t.Errorf("read %q, %v after the kill", rest, err)
	}
}

func TestDetachedSessionEndingBeforeTimeoutIsNotATimeout(t *testing.T) {
	p := newPolicy(t)
	mustDirs(t, p.SessionRepo(), p.RunTmp())
	fake := clock.NewFake(time.Unix(1_000_000, 0))
	d := Detached{RunDir: p.RunTmp(), StdoutPath: filepath.Join(p.RunTmp(), "stdout.log"),
		StderrPath: filepath.Join(p.RunTmp(), "stderr.log"), Timeout: time.Minute, Clock: fake}
	h, err := Launcher{PassThrough: true}.StartDetached(p, []string{"/bin/sh", "-c", "exit 0"}, d)
	if err != nil {
		t.Fatal(err)
	}
	<-h.Done()
	fake.Advance(2 * time.Minute)
	if h.TimedOut() || h.Exit().Code != 0 {
		t.Errorf("exit %+v timedOut %v, want a clean exit", h.Exit(), h.TimedOut())
	}
}
