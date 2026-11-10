// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

import (
	"strings"
	"testing"
)

func TestFormatRust(t *testing.T) {
	source := "pub struct Server{pub name:String}\nimpl Server{pub fn new()->Self{Self{name:String::new()}}}\n"
	formatted, err := formatRust(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formatted), "    pub name: String,\n") {
		t.Fatalf("unformatted output: %s", formatted)
	}
	again, err := formatRust(string(formatted))
	if err != nil || string(again) != string(formatted) {
		t.Fatalf("formatting not stable: %v", err)
	}
}

func TestFormatRustRejectsInvalidSource(t *testing.T) {
	if _, err := formatRust("pub struct {"); err == nil {
		t.Fatal("invalid generated Rust accepted")
	}
}

func TestFormatRustRequiresFormatter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := formatRust("pub struct Server;"); err == nil || !strings.Contains(err.Error(), "rustup component add rustfmt") {
		t.Fatalf("missing actionable formatter error: %v", err)
	}
}
