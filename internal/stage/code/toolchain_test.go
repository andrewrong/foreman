package code

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

type fakeToolchain struct {
	tc  launch.Toolchain
	err error
	dir string // the directory it was asked about
}

func (f *fakeToolchain) Toolchain(_ context.Context, dir string) (launch.Toolchain, error) {
	f.dir = dir
	return f.tc, f.err
}

// D-085: the declared tool directories lead the session PATH and are
// readable; the system PATH follows.
func TestSessionToolchainOnPathAndAllowRead(t *testing.T) {
	w := newWorld(t, "scripted")
	install := filepath.Join(w.root, "tools", "go", "1.2.3")
	bin := filepath.Join(install, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(install)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeToolchain{tc: launch.Toolchain{Path: []string{filepath.Join(real, "bin")}, Read: []string{real}}}
	w.env.Toolchain = fake
	prep, err := w.env.Prepare(context.Background(), w.request())
	if err != nil {
		t.Fatal(err)
	}
	if fake.dir != w.repo {
		t.Errorf("toolchain asked about %s, want the session repo %s", fake.dir, w.repo)
	}
	var path string
	for _, kv := range prep.Policy.Env() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if !strings.HasPrefix(path, filepath.Join(real, "bin")+":") || !strings.Contains(path, "/usr/bin") {
		t.Errorf("session PATH = %q, want the tool dir first and the system PATH after", path)
	}
	if !slices.Contains(prep.Policy.AllowReadPaths(), real) {
		t.Errorf("allowRead %v lacks the install dir %s", prep.Policy.AllowReadPaths(), real)
	}
}

// A declared tool that is not installed refuses the launch: nothing starts.
func TestSessionToolchainMissingToolRefusesLaunch(t *testing.T) {
	w := newWorld(t, "scripted")
	w.env.Toolchain = &fakeToolchain{err: errors.New("toolchain: node@20.1.1 is declared in mise.toml but not installed; run `mise install`")}
	_, err := Run(context.Background(), w.env, w.request())
	if err == nil || !strings.Contains(err.Error(), "node@20.1.1") {
		t.Fatalf("err = %v, want the missing tool named", err)
	}
	if _, serr := os.Stat(filepath.Join(w.run, "stdout.log")); !os.IsNotExist(serr) {
		t.Errorf("a session was started: %v", serr)
	}
}
