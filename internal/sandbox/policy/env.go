package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reserved names the caller may not use for a credential.
func reserved() map[string]bool {
	return map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "TERM": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOFLAGS": true,
		"CLAUDE_TMPDIR": true, "CLAUDE_CODE_TMPDIR": true, "TMPPREFIX": true,
	}
}

func checkCreds(in Inputs) error {
	seen := map[string]bool{}
	add := func(kind, name, value string) error {
		if !envNameRE.MatchString(name) || reserved()[name] {
			return fmt.Errorf("policy: invalid %s name %q", kind, name)
		}
		if seen[name] {
			return fmt.Errorf("policy: duplicate credential %q", name)
		}
		if value == "" || strings.ContainsRune(value, 0) {
			return fmt.Errorf("policy: invalid value for %s %q", kind, name)
		}
		seen[name] = true
		return nil
	}
	if in.Model != nil {
		if err := add("model token", in.Model.Name, in.Model.Value); err != nil {
			return err
		}
	}
	for _, c := range in.TestCreds {
		if err := add("test credential", c.Name, c.Value); err != nil {
			return err
		}
	}
	return nil
}

// AllowedNames returns the sorted names of every variable Env can emit
// (fixed allowlist plus the supplied credentials).
func (p Policy) AllowedNames() []string {
	set := map[string]bool{}
	for _, kv := range p.Env() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set[kv[:i]] = true
		}
	}
	return sortedKeys(set)
}

// Env builds the session environment from scratch: an explicit allowlist plus
// the C-08 credentials given to New. It never consults os.Environ.
func (p Policy) Env() []string {
	in := p.in
	lang := in.Lang
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	m := map[string]string{
		"PATH":          in.Path,
		"HOME":          p.Home(),
		"TMPDIR":        in.RunTmp,
		"CLAUDE_TMPDIR": in.RunTmp,
		// Claude Code 2.1.332 reads CLAUDE_CODE_TMPDIR for its per-session temp
		// dir; without it the Bash tool fails in srt (EPERM under the system temp dir).
		"CLAUDE_CODE_TMPDIR": in.RunTmp,
		"TMPPREFIX":          in.RunTmp + "/zsh",
		"LANG":               lang,
		"TERM":               "dumb",
		"GOCACHE":            p.GoCache(),
		"GOMODCACHE":         p.GoModCache(),
	}
	if in.GoFlags != "" {
		m["GOFLAGS"] = in.GoFlags
	}
	if in.Model != nil {
		m[in.Model.Name] = in.Model.Value
	}
	for _, c := range in.TestCreds {
		m[c.Name] = c.Value
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}
