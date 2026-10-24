// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator_test

// golden_test.go runs the goservice target over every case under
// testdata/cases and compares its output, byte for byte, with the committed
// golden tree. Three cases compile the real example protos (their "source"
// file points there), so the goldens cannot drift from what the examples
// generate; "purge" holds the shapes the examples cannot, being held to
// api-linter. Regenerate with: go test ./plugin/generator -run TestGolden -update

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto/backend"
	"github.com/the-protobuf-project/store/plugin/factory/wire"

	"github.com/the-protobuf-project/service/plugin/generator"
	"github.com/the-protobuf-project/service/plugin/spec"
)

const (
	goModule         = "example.com/gen/service"
	repositoryModule = "example.com/gen/repository"
	messagesModule   = "example.com/test/gen"
)

var testSettings = generator.Settings{
	GoModule:         goModule,
	RepositoryModule: repositoryModule,
	PackageSuffix:    "svc",
	Bindings:         spec.Bindings{Grpc: "{import_path}", Gateway: "{import_path}", MCP: "{import_path}"},
	Gateway:          true,
	MCP:              true,
}

var testVersions = generator.Versions{Plugin: "test", Entity: "test", Engine: "test"}

func TestMain(m *testing.M) {
	header.SetTool("protoc-gen-service")
	os.Exit(m.Run())
}

func casePlugin(string) protokit.Plugin { return generator.Plugin(testSettings, nil, testVersions) }

func TestGolden(t *testing.T) {
	for _, dir := range cases(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			golden.RunPluginCase(t, dir, []string{generator.TargetName}, casePlugin)
		})
	}
}

func TestDeterminism(t *testing.T) {
	for _, dir := range cases(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			golden.Determinism(t, dir, casePlugin(dir))
		})
	}
}

// TestIRAgreement proves this plugin derives, from the same protos, the same
// databases, schemas and tables protoc-gen-store does: the generated servers
// import the store's repository packages by those names.
func TestIRAgreement(t *testing.T) {
	store := wire.Plugin(backend.Readers(backend.New(nil, messagesModule, true, false, false, false)), nil)
	for _, dir := range cases(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			golden.IRAgreement(t, dir, casePlugin(dir), store)
		})
	}
}

// TestGeneratedOutputImports keeps the generated code's dependencies to the
// ones a consumer already has: its own generated trees, runtime-go, gRPC,
// protobuf, and the standard library.
func TestGeneratedOutputImports(t *testing.T) {
	allowed := []string{
		goModule + "/", repositoryModule + "/", messagesModule,
		"github.com/the-protobuf-project/runtime-go/",
		"google.golang.org/grpc/", "google.golang.org/protobuf/", "google.golang.org/genproto/googleapis/rpc/",
	}
	for _, dir := range cases(t) {
		root := filepath.Join(dir, "golden", generator.TargetName)
		var n int
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			n++
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range f.Imports {
				if spec.Name != nil {
					t.Errorf("%s aliases %s: generated code never aliases an import", path, spec.Path.Value)
				}
				imp, _ := strconv.Unquote(spec.Path.Value)
				if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
					continue
				}
				ok := false
				for _, a := range allowed {
					ok = ok || strings.HasPrefix(imp, a) || imp == strings.TrimSuffix(a, "/")
				}
				if !ok {
					t.Errorf("%s imports %q, which is not among the generated code's allowed dependencies", path, imp)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("%s: no generated Go files; the check would pass vacuously", root)
		}
	}
}

// TestWarnings checks that the shapes the generator cannot serve are
// reported and left custom, never guessed at.
func TestWarnings(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "purge"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	pl := casePlugin("")
	ir, err := protokit.Build(p, protokit.Options{}, pl.Readers, pl.Layout)
	if err != nil {
		t.Fatal(err)
	}
	model, err := spec.Build(p, ir, spec.Options{RepositoryModule: repositoryModule, PackageSuffix: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]spec.Kind{}
	for _, pkg := range model.Packages {
		for _, s := range pkg.Services {
			if s.GoName == "Skipped" {
				t.Error("a skipped service was generated")
			}
			for _, m := range s.Methods {
				kinds[m.Proto.GoName] = m.Kind
			}
		}
	}
	want := map[string]spec.Kind{
		"GetDocument": spec.Get, "DeleteDocument": spec.Delete, "PurgeDocument": spec.Purge,
		"GetDocumentByHand": spec.Custom, "UndeleteDocument": spec.Custom,
		"ListPages": spec.Custom, "GetSettings": spec.Custom,
	}
	for m, k := range want {
		if kinds[m] != k {
			t.Errorf("%s classified %s, want %s", m, kinds[m], k)
		}
	}
	joined := strings.Join(model.Warnings, "\n")
	for _, w := range []string{"ListPages", "GetSettings"} {
		if !strings.Contains(joined, w) {
			t.Errorf("no warning names %s; warnings:\n%s", w, joined)
		}
	}
}

func cases(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join("testdata", "cases", e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatal("no cases")
	}
	return out
}
