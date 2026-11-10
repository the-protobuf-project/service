package rustgen_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/service/plugin/rustgen"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

var update = flag.Bool("update-rust", false, "regenerate Rust compile fixtures")

func TestRustExamples(t *testing.T) {
	req := &pluginpb.CodeGeneratorRequest{}
	seen := map[string]bool{}
	for _, name := range []string{"directory", "calendar", "booking", "webhook", "idempotency", "purge", "streaming"} {
		dir := filepath.Join("..", "generator", "testdata", "cases", name)
		if name == "streaming" {
			dir = filepath.Join("testdata", "streaming")
		}
		r := golden.BuildRequest(t, dir)
		req.FileToGenerate = append(req.FileToGenerate, r.FileToGenerate...)
		for _, f := range r.ProtoFile {
			if !seen[f.GetName()] {
				seen[f.GetName()] = true
				req.ProtoFile = append(req.ProtoFile, f)
			}
		}
	}
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = protokit.RunPlugin(p, protokit.Options{Target: rustgen.TargetName}, rustgen.Plugin(rustgen.Settings{}, nil)); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "examples", "rust")
	check := func(name string, data []byte) {
		t.Helper()
		if *update {
			if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, data, 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
		got, err := os.ReadFile(name)
		if err != nil || string(got) != string(data) {
			t.Errorf("%s differs; regenerate with go test ./plugin/rustgen -update-rust", name)
		}
	}
	for _, f := range p.Response().File {
		check(filepath.Join(root, "src", "generated", f.GetName()), []byte(f.GetContent()))
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(&descriptorpb.FileDescriptorSet{File: req.ProtoFile})
	if err != nil {
		t.Fatal(err)
	}
	check(filepath.Join(root, "descriptor.bin"), data)
}

func TestRustWithoutGoPackages(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("..", "generator", "testdata", "cases", "directory"))
	for _, f := range req.ProtoFile {
		if f.Options != nil {
			f.Options.GoPackage = nil
		}
	}
	rustgen.PrepareRequest(req)
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protokit.RunPlugin(p, protokit.Options{Target: rustgen.TargetName}, rustgen.Plugin(rustgen.Settings{}, nil)); err != nil {
		t.Fatal(err)
	}
	if len(p.Response().File) == 0 {
		t.Fatal("no Rust files generated")
	}
}

func TestRustModuleValidation(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("..", "generator", "testdata", "cases", "directory"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	err = protokit.RunPlugin(p, protokit.Options{Target: rustgen.TargetName}, rustgen.Plugin(rustgen.Settings{ProtoModule: "crate::pb;bad"}, nil))
	if err == nil {
		t.Fatal("invalid Rust module accepted")
	}
}

func TestRustDeterminism(t *testing.T) {
	for _, name := range []string{"directory", "calendar", "booking", "webhook", "idempotency", "purge"} {
		t.Run(name, func(t *testing.T) {
			golden.Determinism(t, filepath.Join("..", "generator", "testdata", "cases", name), rustgen.Plugin(rustgen.Settings{}, nil))
		})
	}
}
