package privguard

import (
	"bytes"
	"strings"
	"testing"
)

// Forbidden inputs are assembled at run time so that this file holds none.
func j(parts ...string) string { return strings.Join(parts, "") }

var (
	homePath = j("/Us", "ers/alice/work")
	volPath  = j("/Vol", "umes/disk/x")
	winPath  = j(`C:\Us`, `ers\bob`)
	email    = j("alice", "@", "corp.", "io")
	ip       = j("10.", "20.", "30.", "40")
	ghToken  = j("gh", "p_", strings.Repeat("a1B2", 9))
	pem      = j("-----BEG", "IN RSA PRIV", "ATE KEY-----")
)

func kinds(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func has(fs []Finding, kind string) bool {
	for _, f := range fs {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func TestPrivguardTextRules(t *testing.T) {
	cases := []struct {
		name, line, kind string
	}{
		{"home", "see " + homePath, "abs-path"},
		{"volume", "cd " + volPath, "abs-path"},
		{"windows", "at " + winPath, "abs-path"},
		{"email", "mail " + email, "email"},
		{"ipv4", "host " + ip, "ipv4"},
		{"ipv4-port", "dial " + ip + ":8080", "ipv4"},
		{"host-port", "dial " + j("db.corp.", "io:5432"), "host-port"},
		{"token", "key=" + ghToken, "token"},
		{"private-key", pem, "private-key"},
		{"url-credentials", j("https://bot:", "s3cr3tvalue@host.invalid/x"), "url-credentials"},
		{"chat-id", "chat " + j("-100", "1234567890"), "chat-id"},
		{"hex", "digest " + strings.Repeat("9f3a", 10), "long-hex"},
		{"base64", "secret " + j("Zm9vYmFy", "QmF6UXV4", "MTIzNDU2", "Nzg5MGFi", "Y2RlZmdo", "aWprbG1u"), "long-base64"},
	}
	for _, c := range cases {
		if got := Scan("x.md", []byte(c.line)); !has(got, c.kind) {
			t.Errorf("%s: want %s, got %v", c.name, c.kind, kinds(got))
		}
	}
	clean := []string{
		"git@" + "users.noreply.github.com is fine",
		"no-reply@" + "example.org",
		"loopback 127.0.0.1:8080",
		"version 1.2.3 and file util.go:42",
		"go.sum h1:" + j("abcDEF123", "abcDEF123", "abcDEF123", "abcDEF123", "xyz=="),
		"func TestVeryLongCamelCaseNameThatDescribesBehaviourInDetail1(t *testing.T)",
		"https://example.test/home/page",
		"1.2.3.4.5.6",
	}
	for _, l := range clean {
		if got := Scan("x.md", []byte(l)); len(got) != 0 {
			t.Errorf("%q: unexpected findings %v", l, kinds(got))
		}
	}
}

func TestPrivguardFindingsDoNotCarryMatch(t *testing.T) {
	fs := Scan("a.md", []byte("x\n"+ghToken+"\n"))
	if len(fs) != 1 || fs[0].Line != 2 || fs[0].File != "a.md" || fs[0].Rule != RuleSecrets {
		t.Fatalf("findings: %+v", fs)
	}
	if strings.Contains(fs[0].String(), "a1B2") {
		t.Fatalf("finding text leaks the match: %s", fs[0])
	}
}

func TestPrivguardNoDirExemption(t *testing.T) {
	content := []byte("path " + homePath + "\n")
	for _, name := range []string{"README.md", "docs/design/x.md", "docs/a/b/c.md", "testdata/fixture.txt",
		".privguard/allow.yaml", "internal/privguard/privguard.go", "spec/design/privguard.yaml", `docs\win.md`} {
		fs := Scan(name, content)
		if len(fs) != 1 || fs[0].Kind != "abs-path" || fs[0].Line != 1 {
			t.Errorf("%s: want one abs-path finding, got %v", name, fs)
		}
	}
	// The only exemption is an inline marker with a registered reason, and it
	// covers its own line only.
	sc, err := New(Config{AllowReasons: []string{"rule-definition"}})
	if err != nil {
		t.Fatal(err)
	}
	text := "a " + homePath + " privguard:allow rule-definition\n" +
		"b " + homePath + " privguard:allow unknown-reason\n" +
		"c " + homePath + "\n"
	fs := sc.Scan("docs/x.md", []byte(text))
	if len(fs) != 2 || fs[0].Line != 2 || fs[1].Line != 3 {
		t.Fatalf("want findings on lines 2 and 3, got %v", fs)
	}
	// Without a registered reason nothing is exempt.
	if got := Scan("docs/x.md", []byte(text)); len(got) != 3 {
		t.Fatalf("default scanner honours markers: %v", got)
	}
	// A marker never exempts a secret.
	line := ghToken + " privguard:allow rule-definition"
	if got := sc.Scan("x.md", []byte(line)); !has(got, "token") {
		t.Fatalf("marker exempted a secret: %v", got)
	}
}

func TestPrivguardNoPathExemption(t *testing.T) {
	other := j("gh", "s_", strings.Repeat("Z9y8", 9))
	cfg := Config{Fingerprints: []string{"token:" + Fingerprint(ghToken)}}
	sc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// An exemption is a rule plus a fingerprint; it holds in every path.
	for _, name := range []string{"a.md", "docs/design/b.md", "testdata/c.txt"} {
		if got := sc.Scan(name, []byte("k "+ghToken)); len(got) != 0 {
			t.Errorf("%s: fingerprinted secret still reported: %v", name, got)
		}
		if got := sc.Scan(name, []byte("k "+other)); !has(got, "token") {
			t.Errorf("%s: a different secret was exempted: %v", name, got)
		}
		// The fingerprint is for the token rule only.
		if got := sc.Scan(name, []byte("p "+homePath)); !has(got, "abs-path") {
			t.Errorf("%s: fingerprint exempted another rule: %v", name, got)
		}
	}
	// The same content gives the same findings under any name.
	a := kinds(Scan("a/b/c.md", []byte("k "+other+" "+email)))
	b := kinds(Scan("docs/design/x.md", []byte("k "+other+" "+email)))
	if strings.Join(a, ",") != strings.Join(b, ",") || len(a) != 2 {
		t.Fatalf("findings depend on the path: %v vs %v", a, b)
	}
}

func TestPrivguardBinaryRejected(t *testing.T) {
	big := bytes.Repeat([]byte("a"), MaxFileBytes+1)
	cases := []struct {
		name    string
		content []byte
		kind    string
	}{
		{"cmd/tool/tool", []byte("\x7fELF\x02\x01\x01"), "binary"},
		{"bin/tool.go", []byte{0xcf, 0xfa, 0xed, 0xfe, 0x07}, "binary"},
		{"win.go", []byte("MZ\x90\x00\x03\x00"), "binary"},
		{"data.txt", []byte("text\x00more"), "binary"},
		{"shot.png", []byte("not really png"), "forbidden-file"},
		{"x/.env", []byte("A=1"), "forbidden-file"},
		{"x/.env.local", []byte("A=1"), "forbidden-file"},
		{"cert.pem", []byte("x"), "forbidden-file"},
		{"id.key", []byte("x"), "forbidden-file"},
		{"state.db", []byte("x"), "forbidden-file"},
		{"pkg.tar", []byte("x"), "forbidden-file"},
		{"unknown.xyz", []byte("x"), "file-type-not-allowed"},
		{"big.md", big, "too-large"},
	}
	for _, c := range cases {
		fs := Scan(c.name, c.content)
		if !has(fs, c.kind) {
			t.Errorf("%s: want %s, got %v", c.name, c.kind, kinds(fs))
		}
		for _, f := range fs {
			if f.Rule != RuleFiles {
				t.Errorf("%s: unexpected non-P3 finding %v", c.name, f)
			}
		}
	}
	for _, ok := range []string{"main.go", "docs/a.md", "Makefile", ".gitignore", "spec/x.yaml", "go.sum"} {
		if fs := Scan(ok, []byte("plain text\n")); len(fs) != 0 {
			t.Errorf("%s: unexpected findings %v", ok, fs)
		}
	}
	// The directory never changes the verdict.
	if !has(Scan("docs/design/tool", []byte("\x7fELF")), "binary") {
		t.Error("binary accepted under docs/")
	}
}

func TestPrivguardRedact(t *testing.T) {
	in := strings.Join([]string{
		"run in " + homePath + " as " + email,
		"token " + ghToken + " at " + ip + ":99",
		"noreply user@users.noreply.github.com stays",
		"file util.go:42 stays",
	}, "\n")
	out := Redact(in)
	for _, leak := range []string{homePath, email, ghToken, ip, "alice"} {
		if strings.Contains(out, leak) {
			t.Errorf("redacted text still holds %q:\n%s", leak, out)
		}
	}
	for _, keep := range []string{"user@users.noreply.github.com stays", "file util.go:42 stays", "[redacted:abs-path]", "[redacted:email]", "[redacted:token]", "[redacted:ipv4]"} {
		if !strings.Contains(out, keep) {
			t.Errorf("redacted text lacks %q:\n%s", keep, out)
		}
	}
	if Redact(out) != out {
		t.Error("Redact is not idempotent")
	}
	if got := Redact("nothing to hide\n\n"); got != "nothing to hide\n\n" {
		t.Errorf("clean text changed: %q", got)
	}
	// Markers do not protect text from redaction.
	if got := Redact(email + " privguard:allow rule-definition"); strings.Contains(got, "corp") {
		t.Errorf("marker protected text from Redact: %q", got)
	}
}

func TestPrivguardOwnerConfig(t *testing.T) {
	sc, err := New(Config{Literals: []string{"zed42", " "}, ExtraPatterns: []string{`\bACC-\d{6}\b`}})
	if err != nil {
		t.Fatal(err)
	}
	text := "login ZED42 on box\nacct " + j("ACC", "-123456") + "\nzed420 is another word\n"
	fs := sc.Scan("x.md", []byte(text))
	if len(fs) != 2 || fs[0].Kind != "owner-literal" || fs[0].Line != 1 || fs[1].Kind != "project-pattern" {
		t.Fatalf("findings: %v", fs)
	}
	if got := sc.Redact(text); strings.Contains(got, "ZED42") || strings.Contains(got, "123456") || !strings.Contains(got, "zed420") {
		t.Errorf("redact: %q", got)
	}
	if got := Scan("x.md", []byte(text)); len(got) != 0 {
		t.Errorf("default scanner knows owner values: %v", got)
	}
	if _, err := New(Config{ExtraPatterns: []string{"("}}); err == nil {
		t.Error("bad pattern accepted")
	}
}

func TestParseAllowReasons(t *testing.T) {
	ids, err := ParseAllowReasons([]byte("reasons:\n  - id: a\n    why: because\n  - id: b\n    why: also\n"))
	if err != nil || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := ParseAllowReasons([]byte("reasons:\n  - id: a\n")); err == nil {
		t.Error("reason without why accepted")
	}
}
