// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator_test

import (
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"

	"github.com/the-protobuf-project/service/plugin/generator"
)

// TestRefusals checks that a declaration the generator cannot honour fails
// the run with an error that says what to change, rather than generating
// something subtly wrong.
func TestRefusals(t *testing.T) {
	cases := map[string]string{
		"bad_code":           `code "TEAPOT" is not a google.rpc.Code name`,
		"no_domain":          "needs a domain",
		"partial_mask_field": `maps position to "nowhere"`,
	}
	for dir, want := range cases {
		t.Run(dir, func(t *testing.T) {
			req := golden.BuildRequest(t, filepath.Join("testdata", "errors", dir))
			p, err := protogen.Options{}.New(req)
			if err != nil {
				t.Fatal(err)
			}
			err = protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, casePlugin(dir))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want an error containing %q", err, want)
			}
		})
	}
}
