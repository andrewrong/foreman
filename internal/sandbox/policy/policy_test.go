package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func base() Inputs {
	return Inputs{
		SessionRepo:     "/w/sessions/r1",
		RunTmp:          "/w/run/r1",
		ForemanHome:     "/h/.foreman",
		ProjectCacheDir: "/h/.foreman/cache/p",
		OwnerHome:       "/h",
		DenyRead:        []string{"/h/secrets"},
		Families:        []Family{FamilyGo, FamilyNPM},
		ModelHost:       "api.example.test",
		Path:            "/usr/bin:/bin",
	}
}

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
	in := base()
	in.Model = &ModelToken{Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: "model-tok"}
	in.TestCreds = []TestCred{{Name: "STRIPE_TEST_KEY", Value: "sk_test_x"}}
	env := mustNew(t, in).Env()

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
		"GOMODCACHE": true, "CLAUDE_TMPDIR": true, "TMPPREFIX": true,
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
	if got["HOME"] != "/w/run/r1/home" || got["TMPPREFIX"] != "/w/run/r1/zsh" {
		t.Errorf("HOME/TMPPREFIX wrong: %q %q", got["HOME"], got["TMPPREFIX"])
	}
}

func TestSandboxEnvWithoutCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "x")
	env := mustNew(t, base()).Env()
	for _, kv := range env {
		if strings.Contains(strings.ToLower(kv), "token") || strings.Contains(kv, "GOFLAGS") {
			t.Errorf("unexpected %q", kv)
		}
	}
}

func TestPolicyRejectsBadCredentials(t *testing.T) {
	cases := map[string]func(*Inputs){
		"reserved": func(i *Inputs) { i.Model = &ModelToken{Name: "PATH", Value: "x"} },
		"badname":  func(i *Inputs) { i.TestCreds = []TestCred{{Name: "A=B", Value: "x"}} },
		"empty":    func(i *Inputs) { i.Model = &ModelToken{Name: "T", Value: ""} },
		"duplicate": func(i *Inputs) {
			i.Model = &ModelToken{Name: "T", Value: "a"}
			i.TestCreds = []TestCred{{Name: "T", Value: "b"}}
		},
	}
	for name, f := range cases {
		in := base()
		f(&in)
		if _, err := New(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPolicyRejectsBadPaths(t *testing.T) {
	cases := map[string]func(*Inputs){
		"relative":      func(i *Inputs) { i.SessionRepo = "r1" },
		"unclean":       func(i *Inputs) { i.RunTmp = "/w/run/../x" },
		"repo-in-home":  func(i *Inputs) { i.SessionRepo = "/h/.foreman/sessions/r1" },
		"tmp-in-cache":  func(i *Inputs) { i.RunTmp = "/h/.foreman/cache/p/run" },
		"bad-host":      func(i *Inputs) { i.ExtraHosts = []string{"https://x.test/"} },
		"unknown-fam":   func(i *Inputs) { i.Families = []Family{"cobol"} },
		"long-tmp":      func(i *Inputs) { i.RunTmp = "/w/" + strings.Repeat("x", 80) },
		"no-path":       func(i *Inputs) { i.Path = "" },
		"relative-deny": func(i *Inputs) { i.DenyRead = []string{"secrets"} },
	}
	for name, f := range cases {
		in := base()
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
	var m map[string]map[string]any
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if top["enableWeakerNetworkIsolation"] != true {
		t.Error("enableWeakerNetworkIsolation not true")
	}
	m = map[string]map[string]any{
		"network":    top["network"].(map[string]any),
		"filesystem": top["filesystem"].(map[string]any),
	}
	return m
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestPolicySettingsStructure(t *testing.T) {
	s := parse(t, mustNew(t, base()))
	fs, nw := s["filesystem"], s["network"]
	wantW := []string{"/w/run/r1", "/w/sessions/r1"}
	if g := strs(fs["allowWrite"]); strings.Join(g, ",") != strings.Join(wantW, ",") {
		t.Errorf("allowWrite %v", g)
	}
	if g := strs(fs["denyWrite"]); strings.Join(g, ",") != "/h/.foreman,/h/.foreman/cache/p" {
		t.Errorf("denyWrite %v", g)
	}
	deny := strings.Join(strs(fs["denyRead"]), ",")
	for _, p := range []string{"/h/secrets", "/h/.ssh", "/h/.config/gh"} {
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

func TestPolicyLocalBindAndDeterminism(t *testing.T) {
	in := base()
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
