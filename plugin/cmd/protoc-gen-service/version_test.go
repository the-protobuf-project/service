// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// TestCanonicalVersion: every version a build can report is spelled with a
// leading "v", so a banner reads the same whether a release (goreleaser's bare
// "0.2.1") or a `go install` ("v0.2.1") wrote it.
func TestCanonicalVersion(t *testing.T) {
	for in, want := range map[string]string{
		"0.2.1":        "v0.2.1",
		"v0.2.1":       "v0.2.1",
		"0.2.1+dirty":  "v0.2.1+dirty",
		"v0.2.1+dirty": "v0.2.1+dirty",
		"dev":          "dev",
		"":             "",
	} {
		if got := canonicalVersion(in); got != want {
			t.Errorf("canonicalVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReleaseVersionIsCanonical: a release's ldflags version is stamped with
// its "v".
func TestReleaseVersionIsCanonical(t *testing.T) {
	defer func(v string) { version = v }(version)
	for in, want := range map[string]string{"0.2.1": "v0.2.1", "v0.2.1": "v0.2.1", "0.2.1+dirty": "v0.2.1+dirty"} {
		version = in
		if got := resolveVersion(); got != want {
			t.Errorf("resolveVersion() with ldflags %q = %q, want %q", in, got, want)
		}
	}
}
