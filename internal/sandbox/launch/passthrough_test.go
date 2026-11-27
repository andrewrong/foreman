package launch

import (
	"context"
	"strings"
	"testing"
)

func TestLaunchPassThroughRunsArgvDirectlyWithPolicyEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "leak")
	p := newPolicy(t)
	fr := &fakeRunner{}
	_, err := Launcher{PassThrough: true, Runner: fr}.Run(context.Background(), p, []string{"/bin/model", "-x"},
		IO{ExtraEnv: map[string]string{"B_VAR": "2", "A_VAR": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	s := fr.got[0]
	if s.Path != "/bin/model" || strings.Join(s.Args, " ") != "-x" {
		t.Errorf("not run directly: %q %q", s.Path, s.Args)
	}
	if s.Dir != p.SessionRepo() {
		t.Errorf("dir %q", s.Dir)
	}
	env := s.Env
	n := len(p.Env())
	if strings.Join(env[:n], "\n") != strings.Join(p.Env(), "\n") || env[n] != "A_VAR=1" || env[n+1] != "B_VAR=2" || len(env) != n+2 {
		t.Errorf("env = %q", env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "leak") {
			t.Errorf("leaked %q", kv)
		}
	}
	if (Launcher{PassThrough: true}).Sandboxed() || !(Launcher{SrtPath: "srt"}).Sandboxed() {
		t.Error("Sandboxed reports the wrong thing")
	}
}

func TestLaunchRefusesEnvAdditionsOverridingPolicy(t *testing.T) {
	p := newPolicy(t)
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "MODEL_TOKEN", "", "A=B"} {
		_, err := Launcher{SrtPath: "srt", Runner: &fakeRunner{}}.Start(context.Background(), p, []string{"x"},
			IO{ExtraEnv: map[string]string{name: "v"}})
		if err == nil {
			t.Errorf("addition %q accepted", name)
		}
	}
}
