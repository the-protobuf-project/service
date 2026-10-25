// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"testing"

	"github.com/the-protobuf-project/protokit/manifest"
)

// TestManifest keeps plugin.yaml parseable and self-consistent by protokit's
// rules, so a tool composing plugins can read it.
func TestManifest(t *testing.T) {
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}
