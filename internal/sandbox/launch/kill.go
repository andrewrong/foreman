package launch

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// KillRun terminates a Run's session (kill_run, ACR-28): first the process
// group recorded in pid.json (the launcher called setsid, so pgid == pid), then
// every leftover process whose TMPDIR points into the run directory, which
// catches children that escaped the group with their own setsid. It returns
// the pids it signalled; finding none means the Run is already gone.
func KillRun(runDir string) ([]int, error) {
	var killed []int
	if pf, ok, err := ReadPidFile(runDir); err != nil {
		return nil, err
	} else if ok && pf.Alive() {
		if syscall.Kill(-pf.Pid, syscall.SIGKILL) == nil {
			killed = append(killed, pf.Pid)
		}
	}
	left, err := ScanTmpdir(runDir)
	if err != nil {
		return killed, err
	}
	for _, pid := range left {
		_ = syscall.Kill(-pid, syscall.SIGKILL) // its own group, if it leads one
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			killed = append(killed, pid)
		}
	}
	return killed, nil
}

// ScanTmpdir lists the processes of this user whose environment sets TMPDIR to
// runDir or a path below it. It reads /proc/<pid>/environ where /proc exists
// and `ps -E` (environment appended to the command line) elsewhere. `ps -E`
// shows only the caller's own processes, which is all a session can be. The
// scanning process itself is excluded. The match is a whole TMPDIR=<path>
// word, so a sibling run directory sharing a prefix is not hit. Limits: a
// process that changed its own TMPDIR is not found, and on macOS a command
// line argument spelled TMPDIR=<run dir> would be a false positive.
func ScanTmpdir(runDir string) ([]int, error) {
	self := os.Getpid()
	var pids []int
	if ents, err := os.ReadDir("/proc"); err == nil {
		for _, e := range ents {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid == self {
				continue
			}
			env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
			if err != nil {
				continue
			}
			for _, kv := range strings.Split(string(env), "\x00") {
				if tmpdirInside(kv, runDir) {
					pids = append(pids, pid)
					break
				}
			}
		}
		return pids, nil
	}
	cmd := exec.Command(psPath(), "-axE", "-ww", "-o", "pid=,command=")
	cmd.Env = []string{"LC_ALL=C", "PATH=/bin:/usr/bin"}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		pidStr, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		pid, perr := strconv.Atoi(pidStr)
		if !ok || perr != nil || pid == self {
			continue
		}
		for _, word := range strings.Fields(rest) {
			if tmpdirInside(word, runDir) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}

func tmpdirInside(kv, runDir string) bool {
	v, ok := strings.CutPrefix(kv, "TMPDIR=")
	return ok && (v == runDir || strings.HasPrefix(v, runDir+"/"))
}
