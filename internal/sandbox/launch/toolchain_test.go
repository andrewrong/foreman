package launch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeMise writes a stand-in "mise" script: env --json prints envJSON and
// writes warn to stderr, ls --current --json prints lsJSON. The script also
// records the environment it was started with in <dir>/seen.
func fakeMise(t *testing.T, envJSON, warn, lsJSON string) (bin, seen string) {
	t.Helper()
	dir := t.TempDir()
	seen = filepath.Join(dir, "seen")
	script := "#!/bin/sh\n" +
		"env > " + seen + "\n" +
		"case \"$1\" in\n" +
		"env) cat <<'EOF'\n" + envJSON + "\nEOF\n" +
		"echo '" + warn + "' >&2 ;;\n" +
		"ls) cat <<'EOF'\n" + lsJSON + "\nEOF\n" +
		";; esac\n"
	bin = filepath.Join(dir, "mise")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, seen
}

func realDir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMiseToolchainDirs(t *testing.T) {
	root := realDir(t, t.TempDir())
	install := realDir(t, filepath.Join(root, "installs/go/1.2.3"))
	bin := realDir(t, filepath.Join(install, "bin"))
	shims := realDir(t, filepath.Join(root, "shims"))
	link := filepath.Join(root, "latest")
	if err := os.Symlink(install, link); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "mise.toml"), []byte("[tools]\ngo = \"1.2.3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// mise prints its additions (a symlinked tool dir, a shims dir) first and
	// the inherited PATH after them.
	mbin, seen := fakeMise(t,
		`{"PATH":"`+filepath.Join(link, "bin")+":"+shims+`:/usr/bin:/bin:MISEDIR"}`, "",
		`{"go":[{"version":"1.2.3","install_path":"`+link+`","installed":true}]}`)
	// The fake cannot know its own directory: substitute it in the output.
	data, _ := os.ReadFile(mbin)
	if err := os.WriteFile(mbin, []byte(strings.ReplaceAll(string(data), "MISEDIR", filepath.Dir(mbin))), 0o700); err != nil {
		t.Fatal(err)
	}
	m := Mise{Bin: mbin, EmptyConfig: filepath.Join(t.TempDir(), "mise", "config.toml"),
		Environ: func() []string {
			return []string{"HOME=/h", "PATH=/owner/bin:/opt/homebrew/bin", "MISE_DATA_DIR=/d", "MISE_GLOBAL_CONFIG_FILE=/owner/cfg"}
		}}
	tc, err := m.Toolchain(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{bin}; !reflect.DeepEqual(tc.Path, want) {
		t.Errorf("Path = %v, want %v (real path, no shims, no inherited entries)", tc.Path, want)
	}
	if len(tc.Read) == 0 || tc.Read[0] != install {
		t.Errorf("Read = %v, want the install dir %s first", tc.Read, install)
	}
	if fi, err := os.Stat(m.EmptyConfig); err != nil || fi.Size() != 0 {
		t.Errorf("empty global config: %v", err)
	}
	env, _ := os.ReadFile(seen)
	for _, want := range []string{"MISE_GLOBAL_CONFIG_FILE=" + m.EmptyConfig, "MISE_TRUSTED_CONFIG_PATHS=" + repo, "HOME=/h", "MISE_DATA_DIR=/d"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("mise environment lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "/owner/") || strings.Contains(string(env), "homebrew") {
		t.Errorf("owner PATH or global config reached mise:\n%s", env)
	}
}

func TestMiseRefusesMissingTool(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "mise.toml"), []byte("[tools]\nnode = \"9.9.9\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "c.toml")
	// 1: the warning on stderr.
	bin, _ := fakeMise(t, `{"PATH":"/usr/bin"}`, "mise WARN  missing: node@9.9.9", `{}`)
	_, err := Mise{Bin: bin, EmptyConfig: empty}.Toolchain(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "node@9.9.9") || !strings.Contains(err.Error(), "mise install") {
		t.Errorf("stderr warning: err = %v", err)
	}
	// 2: no warning, but the declared tool is not installed.
	bin, _ = fakeMise(t, `{"PATH":"/usr/bin"}`, "", `{"node":[{"version":"9.9.9","install_path":"/nonexistent","installed":false}]}`)
	_, err = Mise{Bin: bin, EmptyConfig: empty}.Toolchain(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "node@9.9.9") {
		t.Errorf("not installed: err = %v", err)
	}
	// 3: a PATH entry that does not resolve.
	bin, _ = fakeMise(t, `{"PATH":"/nonexistent/node/bin:/usr/bin"}`, "", `{}`)
	_, err = Mise{Bin: bin, EmptyConfig: empty}.Toolchain(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/node/bin") {
		t.Errorf("unresolvable dir: err = %v", err)
	}
}

func TestMiseWithoutMiseTomlAddsNothing(t *testing.T) {
	// No mise.toml: mise is not even needed.
	tc, err := Mise{Bin: "/nonexistent/mise", EmptyConfig: "/nonexistent/c.toml"}.Toolchain(context.Background(), t.TempDir())
	if err != nil || len(tc.Path) != 0 || len(tc.Read) != 0 {
		t.Errorf("toolchain = %+v, %v; want empty", tc, err)
	}
}

func TestMiseNotOnPathRefusesWhenDeclared(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "mise.toml"), []byte("[tools]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	_, err := Mise{EmptyConfig: filepath.Join(t.TempDir(), "c.toml")}.Toolchain(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "mise is not on foremand's PATH") {
		t.Errorf("err = %v", err)
	}
}
