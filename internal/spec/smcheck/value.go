package smcheck

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// omap is an insertion-ordered YAML mapping (ruby hashes keep order, and the
// error messages print hashes, so order is observable).
type omap struct {
	keys []string
	m    map[string]any
}

func newOmap() *omap { return &omap{m: map[string]any{}} }

func (o *omap) set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *omap) get(k string) any { return o.m[k] }

// specError is raised (via panic) for the situations where the ruby script
// raises; Check turns it into an error.
type specError string

func fail(format string, a ...any) { panic(specError(fmt.Sprintf(format, a...))) }

func parseDoc(data []byte) (*omap, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	m, ok := conv(doc.Content[0]).(*omap)
	if !ok {
		return nil, fmt.Errorf("YAML root is not a mapping")
	}
	return m, nil
}

func conv(n *yaml.Node) any {
	switch n.Kind {
	case yaml.AliasNode:
		return conv(n.Alias)
	case yaml.MappingNode:
		o := newOmap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			o.set(str(conv(n.Content[i])), conv(n.Content[i+1]))
		}
		return o
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, conv(c))
		}
		return out
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			return n.Value
		}
		return v
	}
}

func asMap(v any, what string) *omap {
	o, ok := v.(*omap)
	if !ok {
		fail("%s: expected a mapping", what)
	}
	return o
}

func asSlice(v any, what string) []any {
	s, ok := v.([]any)
	if !ok {
		fail("%s: expected a list", what)
	}
	return s
}

func maps(v any, what string) []*omap {
	var out []*omap
	for _, x := range asSlice(v, what) {
		out = append(out, asMap(x, what))
	}
	return out
}

// str renders a scalar the way a string comparison would see it; nil is "".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// strList is the ruby as_list: a list stays, a scalar becomes a one-element list.
func strList(v any) []string {
	if s, ok := v.([]any); ok {
		out := make([]string, 0, len(s))
		for _, x := range s {
			out = append(out, str(x))
		}
		return out
	}
	if v == nil {
		return []string{""}
	}
	return []string{str(v)}
}

// listOrEmpty is ruby Array(x) for nil / list / scalar.
func listOrEmpty(v any) []string {
	if v == nil {
		return nil
	}
	return strList(v)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func inspectStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '#':
			if i+1 < len(s) && strings.ContainsRune("{$@", rune(s[i+1])) {
				b.WriteByte('\\')
			}
			b.WriteByte('#')
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// inspect mimics ruby's Object#inspect (2.x/3.3 hash style, `"k"=>v`).
func inspect(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case string:
		return inspectStr(x)
	case bool:
		return strconv.FormatBool(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = inspectStr(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = inspect(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *omap:
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = inspectStr(k) + "=>" + inspect(x.m[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}
