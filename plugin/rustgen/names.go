// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"google.golang.org/protobuf/compiler/protogen"
)

func validModule(s string) bool {
	for _, part := range strings.Split(s, "::") {
		if part == "" {
			return false
		}
		for i, c := range part {
			if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}
func q(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 32 || r == 127 {
				fmt.Fprintf(&b, `\u{%x}`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
func ident(s string) string {
	s = naming.SnakeCase(s)
	switch s {
	case "self", "super", "crate":
		return s + "_"
	case "as", "async", "await", "break", "const", "continue", "dyn", "else", "enum", "extern", "false", "fn", "for", "if", "impl", "in", "let", "loop", "match", "mod", "move", "mut", "pub", "ref", "return", "static", "struct", "trait", "true", "type", "unsafe", "use", "where", "while", "abstract", "become", "box", "do", "final", "macro", "override", "priv", "typeof", "unsized", "virtual", "yield", "try", "gen":
		return "r#" + s
	}
	return s
}
func (t *Target) typ(m *protogen.Message) string {
	switch m.Desc.FullName() {
	case "google.protobuf.Empty":
		return "()"
	case "google.protobuf.DoubleValue":
		return "f64"
	case "google.protobuf.FloatValue":
		return "f32"
	case "google.protobuf.Int64Value":
		return "i64"
	case "google.protobuf.UInt64Value":
		return "u64"
	case "google.protobuf.Int32Value":
		return "i32"
	case "google.protobuf.UInt32Value":
		return "u32"
	case "google.protobuf.BoolValue":
		return "bool"
	case "google.protobuf.StringValue":
		return "String"
	case "google.protobuf.BytesValue":
		return "Vec<u8>"
	}
	if m.Desc.ParentFile().Package() == "google.protobuf" {
		return "prost_types::" + m.GoIdent.GoName
	}
	parts := strings.Split(string(m.Desc.FullName()), ".")
	pkg := strings.Split(string(m.Desc.ParentFile().Package()), ".")
	out := t.Settings.ProtoModule
	for _, s := range pkg {
		out += "::" + ident(s)
	}
	for _, s := range parts[len(pkg) : len(parts)-1] {
		out += "::" + ident(s)
	}
	return out + "::" + naming.PascalGo(parts[len(parts)-1])
}
func arr(ss []string) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = q(s)
	}
	return "&[" + strings.Join(out, ",") + "]"
}
func field(f *protogen.Field) string {
	if f == nil {
		return ""
	}
	return ident(string(f.Desc.Name()))
}
func scalar(f *protogen.Field, def string) string {
	if f == nil {
		return def
	}
	v := "req." + field(f)
	if f.Desc.HasPresence() {
		v += ".unwrap_or_default()"
	}
	return v
}
func borrowed(f *protogen.Field) string {
	if f == nil {
		return `""`
	}
	if f.Desc.HasPresence() {
		return "req." + field(f) + ".as_deref().unwrap_or_default()"
	}
	return "&req." + field(f)
}
