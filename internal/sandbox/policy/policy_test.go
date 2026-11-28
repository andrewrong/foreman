package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a real directory tree: policy.New resolves every path and
// refuses a missing one (D-081 ②). The root is short and symlink-free
// (srt's socket path limit; macOS /tmp is a symlink).
type fixture struct {
	root string
	in   Inputs
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "fmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	root, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"w/sessions/r1", "w/run/r1", "h/.foreman/cache/p", "h/secrets", "h/.ssh", "m/repo.git/objects", "tc/go", "data"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	j := func(sub string) string { return filepath.Join(root, sub) }
	return fixture{root: root, in: Inputs{
		SessionRepo:     j("w/sessions/r1"),
		RunTmp:          j("w/run/r1"),
		ForemanHome:     j("h/.foreman"),
		ProjectCacheDir: j("h/.foreman/cache/p"),
		OwnerHome:       j("h"),
		DenyRead:        []string{j("h/secrets")},
		DenyRoots:       []string{j("data")},
		MirrorRepo:      j("m/repo.git"),
		ReadPaths:       []string{j("tc/go")},
		HarnessPaths:    []string{j("tc/go")},
		SessionGit:      SessionGit{Name: "foreman-session", Email: "foreman-session@users.noreply.example", DefaultBranch: "main"},
		Families:        []Family{FamilyGo, FamilyNPM},
		ModelHost:       "api.example.test",
		Path:            "/usr/bin:/bin",
	}}
}

func (f fixture) p(sub string) string { return filepath.Join(f.root, sub) }

func base(t *testing.T) Inputs { return newFixture(t).in }

func mustNew(t *testing.T, in Inputs) Policy {
	t.Helper()
	p, err := New(in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSandboxEnvHasOnlyAllowedCredentials(t *testing.T) {
	for _, n := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "FOREMAN_SECRET", "SSH_AUTH_SOCK", "GH_TOKEN"} {
		t.Setenv(n, "leak-"+n)
	}
	in := base(t)
	in.Model = &ModelToken{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: "model-tok"}
	in.TestCreds = []TestCred{{Name: "STRIPE_TEST_KEY", Value: "sk_test_x"}}
	pol := mustNew(t, in)
	env := pol.Env()

	got := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed entry %q", kv)
		}
		got[k] = v
		if strings.HasPrefix(v, "leak-") {
			t.Errorf("leaked parent value in %s", k)
		}
	}
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "TERM": true, "GOCACHE": true,
		"GOMODCACHE": true, "CLAUDE_TMPDIR": true, "CLAUDE_CODE_TMPDIR": true, "TMPPREFIX": true,
		"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_NOSYSTEM": true,
		"CLAUDE_CODE_OAUTH_TOKEN": true, "STRIPE_TEST_KEY": true,
	}
	for k := range got {
		if !allowed[k] {
			t.Errorf("unexpected variable %s", k)
		}
	}
	for k := range allowed {
		if _, ok := got[k]; !ok {
			t.Errorf("missing variable %s", k)
		}
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "model-tok" || got["STRIPE_TEST_KEY"] != "sk_test_x" {
		t.Error("allowed credentials not passed through")
	}
	if got["HOME"] != filepath.Join(in.RunTmp, "home") || got["TMPPREFIX"] != in.RunTmp+"/zsh" {
		t.Errorf("HOME/TMPPREFIX wrong: %q %q", got["HOME"], got["TMPPREFIX"])
	}
	if got["GIT_CONFIG_NOSYSTEM"] != "1" || got["GIT_CONFIG_GLOBAL"] != filepath.Join(in.RunTmp, "home", ".gitconfig") {
		t.Errorf("session git env wrong: %q %q", got["GIT_CONFIG_NOSYSTEM"], got["GIT_CONFIG_GLOBAL"])
	}
}

func TestSandboxEnvWithoutCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "x")
	env := mustNew(t, base(t)).Env()
	for _, kv := range env {
		if strings.Contains(strings.ToLower(kv), "token") || strings.Contains(kv, "GOFLAGS") {
			t.Errorf("unexpected %q", kv)
		}
	}
}

func TestPolicyRejectsBadCredentials(t *testing.T) {
	cases := map[string]func(*Inputs){
		"reserved": func(i *Inputs) { i.Model = &ModelToken{Name: "PATH", Value: "x"} },
		"git-env":  func(i *Inputs) { i.Model = &ModelToken{Name: "GIT_CONFIG_GLOBAL", Value: "x"} },
		"badname":  func(i *Inputs) { i.TestCreds = []TestCred{{Name: "A=B", Value: "x"}} },
		"empty":    func(i *Inputs) { i.Model = &ModelToken{Name: "T", Value: ""} },
		"duplicate": func(i *Inputs) {
			i.Model = &ModelToken{Name: "T", Value: "a"}
			i.TestCreds = []TestCred{{Name: "T", Value: "b"}}
		},
	}
	for name, f := range cases {
		in := base(t)
		f(&in)
		if _, err := New(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPolicyRejectsBadPaths(t *testing.T) {
	fx := newFixture(t)
	long := fx.p("w/" + strings.Repeat("x", 80))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Inputs){
		"relative":      func(i *Inputs) { i.SessionRepo = "r1" },
		"unclean":       func(i *Inputs) { i.RunTmp = fx.root + "/w/run/../run/r1" },
		"repo-in-home":  func(i *Inputs) { i.SessionRepo = fx.p("h/.foreman") },
		"tmp-in-cache":  func(i *Inputs) { i.RunTmp = fx.p("h/.foreman/cache/p") },
		"bad-host":      func(i *Inputs) { i.ExtraHosts = []string{"https://x.test/"} },
		"unknown-fam":   func(i *Inputs) { i.Families = []Family{"cobol"} },
		"long-tmp":      func(i *Inputs) { i.RunTmp = long },
		"no-path":       func(i *Inputs) { i.Path = "" },
		"relative-deny": func(i *Inputs) { i.DenyRead = []string{"secrets"} },
		"relative-path": func(i *Inputs) { i.Path = "/usr/bin:bin" },
	}
	for name, f := range cases {
		in := fx.in
		f(&in)
		if _, err := New(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func parse(t *testing.T, p Policy) map[string]map[string]any {
	t.Helper()
	b, err := p.SettingsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if top["enableWeakerNetworkIsolation"] != true {
		t.Error("enableWeakerNetworkIsolation not true")
	}
	return map[string]map[string]any{
		"network":    top["network"].(map[string]any),
		"filesystem": top["filesystem"].(map[string]any),
	}
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestPolicySettingsStructure(t *testing.T) {
	fx := newFixture(t)
	s := parse(t, mustNew(t, fx.in))
	fs, nw := s["filesystem"], s["network"]
	wantW := []string{fx.p("w/run/r1"), fx.p("w/sessions/r1")}
	if g := strs(fs["allowWrite"]); strings.Join(g, ",") != strings.Join(wantW, ",") {
		t.Errorf("allowWrite %v", g)
	}
	if g := strs(fs["denyWrite"]); strings.Join(g, ",") != fx.p("h/.foreman")+","+fx.p("h/.foreman/cache/p") {
		t.Errorf("denyWrite %v", g)
	}
	deny := strings.Join(strs(fs["denyRead"]), ",")
	for _, p := range []string{fx.p("h/secrets"), fx.p("h/.ssh"), fx.p("h"), fx.p("data")} {
		if !strings.Contains(deny, p) {
			t.Errorf("denyRead lacks %s: %s", p, deny)
		}
	}
	hosts := strings.Join(strs(nw["allowedDomains"]), ",")
	for _, h := range []string{"proxy.golang.org", "sum.golang.org", "registry.npmjs.org", "api.example.test"} {
		if !strings.Contains(hosts, h) {
			t.Errorf("hosts lack %s", h)
		}
	}
	if strings.Contains(hosts, "pypi.org") || strings.Contains(hosts, "crates.io") {
		t.Errorf("undeclared families present: %s", hosts)
	}
	if nw["allowLocalBinding"] != false {
		t.Error("local binding must default off")
	}
	if len(strs(nw["allowUnixSockets"])) != 0 || len(strs(nw["deniedDomains"])) != 0 {
		t.Error("unix sockets / denied domains must be empty lists")
	}
}

// D-080, D-081 ①: allowRead is the session repo, the run dir, the mirror's
// objects/ only (access.yaml R0), the harness paths and the configured read paths, sorted and
// without duplicates (a path both declared and configured appears once).
func TestPolicyAllowReadIsTheAllowlist(t *testing.T) {
	fx := newFixture(t)
	s := parse(t, mustNew(t, fx.in))
	want := []string{fx.p("m/repo.git/objects"), fx.p("tc/go"), fx.p("w/run/r1"), fx.p("w/sessions/r1")}
	if g := strs(s["filesystem"]["allowRead"]); strings.Join(g, ",") != strings.Join(want, ",") {
		t.Errorf("allowRead %v, want %v", g, want)
	}
	if g := strs(s["filesystem"]["allowWrite"]); strings.Contains(strings.Join(g, ","), "repo.git") {
		t.Errorf("the mirror must stay read-only, allowWrite %v", g)
	}
}

// D-081 ②: a symlinked path is replaced by its real path in every list, and
// a missing path is an error naming it.
func TestPolicyResolvesSymlinksAndRefusesMissingPaths(t *testing.T) {
	fx := newFixture(t)
	link := fx.p("lts")
	if err := os.Symlink(fx.p("tc/go"), link); err != nil {
		t.Fatal(err)
	}
	binLink := fx.p("shims")
	if err := os.Symlink("/usr/bin", binLink); err != nil {
		t.Fatal(err)
	}
	in := fx.in
	in.ReadPaths = []string{link}
	in.HarnessPaths = nil
	in.Path = binLink + ":/bin"
	pol := mustNew(t, in)
	if got := strings.Join(pol.AllowReadPaths(), ","); strings.Contains(got, link) || !strings.Contains(got, fx.p("tc/go")) {
		t.Errorf("allowRead not resolved: %s", got)
	}
	var path string
	for _, kv := range pol.Env() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if strings.Contains(path, "shims") || !strings.HasPrefix(path, "/usr/bin:") {
		t.Errorf("PATH not resolved: %s", path)
	}

	missing := fx.p("nope")
	cases := map[string]func(*Inputs){
		"ReadPaths":    func(i *Inputs) { i.ReadPaths = []string{missing} },
		"HarnessPaths": func(i *Inputs) { i.HarnessPaths = []string{missing} },
		"MirrorRepo":   func(i *Inputs) { i.MirrorRepo = missing },
		"DenyRoots":    func(i *Inputs) { i.DenyRoots = []string{missing} },
		"DenyRead":     func(i *Inputs) { i.DenyRead = []string{missing} },
		"DenyWrite":    func(i *Inputs) { i.DenyWrite = []string{missing} },
		"PATH entry":   func(i *Inputs) { i.Path = "/usr/bin:" + missing },
		"RunTmp":       func(i *Inputs) { i.RunTmp = missing },
	}
	for name, f := range cases {
		in := fx.in
		f(&in)
		_, err := New(in)
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error %v does not name the missing path", name, err)
		}
	}
}

// A symlink must not hide that a session directory sits inside a deny-write
// directory: the check runs on real paths.
func TestPolicyChecksRealPathsAgainstDenyWrite(t *testing.T) {
	fx := newFixture(t)
	alias := fx.p("alias")
	if err := os.Symlink(fx.p("h/.foreman/cache/p"), alias); err != nil {
		t.Fatal(err)
	}
	in := fx.in
	in.RunTmp = alias
	if _, err := New(in); err == nil {
		t.Error("run dir reaching a deny-write directory through a symlink accepted")
	}
}

// srt lets allowRead override denyRead, so an allowRead path covering a
// denied one would undo the denial.
func TestPolicyRefusesAllowReadCoveringDenyRead(t *testing.T) {
	fx := newFixture(t)
	for name, p := range map[string]string{"secrets": fx.p("h/secrets"), "owner home": fx.p("h"), "deny root": fx.p("data")} {
		in := fx.in
		in.ReadPaths = []string{p}
		if _, err := New(in); err == nil {
			t.Errorf("%s: read path accepted", name)
		}
	}
}

func TestPolicyLocalBindAndDeterminism(t *testing.T) {
	in := base(t)
	in.LocalBind = true
	in.Families = AllFamilies()
	in.ExtraHosts = []string{"z.test", "a.test", "proxy.golang.org"}
	p := mustNew(t, in)
	a, _ := p.SettingsJSON()
	b, _ := p.SettingsJSON()
	if string(a) != string(b) {
		t.Error("not deterministic")
	}
	s := parse(t, p)
	if s["network"]["allowLocalBinding"] != true {
		t.Error("local binding not set")
	}
	hosts := strs(s["network"]["allowedDomains"])
	seen := map[string]int{}
	for i, h := range hosts {
		seen[h]++
		if i > 0 && hosts[i-1] > h {
			t.Errorf("not sorted: %v", hosts)
		}
	}
	if seen["proxy.golang.org"] != 1 || seen["crates.io"] != 1 || seen["codeload.github.com"] != 1 {
		t.Errorf("hosts %v", hosts)
	}
}

// D-082: the exact content of the generated session gitconfig and the
// environment that points git at it.
func TestSessionGitconfigGenerated(t *testing.T) {
	in := base(t)
	in.SessionGit = SessionGit{Name: "Fore Man", Email: "fm@users.noreply.example", DefaultBranch: "trunk"}
	pol := mustNew(t, in)
	want := "[user]\n\tname = \"Fore Man\"\n\temail = \"fm@users.noreply.example\"\n" +
		"[init]\n\tdefaultBranch = \"trunk\"\n" +
		"[commit]\n\tgpgsign = false\n" +
		"[core]\n\teditor = true\n\tpager = cat\n" +
		"[advice]\n\tdetachedHead = false\n" +
		"[push]\n\tdefault = nothing\n"
	if got := string(pol.GitConfig()); got != want {
		t.Errorf("gitconfig:\n%s\nwant:\n%s", got, want)
	}
	if pol.GitConfigPath() != filepath.Join(pol.Home(), ".gitconfig") {
		t.Errorf("path %s", pol.GitConfigPath())
	}
	env := strings.Join(pol.Env(), "\n")
	for _, kv := range []string{"GIT_CONFIG_GLOBAL=" + pol.GitConfigPath(), "GIT_CONFIG_NOSYSTEM=1"} {
		if !strings.Contains(env, kv) {
			t.Errorf("env lacks %s", kv)
		}
	}
	if strings.Contains(env, "/dev/null") {
		t.Error("GIT_CONFIG_GLOBAL=/dev/null was superseded by D-082")
	}

	for name, g := range map[string]SessionGit{
		"no name":    {Email: "a@b", DefaultBranch: "main"},
		"no email":   {Name: "n", DefaultBranch: "main"},
		"no branch":  {Name: "n", Email: "a@b"},
		"newline":    {Name: "n\n[core]", Email: "a@b", DefaultBranch: "main"},
		"not-an-adr": {Name: "n", Email: "ab", DefaultBranch: "main"},
	} {
		bad := base(t)
		bad.SessionGit = g
		if _, err := New(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
