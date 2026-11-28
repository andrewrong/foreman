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

func familyHosts() map[Family][]string {
	return map[Family][]string{
		FamilyGo:     {"proxy.golang.org", "sum.golang.org", "*.googleapis.com"},
		FamilyNPM:    {"registry.npmjs.org", "*.npmjs.org"},
		FamilyPython: {"pypi.org", "files.pythonhosted.org"},
		FamilyRust:   {"crates.io", "static.crates.io", "index.crates.io"},
		FamilyGitHub: {"objects.githubusercontent.com", "codeload.github.com"},
	}
}

// AllFamilies lists every R0 family in a fixed order.
func AllFamilies() []Family {
	return []Family{FamilyGo, FamilyNPM, FamilyPython, FamilyRust, FamilyGitHub}
}

// Hosts returns the hosts of a family, or nil for an unknown family.
func (f Family) Hosts() []string { return append([]string(nil), familyHosts()[f]...) }

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
	ForemanHome string // denyWrite: foremand's home (mirror, db, caches); empty when DenyWrite lists subdirectories instead
	// DenyWrite lists denyWrite subdirectories of FOREMAN_HOME (access.yaml
	// layout: db/, config/, projects/<p>/repo.git/, projects/<p>/integrate/).
	// The whole home cannot be denied because session repos and run dirs
	// live inside it, so a deployment sets this and leaves ForemanHome empty.
	DenyWrite []string
	// ProjectCacheDir is the per-project Go cache master (D-066); never
	// writable from the session. The session works on a clone in RunTmp.
	ProjectCacheDir string
	OwnerHome       string   // owner home: denyRead as a whole, ~/.ssh ... listed too
	DenyRead        []string // secrets dirs and other denied paths (must exist)

	// Read allowlist (D-080, D-081). Everything under a deny root is
	// unreadable except the allowRead paths: SessionRepo, RunTmp, MirrorRepo,
	// HarnessPaths and ReadPaths. Nothing is inferred; the owner configures
	// ReadPaths and DenyRoots (sandbox.read_paths, sandbox.deny_roots).
	DenyRoots []string
	// MirrorRepo is the project's mirror repo.git. Only its objects/ is
	// allowRead (session repos borrow its objects, access.yaml R0); config and
	// refs stay unreadable. Its write protection comes from
	// the caller listing it in DenyWrite.
	MirrorRepo string
	// HarnessPaths are the install paths the harness adapter declares
	// (api.Invocation.ReadPaths).
	HarnessPaths []string
	ReadPaths    []string // toolchain directories, from sandbox.read_paths

	// SessionGit fills the generated session .gitconfig (D-082).
	SessionGit SessionGit

	systemRoots   []string // resolved by New: SystemDenyRoots (D-090 ⑥)
	ownerSecrets  []string // resolved by New: OwnerHome/<secret dirs>
	mirrorObjects string   // resolved by New: MirrorRepo/objects (access.yaml R0)

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
func ownerSecretDirs() []string {
	return []string{".ssh", ".config/gh", ".aws", ".gnupg", ".netrc"}
}

const maxRunTmpLen = 70

// New validates inputs and returns a Policy.
func New(in Inputs) (Policy, error) {
	denied, err := checkPaths(in)
	if err != nil {
		return Policy{}, err
	}
	in, err = resolveInputs(in)
	if err != nil {
		return Policy{}, err
	}
	denied = append([]string{}, in.DenyWrite...)
	if in.ForemanHome != "" {
		denied = append(denied, in.ForemanHome)
	}
	if err := checkSessionDirs(in, denied); err != nil {
		return Policy{}, err
	}
	if err := checkAllowRead(in); err != nil {
		return Policy{}, err
	}
	if err := checkSessionGit(in.SessionGit); err != nil {
		return Policy{}, err
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
		if familyHosts()[f] == nil {
			return Policy{}, fmt.Errorf("policy: unknown host family %q", f)
		}
	}
	if err := checkCreds(in); err != nil {
		return Policy{}, err
	}
	return Policy{in: in}, nil
}

// checkPaths validates every path input and returns the deny-write list.
func checkPaths(in Inputs) ([]string, error) {
	for name, p := range map[string]string{"SessionRepo": in.SessionRepo, "RunTmp": in.RunTmp} {
		if err := absPath(name, p); err != nil {
			return nil, err
		}
	}
	if in.ForemanHome == "" && len(in.DenyWrite) == 0 {
		return nil, errors.New("policy: ForemanHome or DenyWrite is required")
	}
	denied := append([]string{}, in.DenyWrite...)
	if in.ForemanHome != "" {
		denied = append(denied, in.ForemanHome)
	}
	for _, p := range denied {
		if err := absPath("DenyWrite", p); err != nil {
			return nil, err
		}
	}
	for name, p := range map[string]string{"ProjectCacheDir": in.ProjectCacheDir, "OwnerHome": in.OwnerHome, "MirrorRepo": in.MirrorRepo} {
		if p != "" {
			if err := absPath(name, p); err != nil {
				return nil, err
			}
		}
	}
	for name, list := range map[string][]string{"DenyRead": in.DenyRead, "DenyRoots": in.DenyRoots,
		"HarnessPaths": in.HarnessPaths, "ReadPaths": in.ReadPaths} {
		for _, p := range list {
			if err := absPath(name, p); err != nil {
				return nil, err
			}
		}
	}
	return denied, nil
}

// checkSessionDirs: a deny-write ancestor would silently override the
// allowWrite entry.
func checkSessionDirs(in Inputs, denied []string) error {
	for name, p := range map[string]string{"SessionRepo": in.SessionRepo, "RunTmp": in.RunTmp} {
		if in.ProjectCacheDir != "" && within(p, in.ProjectCacheDir) {
			return fmt.Errorf("policy: %s is inside a deny-write directory", name)
		}
		for _, d := range denied {
			if within(p, d) {
				return fmt.Errorf("policy: %s is inside a deny-write directory", name)
			}
		}
	}
	return nil
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
		for _, h := range familyHosts()[f] {
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
	for _, d := range p.in.DenyRoots {
		set[d] = true
	}
	for _, d := range p.in.systemRoots {
		set[d] = true
	}
	if p.in.OwnerHome != "" {
		set[p.in.OwnerHome] = true
		for _, d := range p.in.ownerSecrets {
			set[d] = true
		}
	}
	return sortedKeys(set)
}

// AllowReadPaths returns the sorted allowRead list (D-080, D-081).
func (p Policy) AllowReadPaths() []string {
	set := map[string]bool{p.in.SessionRepo: true, p.in.RunTmp: true}
	if p.in.mirrorObjects != "" {
		set[p.in.mirrorObjects] = true
	}
	for _, l := range [][]string{p.in.HarnessPaths, p.in.ReadPaths} {
		for _, d := range l {
			set[d] = true
		}
	}
	return sortedKeys(set)
}

// DenyWritePaths returns the sorted denyWrite list.
func (p Policy) DenyWritePaths() []string {
	set := map[string]bool{}
	if p.in.ForemanHome != "" {
		set[p.in.ForemanHome] = true
	}
	for _, d := range p.in.DenyWrite {
		set[d] = true
	}
	if p.in.ProjectCacheDir != "" {
		set[p.in.ProjectCacheDir] = true
	}
	return sortedKeys(set)
}

// Home is the HOME of the session, inside the run tmp.
func (p Policy) Home() string { return filepath.Join(p.in.RunTmp, "home") }

// ProjectCacheDir is the project cache master (never writable from the
// session); empty when the policy has none.
func (p Policy) ProjectCacheDir() string { return p.in.ProjectCacheDir }

// DefaultGoFlags are the GOFLAGS of sessions, L0 and the cache warm-up (they
// must agree, or the build cache would not hit). -trimpath keeps package
// action IDs independent of the per-run module cache path; without it every
// dependency in GOMODCACHE would recompile in each run. -modcacherw keeps the
// module cache deletable (D-086 removes the run directory).
const DefaultGoFlags = "-trimpath -modcacherw"

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
	AllowRead  []string `json:"allowRead"`
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
			AllowRead:  p.AllowReadPaths(),
			AllowWrite: []string{p.in.RunTmp, p.in.SessionRepo},
			DenyWrite:  p.DenyWritePaths(),
		},
		Weaker: true,
	}
	sort.Strings(s.Filesystem.AllowWrite)
	return json.MarshalIndent(s, "", "  ")
}
