package privguard

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The patterns below describe forbidden shapes. They are assembled from
// pieces so that this file does not itself contain what it looks for.

func join(parts ...string) string { return strings.Join(parts, "") }

func builtinRules() []rule {
	pathPrefixes := join(`/Us`, `ers/|/ho`, `me/|/Vol`, `umes/|/priv`, `ate/tmp`)
	winPath := join(`[A-Za-z]:\\Us`, `ers\\`)
	// A prefix only counts at the start of a token, so URLs like
	// https://host/x/home/y are not paths.
	pathRe := `(?:^|[^\w.~-])((?:` + pathPrefixes + `|` + winPath + `)[^\s"'` + "`" + `<>)\]]*)`
	ipv4 := `(?:\d{1,3}\.){3}\d{1,3}`
	pemBegin := join(`-----BEG`, `IN [A-Z ]*PRIV`, `ATE KEY-----`)

	return []rule{
		// P1: secrets by known prefix. The inline marker never exempts these.
		{rule: RuleSecrets, kind: "token", secret: true, re: regexp.MustCompile(
			`\bgh[pousr]_[A-Za-z0-9]{30,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bsk-[A-Za-z0-9_-]{20,}` +
				`|\bAKIA[0-9A-Z]{16}\b|\bxox[abprs]-[A-Za-z0-9-]{10,}|\bAIza[0-9A-Za-z_-]{35}` +
				`|\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}` +
				`|\b\d{8,10}:[A-Za-z0-9_-]{35}\b|\bBearer [A-Za-z0-9._~+/-]{20,}`)},
		{rule: RuleSecrets, kind: "private-key", secret: true, re: regexp.MustCompile(pemBegin)},
		{rule: RuleSecrets, kind: "url-credentials", secret: true, re: regexp.MustCompile(
			`://[^/\s:@]+:([^/\s@]+)@`)},

		// P2: local identity.
		{rule: RuleIdentity, kind: "abs-path", re: regexp.MustCompile(pathRe)},
		{rule: RuleIdentity, kind: "email", re: regexp.MustCompile(
			`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), keep: notPlaceholderEmail},
		{rule: RuleIdentity, kind: "ipv4", re: regexp.MustCompile(
			`(?:^|[^\w.])(` + ipv4 + `(?::\d{1,5})?)(?:$|[^\w])`), keep: validIPv4},
		{rule: RuleIdentity, kind: "host-port", re: regexp.MustCompile(
			`(?:^|[^\w.-])((?:localhost|[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}):\d{2,5})\b`),
			keep: notFileLine},
		{rule: RuleIdentity, kind: "chat-id", re: regexp.MustCompile(`(?:^|[^\w])(-100\d{9,})\b`)},
		{rule: RuleIdentity, kind: "long-hex", re: regexp.MustCompile(`\b[0-9A-Fa-f]{32,}\b`), keep: notRepeated},
		{rule: RuleIdentity, kind: "long-base64", re: regexp.MustCompile(
			`(?:^|[^A-Za-z0-9+/_=-])([A-Za-z0-9+/_-]{40,}={0,2})`), keep: looksRandom},
	}
}

// notNoreply keeps an email match that is not a noreply address: a noreply
// local part, or a domain with a "noreply" label (users.noreply.github.com).
func notNoreply(line string, s, e int) bool {
	addr := strings.ToLower(line[s:e])
	at := strings.LastIndexByte(addr, '@')
	local, domain := addr[:at], addr[at+1:]
	if local == "noreply" || local == "no-reply" || local == "donotreply" {
		return false
	}
	return !slices.Contains(strings.Split(domain, "."), "noreply")
}

// notPlaceholderEmail is the P2 email filter: noreply addresses and addresses
// in the domains RFC 2606 reserves for examples cannot identify anyone.
func notPlaceholderEmail(line string, s, e int) bool {
	if !notNoreply(line, s, e) {
		return false
	}
	domain := strings.ToLower(line[s:e])
	domain = domain[strings.LastIndexByte(domain, '@')+1:]
	for _, d := range []string{"example.com", "example.net", "example.org"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return false
		}
	}
	for _, tld := range []string{".invalid", ".example", ".test", ".localhost"} {
		if strings.HasSuffix(domain, tld) {
			return false
		}
	}
	return true
}

// validIPv4 keeps well-formed addresses; loopback (127.0.0.0/8) is not
// identifying and is dropped.
func validIPv4(line string, s, e int) bool {
	if e+1 < len(line) && line[e] == '.' && line[e+1] >= '0' && line[e+1] <= '9' {
		return false // part of a longer dotted number, e.g. a version
	}
	host, _, _ := strings.Cut(line[s:e], ":")
	octets := strings.Split(host, ".")
	for _, o := range octets {
		n, err := strconv.Atoi(o)
		if err != nil || n > 255 || (len(o) > 1 && o[0] == '0') {
			return false
		}
	}
	return octets[0] != "127"
}

var fileExts = map[string]bool{"go": true, "md": true, "yaml": true, "yml": true, "json": true,
	"jsonl": true, "sql": true, "txt": true, "rb": true, "mod": true, "sum": true, "toml": true,
	"sh": true, "html": true, "css": true, "js": true, "ts": true}

// notFileLine rejects "name.go:12" style file:line references.
func notFileLine(line string, s, e int) bool {
	host, _, _ := strings.Cut(line[s:e], ":")
	i := strings.LastIndexByte(host, '.')
	return i < 0 || !fileExts[strings.ToLower(host[i+1:])]
}

func notRepeated(line string, s, e int) bool {
	seen := map[byte]bool{}
	for i := s; i < e; i++ {
		seen[line[i]|0x20] = true
	}
	return len(seen) > 3
}

// looksRandom accepts a long run of base64 characters that has upper, lower
// and digit characters, is not just hex, and has high entropy. Digest labels
// (h1:, sha256:) mark content hashes, which are not secrets.
func looksRandom(line string, s, e int) bool {
	tok := line[s:e]
	for _, p := range []string{"h1:", "sha256:", "sha256-", "sha512-", "sha1:"} {
		if strings.HasSuffix(line[:s], p) {
			return false
		}
	}
	var lower, upper, digit bool
	digits := 0
	for _, c := range tok {
		switch {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
			digits++
		}
	}
	if !(lower && upper && digit) || strings.Count(tok, "/") > 3 {
		return false
	}
	// Identifiers (long camel-case test names) have few digits; random text
	// has many.
	if digits < 3 {
		return false
	}
	return entropy(tok) >= 4.2
}

func entropy(s string) float64 {
	freq := map[rune]float64{}
	n := 0.0
	for _, c := range s {
		freq[c]++
		n++
	}
	h := 0.0
	for _, f := range freq {
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}
