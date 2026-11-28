package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Toolchain is the resolved toolchain of one session repo (D-085). Every path
// is real (symlinks resolved) and shims-free.
type Toolchain struct {
	// Path are the directories to put first on the session PATH.
	Path []string
	// Read are the directories to allow reading: the tools' install
	// directories (a go binary reads its GOROOT, the parent of its bin).
	Read []string
}

// ToolchainSource resolves the tools a session repo declares (D-085). It runs
// outside the sandbox, in foremand; tests inject a fake.
type ToolchainSource interface {
	// Toolchain returns what to add for the tools dir declares. The zero
	// Toolchain means "system tools only". A declared tool that is not
	// installed is an error naming it.
	Toolchain(ctx context.Context, dir string) (Toolchain, error)
}

// Mise is the real ToolchainSource: it reads the project's mise.toml through
// `mise env --json` (D-085). The mise binary is found on foremand's PATH at
// every call (like D-084), so an upgrade is picked up by the next launch.
type Mise struct {
	// Bin overrides the PATH lookup of "mise" (tests only).
	Bin string
	// EmptyConfig is a file foremand owns and keeps empty (named *.toml); it
	// is MISE_GLOBAL_CONFIG_FILE, so the owner's global tools (gh ...) never
	// reach a session. Created on first use.
	EmptyConfig string
	// Environ supplies mise's own environment (HOME, XDG_*, MISE_*); nil
	// means the process environment. PATH is never taken from it.
	Environ func() []string
}

func miseProjectFiles() []string {
	return []string{"mise.toml", ".mise.toml", "mise/config.toml", ".config/mise.toml", ".config/mise/config.toml"}
}

var missingRE = regexp.MustCompile(`missing:\s*(\S+)`)

// Toolchain implements ToolchainSource.
func (m Mise) Toolchain(ctx context.Context, dir string) (Toolchain, error) {
	declared := false
	for _, f := range miseProjectFiles() {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			declared = true
			break
		}
	}
	if !declared {
		return Toolchain{}, nil // no mise.toml: system tools only
	}
	bin, err := m.binary()
	if err != nil {
		return Toolchain{}, err
	}
	if m.EmptyConfig == "" {
		return Toolchain{}, errors.New("toolchain: EmptyConfig is not set")
	}
	if err := ensureEmpty(m.EmptyConfig); err != nil {
		return Toolchain{}, err
	}
	base := []string{"/usr/bin", "/bin", filepath.Dir(bin)}
	run := func(args ...string) ([]byte, string, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		cmd.Env = m.env(base, dir)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.Bytes(), errb.String(), err
	}
	out, stderr, err := run("env", "--json")
	if err != nil {
		return Toolchain{}, fmt.Errorf("toolchain: mise env --json in %s: %w: %s", dir, err, strings.TrimSpace(stderr))
	}
	if mm := missingRE.FindStringSubmatch(stderr); mm != nil {
		return Toolchain{}, missingErr(mm[1])
	}
	var installs []string
	if lsOut, _, err := run("ls", "--current", "--json"); err == nil {
		var tool string
		var bad bool
		if installs, tool, bad = parseInstalls(lsOut); bad {
			return Toolchain{}, missingErr(tool)
		}
	}
	pathDirs, err := ParseMiseEnv(out, base)
	if err != nil {
		return Toolchain{}, err
	}
	// PATH dirs are listed too: one outside every install dir must still be readable.
	return Toolchain{Path: pathDirs, Read: append(installs, pathDirs...)}, nil
}

func (m Mise) binary() (string, error) {
	bin := m.Bin
	if bin == "" {
		found, err := exec.LookPath("mise")
		if err != nil {
			return "", errors.New("toolchain: the repo declares tools in mise.toml but mise is not on foremand's PATH (D-085); install mise or remove mise.toml")
		}
		bin = found
	}
	return filepath.Abs(bin)
}

func missingErr(tool string) error {
	return fmt.Errorf("toolchain: %s is declared in mise.toml but not installed; run `mise install` and retry (foremand does not install tools, D-085)", tool)
}

// env is mise's environment: the owner's HOME/XDG/MISE_* locate the installed
// tools, but the global config and trust are foremand's.
func (m Mise) env(base []string, dir string) []string {
	environ := os.Environ
	if m.Environ != nil {
		environ = m.Environ
	}
	env := []string{"PATH=" + strings.Join(base, ":"), "MISE_GLOBAL_CONFIG_FILE=" + m.EmptyConfig,
		"MISE_TRUSTED_CONFIG_PATHS=" + dir, "NO_COLOR=1"}
	own := map[string]bool{"MISE_GLOBAL_CONFIG_FILE": true, "MISE_TRUSTED_CONFIG_PATHS": true, "MISE_ENV": true, "MISE_CONFIG_DIR": true}
	for _, kv := range environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || (strings.HasPrefix(name, "MISE_") && !own[name]) {
			env = append(env, kv)
		}
	}
	return env
}

func ensureEmpty(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
		return nil
	}
	return os.WriteFile(path, nil, 0o600)
}

// parseInstalls reads `mise ls --current --json`: the resolved install
// directory of every declared tool, or the first tool@version that is not
// installed.
func parseInstalls(raw []byte) (dirs []string, missing string, bad bool) {
	var tools map[string][]struct {
		Version     string `json:"version"`
		InstallPath string `json:"install_path"`
		Installed   bool   `json:"installed"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return nil, "", false
	}
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, v := range tools[n] {
			if !v.Installed {
				return nil, n + "@" + v.Version, true
			}
			real, err := filepath.EvalSymlinks(v.InstallPath)
			if err != nil {
				return nil, n + "@" + v.Version, true
			}
			dirs = append(dirs, real)
		}
	}
	return dirs, "", false
}

// ParseMiseEnv extracts from `mise env --json` the PATH entries mise added
// (those not in inherited, the PATH it was started with), drops shims
// directories (a shim does not run inside srt) and resolves symlinks: srt
// matches real paths (D-081 2). A directory that does not resolve is an
// error.
func ParseMiseEnv(raw []byte, inherited []string) ([]string, error) {
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("toolchain: parse mise env: %w", err)
	}
	skip := map[string]bool{}
	for _, d := range inherited {
		skip[d] = true
	}
	var dirs []string
	seen := map[string]bool{}
	for _, d := range strings.Split(env["PATH"], ":") {
		if d == "" || skip[d] || filepath.Base(d) == "shims" || !filepath.IsAbs(d) {
			continue
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			return nil, fmt.Errorf("toolchain: mise tool directory %s does not resolve (run `mise install`): %w", d, err)
		}
		if !seen[real] {
			seen[real] = true
			dirs = append(dirs, real)
		}
	}
	return dirs, nil
}
