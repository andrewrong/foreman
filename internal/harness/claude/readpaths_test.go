package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// D-084: claude is found on foremand's own PATH, resolved through every
// symlink at each launch, launched by that path, and its directory is
// declared as a read path. Retargeting the link between two launches makes
// the second launch use the new target.
func TestHarnessBinaryFromPath(t *testing.T) {
	root := shortDir(t)
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	v1, v2 := mk("share/claude/versions/1.0.0"), mk("share/other/2.0.0")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(root, "mid")
	if err := os.Symlink(v1, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", Command)
	if err := os.Symlink(mid, link); err != nil { // chain: bin/claude -> mid -> v1
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+filepath.Join(root, "absent"))

	a, err := New(testLease(t), Config{ReadFile: files(promptFS())})
	if err != nil {
		t.Fatal(err)
	}
	launch := func(want string) {
		t.Helper()
		inv, err := a.BuildInvocation(coderSpec())
		if err != nil {
			t.Fatal(err)
		}
		if inv.Argv[0] != want {
			t.Errorf("launched %q, want %q", inv.Argv[0], want)
		}
		if wantRP := []string{want, filepath.Dir(want)}; !reflect.DeepEqual(inv.ReadPaths, wantRP) {
			t.Errorf("ReadPaths = %v, want %v", inv.ReadPaths, wantRP)
		}
	}
	launch(v1)
	if err := os.Remove(mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v2, mid); err != nil {
		t.Fatal(err)
	}
	launch(v2)

	// Not on PATH: the launch and the startup lookup both refuse.
	t.Setenv("PATH", filepath.Join(root, "empty"))
	if _, err := a.BuildInvocation(coderSpec()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("BuildInvocation without claude on PATH: %v", err)
	}
	if _, err := Locate(); err == nil {
		t.Error("Locate succeeded without claude on PATH")
	}
}
