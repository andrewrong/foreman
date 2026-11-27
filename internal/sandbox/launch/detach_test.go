package launch

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	MaybeRunSession() // the test binary doubles as the session launcher
	if os.Getenv("FM_TEST_BLOCK") == "1" {
		// A session-like child: not an Apple platform binary, so `ps -E` can
		// show its environment (it cannot for /bin/cat and friends).
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startDetached(t *testing.T, argv []string) (*Handle, string, Detached) {
	t.Helper()
	p := newPolicy(t)
	if err := os.MkdirAll(p.SessionRepo(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.RunTmp(), 0o700); err != nil {
		t.Fatal(err)
	}
	d := Detached{RunDir: p.RunTmp(), StdoutPath: filepath.Join(p.RunTmp(), "stdout.log"), StderrPath: filepath.Join(p.RunTmp(), "stderr.log")}
	h, err := Launcher{PassThrough: true}.StartDetached(p, argv, d)
	if err != nil {
		t.Fatal(err)
	}
	return h, p.RunTmp(), d
}

func TestDetachedSessionRecordsPidBeforeExecAndEnds(t *testing.T) {
	h, runDir, _ := startDetached(t, []string{"/bin/sh", "-c", "echo hello; exit 3"})
	// StartDetached returned after the pipe handshake, so pid.json exists now.
	pf, ok, err := ReadPidFile(runDir)
	if err != nil || !ok || pf.Pid != h.Pid || pf.StartTime == "" {
		t.Fatalf("pid.json = %+v ok=%v err=%v, want pid %d", pf, ok, err, h.Pid)
	}
	<-h.Done()
	if h.Exit().Code != 3 {
		t.Errorf("exit = %+v, want code 3", h.Exit())
	}
	if pf.Alive() {
		t.Error("an exited session is reported alive")
	}
	out, _ := os.ReadFile(filepath.Join(runDir, "stdout.log"))
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("stdout = %q", out)
	}
}

func TestDetachedSessionLeadsItsOwnGroupAndKillRunStopsIt(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	h, runDir, _ := startDetached(t, []string{"/bin/sh", "-c", "read x < " + fifo})
	pf, _, _ := ReadPidFile(runDir)
	if !pf.Alive() {
		t.Fatal("session blocked on the fifo is not alive")
	}
	if pg, err := syscall.Getpgid(h.Pid); err != nil || pg != h.Pid {
		t.Errorf("pgid = %d (%v), want its own group %d (setsid)", pg, err, h.Pid)
	}
	if killed, err := KillRun(runDir); err != nil || len(killed) == 0 {
		t.Fatalf("KillRun = %v, %v", killed, err)
	}
	<-h.Done()
	if !h.Exit().Signaled {
		t.Errorf("exit = %+v, want killed by signal", h.Exit())
	}
	if pf.Alive() {
		t.Error("session alive after KillRun")
	}
}

func TestKillRunFindsProcessThatLeftTheGroupByTmpdir(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "fmk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	runDir := filepath.Join(root, "runs", "r1")
	// A sibling directory sharing the prefix must be left alone.
	sibling := filepath.Join(root, "runs", "r10")
	start := func(tmp string) (*exec.Cmd, func()) {
		cmd := exec.Command(os.Args[0])
		cmd.Env = []string{"TMPDIR=" + tmp, "FM_TEST_BLOCK=1"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // escapes the session's group
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, func() { in.Close() }
	}
	escaped, _ := start(filepath.Join(runDir, "sub"))
	other, release := start(sibling)
	if _, err := KillRun(runDir); err != nil { // no pid.json: only the TMPDIR scan can find it
		t.Fatal(err)
	}
	err = escaped.Wait()
	var ee *exec.ExitError
	if err == nil || !errorsAs(err, &ee) || ee.ExitCode() != -1 {
		t.Errorf("escaped process not killed by signal: %v", err)
	}
	release() // EOF: the unrelated process ends by itself, proving it was not killed
	if err := other.Wait(); err != nil {
		t.Errorf("process of the sibling run directory was killed: %v", err)
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	*target = ee
	return ok
}

func TestPidFileAliveRejectsRecycledPid(t *testing.T) {
	_, ok, _ := ProcInfo(os.Getpid())
	if !ok {
		t.Skip("ps cannot read this process")
	}
	own, _, _ := ProcInfo(os.Getpid())
	if !(PidFile{Pid: os.Getpid(), StartTime: own}).Alive() {
		t.Error("own process with the right start time is not alive")
	}
	if (PidFile{Pid: os.Getpid(), StartTime: "Thu Jan  1 00:00:00 1970"}).Alive() {
		t.Error("a pid with a different start time must count as recycled, not alive")
	}
}
