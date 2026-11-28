package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The adapter declares the real binary and the real install directory, not
// the directory of the symlink that points at it (D-080).
func TestInvocationDeclaresRealInstallPaths(t *testing.T) {
	d, err := os.MkdirTemp("/tmp", "fmc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	root, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(root, "share/claude/versions")
	if err := os.MkdirAll(install, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(install, "2.1.0")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "claude")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		binary string
		want   []string
	}{
		"symlinked binary": {link, []string{real, install}},
		"bare name":        {"claude", nil},
		"missing binary":   {filepath.Join(root, "bin", "nope"), nil},
	} {
		a, err := New(testLease(t), Config{Binary: tc.binary, ReadFile: files(promptFS())})
		if err != nil {
			t.Fatal(err)
		}
		inv, err := a.BuildInvocation(coderSpec())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(inv.ReadPaths, tc.want) {
			t.Errorf("%s: ReadPaths = %v, want %v", name, inv.ReadPaths, tc.want)
		}
	}
}
