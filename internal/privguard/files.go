package privguard

import (
	"bytes"
	"path"
	"strings"
)

// MaxFileBytes is the P3 size limit for one file (1 MiB).
const MaxFileBytes = 1 << 20

var allowedExts = map[string]bool{
	".go": true, ".mod": true, ".sum": true, ".md": true, ".yaml": true, ".yml": true,
	".json": true, ".jsonl": true, ".sql": true, ".rb": true, ".txt": true, ".toml": true,
	".sh": true, ".html": true, ".css": true, ".js": true, ".ts": true, ".tmpl": true,
}

var allowedBases = map[string]bool{
	"Makefile": true, "LICENSE": true, "CODEOWNERS": true, "Dockerfile": true,
	".gitignore": true, ".gitattributes": true, ".editorconfig": true,
}

// forbiddenExts are rejected even if someone adds them to the allowlist: keys,
// databases, screenshots and archives.
var forbiddenExts = map[string]bool{
	".db": true, ".sqlite": true, ".sqlite3": true, ".pem": true, ".key": true, ".p12": true,
	".pfx": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".heic": true, ".ico": true, ".zip": true, ".tar": true, ".gz": true,
	".tgz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true, ".exe": true,
	".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true,
}

var magics = [][]byte{
	[]byte("\x7fELF"),
	{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf}, // Mach-O
	{0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe},
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O universal
}

// checkFile applies P3 to one file: file type, size and binary content. The
// directory part of name is ignored on purpose (no path exemptions).
func checkFile(name string, content []byte) []Finding {
	var out []Finding
	add := func(kind string) { out = append(out, Finding{Rule: RuleFiles, Kind: kind, File: name}) }
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(path.Ext(base))
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		add("forbidden-file")
	case forbiddenExts[ext]:
		add("forbidden-file")
	case !allowedExts[ext] && !allowedBases[base]:
		add("file-type-not-allowed")
	}
	if len(content) > MaxFileBytes {
		add("too-large")
	}
	if isBinary(content) {
		add("binary")
	}
	return out
}

// A PE file starts with MZ and always has NUL bytes in its header, so the NUL
// check below covers it without rejecting text that merely starts with MZ.
func isBinary(b []byte) bool {
	for _, m := range magics {
		if bytes.HasPrefix(b, m) {
			return true
		}
	}
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}
