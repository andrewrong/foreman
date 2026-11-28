package launch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/policy"
)

type fakeProc struct{}

func (fakeProc) Pid() int            { return 1 }
func (fakeProc) Kill() error         { return nil }
func (fakeProc) Wait() (Exit, error) { return Exit{Code: 7}, nil }

type fakeRunner struct{ got []Spec }

func (f *fakeRunner) Start(_ context.Context, s Spec) (Process, error) {
	f.got = append(f.got, s)
	return fakeProc{}, nil
}

func newPolicy(t *testing.T) policy.Policy {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "fml") // short: srt socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, d := range []string{"/repo", "/run", "/home"} {
		if err := os.MkdirAll(root+d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := policy.New(policy.Inputs{
		SessionRepo: root + "/repo",
		RunTmp:      root + "/run",
		ForemanHome: root + "/home",
		Path:        "/usr/bin",
		SessionGit:  policy.SessionGit{Name: "n", Email: "n@users.noreply.example", DefaultBranch: "main"},
		Families:    []policy.Family{policy.FamilyGo},
		Model:       &policy.ModelToken{Name: "MODEL_TOKEN", Value: "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLaunchBuildsSrtInvocation(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "leak")
	p := newPolicy(t)
	fr := &fakeRunner{}
	argv := []string{"echo", "a b; $(rm -rf /)", "--settings"}
	ex, err := Launcher{SrtPath: "/opt/srt", Runner: fr}.Run(context.Background(), p, argv, IO{})
	if err != nil || ex.Code != 7 {
		t.Fatalf("exit %v err %v", ex, err)
	}
	s := fr.got[0]
	if s.Path != "/opt/srt" {
		t.Errorf("path %q", s.Path)
	}
	want := append([]string{"--settings", p.SettingsPath(), "--"}, argv...)
	if strings.Join(s.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args %q", s.Args)
	}
	if s.Dir != p.SessionRepo() {
		t.Errorf("dir %q", s.Dir)
	}
	for _, kv := range s.Env {
		if strings.Contains(kv, "leak") {
			t.Errorf("leaked %q", kv)
		}
	}
	if strings.Join(s.Env, "\n") != strings.Join(p.Env(), "\n") {
		t.Error("env is not exactly the policy env")
	}
}

func TestLaunchWritesPrivateSettings(t *testing.T) {
	p := newPolicy(t)
	if _, err := (Launcher{SrtPath: "srt", Runner: &fakeRunner{}}).Start(context.Background(), p, []string{"true"}, IO{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p.SettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p.SettingsPath())
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Home()); err != nil {
		t.Errorf("run home not created: %v", err)
	}
}

func TestLaunchRejectsBadInput(t *testing.T) {
	p := newPolicy(t)
	if _, err := (Launcher{Runner: &fakeRunner{}}).Start(context.Background(), p, []string{"x"}, IO{}); err == nil {
		t.Error("empty srt path accepted")
	}
	if _, err := (Launcher{SrtPath: "srt", Runner: &fakeRunner{}}).Start(context.Background(), p, nil, IO{}); err == nil {
		t.Error("empty argv accepted")
	}
}

func TestExecRunnerDoesNotInheritEnv(t *testing.T) {
	t.Setenv("FOREMAN_LEAK_PROBE_TOKEN", "leak")
	var out strings.Builder
	p, err := ExecRunner{}.Start(context.Background(), Spec{Path: "/usr/bin/env", Env: []string{"A=1"}, Stdout: &out})
	if err != nil {
		t.Skip("no /usr/bin/env")
	}
	if ex, err := p.Wait(); err != nil || ex.Code != 0 {
		t.Fatalf("%v %v", ex, err)
	}
	if strings.TrimSpace(out.String()) != "A=1" {
		t.Errorf("env was %q", out.String())
	}
}

// D-082: the launcher writes the generated gitconfig into the run HOME.
func TestLaunchWritesSessionGitconfig(t *testing.T) {
	p := newPolicy(t)
	if _, err := (Launcher{SrtPath: "srt", Runner: &fakeRunner{}}).Start(context.Background(), p, []string{"true"}, IO{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.GitConfigPath())
	if err != nil || string(b) != string(p.GitConfig()) {
		t.Errorf("gitconfig not written as generated: %v", err)
	}
}

// Probe P4: srt is a node script and a mise shim on PATH breaks it, so the
// configured node dir goes first in the PATH srt starts with; a pass-through
// launcher leaves PATH alone.
func TestLaunchPutsNodeDirFirstInSrtPath(t *testing.T) {
	p := newPolicy(t)
	fr := &fakeRunner{}
	if _, err := (Launcher{SrtPath: "srt", NodeDir: "/opt/node/bin", Runner: fr}).Start(context.Background(), p, []string{"true"}, IO{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Launcher{PassThrough: true, NodeDir: "/opt/node/bin", Runner: fr}).Start(context.Background(), p, []string{"true"}, IO{}); err != nil {
		t.Fatal(err)
	}
	pathOf := func(env []string) string {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				return v
			}
		}
		return ""
	}
	if got := pathOf(fr.got[0].Env); !strings.HasPrefix(got, "/opt/node/bin:") {
		t.Errorf("srt PATH = %q", got)
	}
	if got := pathOf(fr.got[1].Env); strings.Contains(got, "/opt/node/bin") {
		t.Errorf("pass-through PATH = %q", got)
	}
}

func TestLauncherResolveRefusesMissingPaths(t *testing.T) {
	real, err := os.MkdirTemp("/tmp", "fml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(real) })
	link := real + "-link"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	got, err := Launcher{SrtPath: link, NodeDir: link}.Resolve()
	if err != nil || got.SrtPath == link || got.NodeDir == link {
		t.Errorf("not resolved: %+v %v", got, err)
	}
	if _, err := (Launcher{SrtPath: link, NodeDir: real + "/missing"}).Resolve(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing node dir: %v", err)
	}
	if _, err := (Launcher{PassThrough: true}).Resolve(); err != nil {
		t.Errorf("pass-through: %v", err)
	}
}
