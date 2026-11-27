// Package secrets checks and reads secret files (D-026). It is the only
// package that opens files in the secrets directory (MD-5). A value is
// returned as a Secret, which never prints itself: only the forge client and
// the harness model-token env injection call Reveal.
package secrets

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Secret is an opaque secret value. Every formatting path (fmt verbs, JSON,
// text, slog) yields "[redacted]".
type Secret struct{ v string }

// New wraps a value. Prefer the readers in this package.
func New(v string) Secret { return Secret{v: v} }

// Reveal returns the raw value. Callers: the forge client (HTTP header) and
// the harness model-token env injection only. Never log the result.
func (s Secret) Reveal() string { return s.v }

// IsEmpty reports whether the secret holds no value.
func (s Secret) IsEmpty() bool { return s.v == "" }

// Len is the value length in bytes; it does not reveal content.
func (s Secret) Len() int { return len(s.v) }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer (the %#v verb).
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter so that every verb prints the placeholder.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// MarshalText implements encoding.TextMarshaler (TOML, YAML, map keys).
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

var (
	_ fmt.Formatter          = Secret{}
	_ fmt.GoStringer         = Secret{}
	_ json.Marshaler         = Secret{}
	_ encoding.TextMarshaler = Secret{}
	_ slog.LogValuer         = Secret{}
)
