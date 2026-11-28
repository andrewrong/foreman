package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrewrong/foreman/internal/config/schema"
	wsgit "github.com/andrewrong/foreman/internal/workspace/git"
)

// shortHome is a FOREMAN_HOME with a short, symlink-free path (D-068).
func shortHome(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "fmd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	home, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func writeConfig(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(home, "config", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const rolesTOML = "[role.intake]\ntimeout = \"20m\"\n[role.planner]\ntimeout = \"20m\"\n[role.coder]\ntimeout = \"20m\"\n" +
	"[role.reviewer]\ntimeout = \"20m\"\n[role.rescuer]\ntimeout = \"20m\"\n[role.diagnoser]\ntimeout = \"20m\"\n[role.improver]\ntimeout = \"20m\"\n"

// Starting foremand with a runtime.toml that lacks owner keys exits non-zero
// and names every missing key, one per line, before the database or the
// network is touched (D-074): the home gets no db directory.
func TestDaemonRefusesMissingOwnerKeys(t *testing.T) {
	t.Parallel()
	home := shortHome(t)
	writeConfig(t, home, map[string]string{
		"runtime.toml":       "[limits]\ncheck_fail = 4\n",
		"roles.toml":         rolesTOML,
		"projects/demo.toml": "repo_path = \"/srv/x\"\n",
	})
	var out, errb bytes.Buffer
	if code := runDaemon(context.Background(), home, &out, &errb); code == 0 {
		t.Fatalf("daemon started without owner keys; stdout %q", out.String())
	}
	lines := strings.Split(strings.TrimSpace(errb.String()), "\n")
	for _, key := range []string{
		"runtime.toml: secrets_dir", "runtime.toml: parallel.workers", "runtime.toml: sandbox.srt_path",
		"runtime.toml: sandbox.node_dir", "runtime.toml: sandbox.read_paths", "runtime.toml: sandbox.deny_roots",
		"runtime.toml: session_git.name", "runtime.toml: session_git.email",
		"projects/demo.toml: github_repo", "projects/demo.toml: default_branch", "projects/demo.toml: l0",
	} {
		n := 0
		for _, l := range lines {
			if strings.Contains(l, key+":") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("key %q named on %d lines, want 1\n%s", key, n, errb.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "db")); err == nil {
		t.Error("the database directory exists: the daemon touched the store before refusing")
	}
}

// Every value Compose needs comes from the three file kinds.
func TestConfigFromFilesMapsEveryOwnerKey(t *testing.T) {
	t.Parallel()
	home := shortHome(t)
	writeConfig(t, home, map[string]string{
		"runtime.toml": "secrets_dir = \"/srv/s\"\n[parallel]\nworkers = 3\n[sandbox]\nsrt_path = \"/srv/srt\"\nnode_dir = \"/srv/node\"\n" +
			"read_paths = [\"/srv/go\"]\ndeny_roots = [\"/srv/home\", \"/srv/data\"]\n[session_git]\nname = \"fm\"\nemail = \"fm@users.noreply.example\"\n",
		"roles.toml":         rolesTOML,
		"projects/demo.toml": "repo_path = \"/srv/demo\"\ngithub_repo = \"acme/widgets\"\ndefault_branch = \"trunk\"\nl0 = [[\"go\", \"test\", \"./...\"]]\n",
	})
	eff, err := schema.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configFromFiles(home, "/srv/home", eff)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project != "demo" || cfg.Repo.Owner != "acme" || cfg.Repo.Name != "widgets" || cfg.RepoPath != "/srv/demo" ||
		cfg.DefaultBranch != "trunk" || cfg.Remote != "https://github.com/acme/widgets.git" {
		t.Errorf("project values not mapped: %+v", cfg)
	}
	if cfg.SrtPath != "/srv/srt" || cfg.NodeDir != "/srv/node" || cfg.SecretsDir != "/srv/s" || cfg.Workers != 3 ||
		!reflect.DeepEqual(cfg.ReadPaths, []string{"/srv/go"}) || !reflect.DeepEqual(cfg.DenyRoots, []string{"/srv/home", "/srv/data"}) ||
		cfg.OwnerHome != "/srv/home" {
		t.Errorf("runtime values not mapped: %+v", cfg)
	}
	if cfg.SessionGit != (wsgit.Identity{Name: "fm", Email: "fm@users.noreply.example"}) {
		t.Errorf("session_git = %+v", cfg.SessionGit)
	}
	if !reflect.DeepEqual(cfg.L0, [][]string{{"go", "test", "./..."}}) || cfg.Models["coder"] == "" || cfg.Harness != "claude" {
		t.Errorf("l0, models or harness not mapped: %+v", cfg)
	}
}
