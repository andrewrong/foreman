// Package policy builds the OS-level sandbox policy for one session run
// (spec/design/access.yaml R0..R3, C-08, D-066) and renders it as srt
// settings JSON. It reads nothing from the process environment.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Family is a package-repository host family granted by access.yaml R0.
type Family string

// Host families of R0.
const (
	FamilyGo     Family = "go"
	FamilyNPM    Family = "npm"
	FamilyPython Family = "python"
	FamilyRust   Family = "rust"
	FamilyGitHub Family = "github-download"
)

var familyHosts = map[Family][]string{
	FamilyGo:     {"proxy.golang.org", "sum.golang.org", "*.googleapis.com"},
	FamilyNPM:    {"registry.npmjs.org", "*.npmjs.org"},
	FamilyPython: {"pypi.org", "files.pythonhosted.org"},
	FamilyRust:   {"crates.io", "static.crates.io", "index.crates.io"},
	FamilyGitHub: {"objects.githubusercontent.com", "codeload.github.com"},
}

// AllFamilies lists every R0 family in a fixed order.
func AllFamilies() []Family {
	return []Family{FamilyGo, FamilyNPM, FamilyPython, FamilyRust, FamilyGitHub}
}

// Hosts returns the hosts of a family, or nil for an unknown family.
func (f Family) Hosts() []string { return append([]string(nil), familyHosts[f]...) }

// Cred is one environment credential passed in by the caller. Only the two
// C-08 classes exist: the model token and R1 test credentials.
type Cred struct{ Name, Value string }

// ModelToken is the harness credential (foreman-specific, revocable, D-045).
type ModelToken Cred

// TestCred is an R1 development test credential (no production authority).
type TestCred Cred

// Inputs describe one run. All paths must be clean and absolute.
type Inputs struct {
	SessionRepo string // writable: the session's own repo (D-045)
	RunTmp      string // writable: run temp dir; HOME lives inside
	ForemanHome string // denyWrite: foremand's home (mirror, db, caches)
	// ProjectCacheDir is the per-project Go cache master (D-066); never
	// writable from the session. The session works on a clone in RunTmp.
	ProjectCacheDir string
	OwnerHome       string   // owner home; ~/.ssh, ~/.config/gh ... are denyRead
	DenyRead        []string // secrets dirs and other denied paths

	Families   []Family // R0 package hosts
	ModelHost  string   // declared by the harness adapter
	ExtraHosts []string // R1 declared external hosts
	LocalBind  bool     // per-role localhost binding (default false)

	// Environment values, supplied by the caller (never read from os).
	Path      string // PATH for the session
	Lang      string // default en_US.UTF-8
	GoFlags   string // optional
	Model     *ModelToken
	TestCreds []TestCred
}

// Policy is a validated policy.
type Policy struct{ in Inputs }

// ownerSecretDirs are denied relative to OwnerHome.
var ownerSecretDirs = []string{".ssh", ".config/gh", ".aws", ".gnupg", ".netrc"}

const maxRunTmpLen = 70

// New validates inputs and returns a Policy.
func New(in Inputs) (Policy, error) {
	for name, p := range map[string]string{"SessionRepo": in.SessionRepo, "RunTmp": in.RunTmp, "ForemanHome": in.ForemanHome} {
		if err := absPath(name, p); err != nil {
			return Policy{}, err
		}
	}
	for name, p := range map[string]string{"ProjectCacheDir": in.ProjectCacheDir, "OwnerHome": in.OwnerHome} {
		if p != "" {
			if err := absPath(name, p); err != nil {
				return Policy{}, err
			}
		}
	}
	for _, p := range in.DenyRead {
		if err := absPath("DenyRead", p); err != nil {
			return Policy{}, err
		}
	}
	// A deny-write ancestor would silently override the allowWrite entry.
	for name, p := range map[string]string{"SessionRepo": in.SessionRepo, "RunTmp": in.RunTmp} {
		if within(p, in.ForemanHome) || (in.ProjectCacheDir != "" && within(p, in.ProjectCacheDir)) {
			return Policy{}, fmt.Errorf("policy: %s is inside a deny-write directory", name)
		}
	}
	// srt creates a unix socket "<RunTmp>/srt-mux-<pid>-<n>.sock"; sun_path
	// holds ~104 bytes on macOS, so a long RunTmp breaks every launch.
	if len(in.RunTmp) > maxRunTmpLen {
		return Policy{}, fmt.Errorf("policy: RunTmp longer than %d bytes (unix socket path limit)", maxRunTmpLen)
	}
	if in.Path == "" {
		return Policy{}, errors.New("policy: Path is required")
	}
	for _, h := range hostList(in) {
		if err := validHost(h); err != nil {
			return Policy{}, err
		}
	}
	for _, f := range in.Families {
		if familyHosts[f] == nil {
			return Policy{}, fmt.Errorf("policy: unknown host family %q", f)
		}
	}
	if err := checkCreds(in); err != nil {
		return Policy{}, err
	}
	return Policy{in: in}, nil
}

func absPath(name, p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("policy: %s must be a clean absolute path", name)
	}
	return nil
}

func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

func validHost(h string) error {
	if h == "" || strings.ContainsAny(h, "/: \t\n\"") || strings.Contains(h, "**") {
		return fmt.Errorf("policy: invalid host %q", h)
	}
	return nil
}

func hostList(in Inputs) []string {
	set := map[string]bool{}
	for _, f := range in.Families {
		for _, h := range familyHosts[f] {
			set[h] = true
		}
	}
	if in.ModelHost != "" {
		set[in.ModelHost] = true
	}
	for _, h := range in.ExtraHosts {
		set[h] = true
	}
	return sortedKeys(set)
}

// AllowedHosts returns the sorted, de-duplicated allowed host list.
func (p Policy) AllowedHosts() []string { return hostList(p.in) }

// SessionRepo returns the writable session repo.
func (p Policy) SessionRepo() string { return p.in.SessionRepo }

// RunTmp returns the run temp dir.
func (p Policy) RunTmp() string { return p.in.RunTmp }

// LocalBind reports whether localhost binding is allowed.
func (p Policy) LocalBind() bool { return p.in.LocalBind }

// DenyReadPaths returns the sorted denyRead list.
func (p Policy) DenyReadPaths() []string {
	set := map[string]bool{}
	for _, d := range p.in.DenyRead {
		set[d] = true
	}
	if p.in.OwnerHome != "" {
		for _, d := range ownerSecretDirs {
			set[filepath.Join(p.in.OwnerHome, d)] = true
		}
	}
	return sortedKeys(set)
}

// DenyWritePaths returns the sorted denyWrite list.
func (p Policy) DenyWritePaths() []string {
	set := map[string]bool{p.in.ForemanHome: true}
	if p.in.ProjectCacheDir != "" {
		set[p.in.ProjectCacheDir] = true
	}
	return sortedKeys(set)
}

// Home is the HOME of the session, inside the run tmp.
func (p Policy) Home() string { return filepath.Join(p.in.RunTmp, "home") }

// GoCache is the session's writable clone of the project Go build cache.
func (p Policy) GoCache() string { return filepath.Join(p.in.RunTmp, "gocache") }

// GoModCache is the session's writable clone of the project module cache.
func (p Policy) GoModCache() string { return filepath.Join(p.in.RunTmp, "gomodcache") }

// SettingsPath is where the launcher writes the srt settings file.
func (p Policy) SettingsPath() string { return filepath.Join(p.in.RunTmp, "srt-settings.json") }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type srtSettings struct {
	Network    srtNetwork    `json:"network"`
	Filesystem srtFilesystem `json:"filesystem"`
	Weaker     bool          `json:"enableWeakerNetworkIsolation"`
}

type srtNetwork struct {
	AllowedDomains    []string `json:"allowedDomains"`
	DeniedDomains     []string `json:"deniedDomains"`
	AllowUnixSockets  []string `json:"allowUnixSockets"`
	AllowLocalBinding bool     `json:"allowLocalBinding"`
}

type srtFilesystem struct {
	DenyRead   []string `json:"denyRead"`
	AllowWrite []string `json:"allowWrite"`
	DenyWrite  []string `json:"denyWrite"`
}

// SettingsJSON renders the srt settings deterministically.
func (p Policy) SettingsJSON() ([]byte, error) {
	s := srtSettings{
		Network: srtNetwork{
			AllowedDomains:    p.AllowedHosts(),
			DeniedDomains:     []string{},
			AllowUnixSockets:  []string{},
			AllowLocalBinding: p.in.LocalBind,
		},
		Filesystem: srtFilesystem{
			DenyRead:   p.DenyReadPaths(),
			AllowWrite: []string{p.in.RunTmp, p.in.SessionRepo},
			DenyWrite:  p.DenyWritePaths(),
		},
		Weaker: true,
	}
	sort.Strings(s.Filesystem.AllowWrite)
	return json.MarshalIndent(s, "", "  ")
}
