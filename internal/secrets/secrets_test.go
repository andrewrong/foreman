package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodToken = "0123456789abcdef" + "0123456789abcdef-SECRETVALUE"

func mkdir(t *testing.T, mode os.FileMode) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
	return d
}

func put(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecretFileCheck(t *testing.T) {
	dir := mkdir(t, 0o700)
	good := put(t, dir, "foreman.github.token", goodToken+"\n", 0o600)
	strict := put(t, dir, "foreman.strict", goodToken, 0o400)

	t.Run("accepts 0600 and stricter", func(t *testing.T) {
		for _, p := range []string{good, strict} {
			if err := CheckFile(p); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
		}
		s, err := Read(dir, "github.token", Token)
		if err != nil {
			t.Fatal(err)
		}
		if s.Reveal() != goodToken {
			t.Fatal("value not trimmed or not read")
		}
	})

	cases := []struct {
		name string
		mk   func(t *testing.T) string
		want error
	}{
		{"group readable", func(t *testing.T) string { return put(t, dir, "foreman.g", goodToken, 0o640) }, ErrMode},
		{"world readable", func(t *testing.T) string { return put(t, dir, "foreman.w", goodToken, 0o604) }, ErrMode},
		{"symlink", func(t *testing.T) string {
			l := filepath.Join(dir, "foreman.link")
			if err := os.Symlink(good, l); err != nil {
				t.Fatal(err)
			}
			return l
		}, ErrSymlink},
		{"directory", func(t *testing.T) string {
			d := filepath.Join(dir, "foreman.dir")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			return d
		}, ErrNotRegular},
		{"group writable parent", func(t *testing.T) string {
			d := mkdir(t, 0o770)
			return put(t, d, "foreman.x", goodToken, 0o600)
		}, ErrParentWrite},
		{"world writable parent", func(t *testing.T) string {
			d := mkdir(t, 0o702)
			return put(t, d, "foreman.x", goodToken, 0o600)
		}, ErrParentWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.mk(t)
			err := CheckFile(p)
			if !errors.Is(err, c.want) {
				t.Fatalf("CheckFile: got %v, want %v", err, c.want)
			}
			if _, err := ReadFile(p, Plain); !errors.Is(err, c.want) {
				t.Fatalf("ReadFile: got %v, want %v", err, c.want)
			}
		})
	}

	t.Run("owned by someone else", func(t *testing.T) {
		other := Checker{UID: func() int { return os.Geteuid() + 1 }}
		if err := other.CheckFile(good); !errors.Is(err, ErrOwner) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("content rules", func(t *testing.T) {
		short := put(t, dir, "foreman.short", "tooshort", 0o600)
		empty := put(t, dir, "foreman.empty", " \n", 0o600)
		if _, err := ReadFile(short, Token); !errors.Is(err, ErrTooShort) {
			t.Fatalf("short token: %v", err)
		}
		if _, err := ReadFile(short, Plain); err != nil {
			t.Fatalf("short plain: %v", err)
		}
		if _, err := ReadFile(empty, Plain); !errors.Is(err, ErrEmpty) {
			t.Fatalf("empty: %v", err)
		}
	})

	t.Run("errors never contain the value", func(t *testing.T) {
		p := put(t, dir, "foreman.leaky", goodToken, 0o644)
		_, err := ReadFile(p, Token)
		if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Fatalf("err = %v", err)
		}
		short := put(t, dir, "foreman.short2", "SECRETVALUE", 0o600)
		_, err = ReadFile(short, Token)
		if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("bad names", func(t *testing.T) {
		for _, n := range []string{"", "../x", "a/b", "..", "with space"} {
			if _, err := Read(dir, n, Plain); !errors.Is(err, ErrBadName) {
				t.Fatalf("%q: %v", n, err)
			}
		}
	})
}

func TestSecretsDirHygiene(t *testing.T) {
	t.Run("clean directory", func(t *testing.T) {
		d := mkdir(t, 0o700)
		put(t, d, "foreman.github.token", goodToken, 0o600)
		put(t, d, "foreman.telegram.token", goodToken, 0o600)
		put(t, d, "service.env", "A=B", 0o600) // not foreman's; never opened
		if err := CheckDir(d); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("directory mode", func(t *testing.T) {
		for _, m := range []os.FileMode{0o750, 0o755, 0o500} {
			if err := CheckDir(mkdir(t, m)); !errors.Is(err, ErrDirMode) {
				t.Fatalf("%04o: %v", m, err)
			}
		}
	})
	t.Run("backup files", func(t *testing.T) {
		d := mkdir(t, 0o700)
		put(t, d, "foreman.github.token.bak", goodToken, 0o600)
		put(t, d, "other.bak.1", "x", 0o600)
		err := CheckDir(d)
		if !errors.Is(err, ErrBackupFile) || !strings.Contains(err.Error(), "other.bak.1") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("second source", func(t *testing.T) {
		d := mkdir(t, 0o700)
		put(t, d, "foreman.github.token", goodToken, 0o600)
		put(t, d, "foreman.github.token.old", goodToken, 0o600)
		put(t, d, "foreman.GitHub.Token~", goodToken, 0o600)
		put(t, d, "foreman.telegram.token", goodToken, 0o600)
		err := CheckDir(d)
		if !errors.Is(err, ErrDuplicateKey) || strings.Contains(err.Error(), "telegram") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("symlinked directory and owner", func(t *testing.T) {
		d := mkdir(t, 0o700)
		l := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(d, l); err != nil {
			t.Fatal(err)
		}
		if err := CheckDir(l); !errors.Is(err, ErrSymlink) {
			t.Fatalf("got %v", err)
		}
		other := Checker{UID: func() int { return os.Geteuid() + 1 }}
		if err := other.CheckDir(d); !errors.Is(err, ErrOwner) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("hygiene does not read content", func(t *testing.T) {
		d := mkdir(t, 0o700)
		put(t, d, "foreman.unreadable", goodToken, 0o000)
		if err := CheckDir(d); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSecretIsRedactedEverywhere(t *testing.T) {
	s := New("hunter2-super-secret")
	var logbuf bytes.Buffer
	slog.New(slog.NewTextHandler(&logbuf, nil)).Info("x", "k", s)
	j, err := json.Marshal(map[string]any{"s": s, "p": &s})
	if err != nil {
		t.Fatal(err)
	}
	outs := []string{
		s.String(), fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x %d", s, s, s, s, s, s, s),
		fmt.Sprint(&s), fmt.Sprintf("%v", []Secret{s}), fmt.Sprintf("%+v", struct{ T Secret }{s}),
		string(j), logbuf.String(),
	}
	for _, o := range outs {
		if strings.Contains(o, "hunter2") || strings.Contains(o, "super-secret") || o == "" {
			t.Fatalf("leaked or empty output: %q", o)
		}
	}
	if s.Reveal() != "hunter2-super-secret" || s.Len() != 20 {
		t.Fatal("Reveal broken")
	}
}
