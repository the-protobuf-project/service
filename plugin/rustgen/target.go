// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package rustgen emits tonic service implementations over typed async repositories.
package rustgen

import (
	"embed"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/service/plugin/generator"
	"github.com/the-protobuf-project/service/plugin/servicev1"
	"github.com/the-protobuf-project/service/plugin/spec"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

const TargetName = "rustservice"

// PrepareRequest supplies protogen's internal Go import mappings for Rust-only
// schemas. It does not affect the Rust paths, which come from proto packages.
func PrepareRequest(request *pluginpb.CodeGeneratorRequest) {
	for _, f := range request.ProtoFile {
		if f.GetOptions().GetGoPackage() != "" {
			continue
		}
		if f.Options == nil {
			f.Options = &descriptorpb.FileOptions{}
		}
		pkg := strings.ReplaceAll(f.GetPackage(), ".", "/")
		if pkg == "" {
			pkg = "root"
		}
		f.Options.GoPackage = proto.String("service.invalid/proto/" + pkg)
	}
}

//go:embed templates/*.rs
var templates embed.FS

// Settings controls where generated Rust modules resolve their dependencies.
type Settings struct {
	// ProtoModule contains the prost messages and tonic bindings.
	ProtoModule string
	// ServiceModule contains this target's generated root mod.rs.
	ServiceModule string
	// ErrorDomain overrides the service's google.api.default_host.
	ErrorDomain string
}
type Target struct {
	Settings Settings
	Versions generator.Versions
}

// Plugin registers the Rust target with the same schema readers as Go.
func Plugin(s Settings, cfg *generator.Config, v generator.Versions) protokit.Plugin {
	return protokit.Plugin{Registry: map[string]schema.Target{TargetName: &Target{Settings: s, Versions: v}}, Readers: servicev1.Readers(), Layout: cfg.Layout()}
}
func (*Target) Name() string { return TargetName }
func (*Target) Generate(*protogen.Plugin, []*schema.Database) error {
	return fmt.Errorf("service: Rust generation requires the full IR")
}
func (t *Target) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	if t.Settings.ProtoModule == "" {
		t.Settings.ProtoModule = "crate::pb"
	}
	if t.Settings.ServiceModule == "" {
		t.Settings.ServiceModule = "crate::service"
	}
	for _, v := range []string{t.Settings.ProtoModule, t.Settings.ServiceModule} {
		if !validModule(v) {
			return fmt.Errorf("service: invalid Rust module path %q", v)
		}
	}
	model, err := spec.Build(p, ir, spec.Options{RepositoryModule: "rust", PackageSuffix: "svc", ErrorDomain: t.Settings.ErrorDomain})
	if err != nil {
		return err
	}
	for _, w := range model.Warnings {
		fmt.Fprintln(os.Stderr, "protoc-gen-service: warning:", w)
	}
	if len(model.Packages) == 0 {
		return nil
	}
	for _, file := range []struct{ template, output, note string }{
		{"servicex.rs", "servicex/mod.rs", "Shared runtime of the generated AIP standard-method servers."},
		{"feeds.rs", "servicex/feeds.rs", "Shared change-feed runtime."},
		{"adapters.rs", "servicex/adapters.rs", "Shared event and idempotency record adapters."},
	} {
		data, err := templates.ReadFile("templates/" + file.template)
		if err != nil {
			return err
		}
		if err := t.emit(p, file.output, nil, file.note, string(data)); err != nil {
			return err
		}
	}
	tree := &moduleNode{children: map[string]*moduleNode{}}
	for _, pkg := range model.Packages {
		if pkg.ProtoPackage == "" {
			return fmt.Errorf("service: Rust services require a proto package")
		}
		n := tree
		for _, part := range strings.Split(pkg.ProtoPackage, ".") {
			part = ident(part)
			if n.children[part] == nil {
				n.children[part] = &moduleNode{children: map[string]*moduleNode{}}
			}
			n = n.children[part]
		}
		parts := strings.Split(pkg.ProtoPackage, ".")
		for i, part := range parts {
			parts[i] = strings.TrimPrefix(ident(part), "r#")
		}
		dir := path.Join(parts...)
		sources := generator.PackageSources(pkg)
		emit := func(name, note string, body string, sources []string) error {
			if n.children[name] != nil {
				return fmt.Errorf("service: Rust module %s conflicts with a proto package", name)
			}
			n.exports = append(n.exports, name)
			imports := "use " + t.Settings.ServiceModule + "::servicex;\n"
			protoPath := t.Settings.ProtoModule
			for _, part := range strings.Split(pkg.ProtoPackage, ".") {
				protoPath += "::" + ident(part)
			}
			if strings.Contains(body, protoPath+"::") {
				body = strings.ReplaceAll(body, protoPath+"::", "pb::")
				imports += "use " + protoPath + " as pb;\n"
			}
			return t.emit(p, path.Join(dir, strings.TrimPrefix(name, "r#")+".rs"), sources, note, imports+"\n"+body)
		}
		for _, svc := range pkg.Services {
			var out strings.Builder
			if err := t.service(&out, svc); err != nil {
				return err
			}
			if err := emit(ident(svc.GoName), "AIP standard-method server for "+svc.FullName+".", out.String(), []string{svc.Proto.Location.SourceFile}); err != nil {
				return err
			}
		}
		for _, section := range []struct {
			name, note string
			render     func(*strings.Builder) error
		}{
			{"errors", "Error reasons of package " + pkg.ProtoPackage + " (AIP-193).", func(b *strings.Builder) error { t.errors(b, pkg); return nil }},
			{"feeds", "Change feeds of package " + pkg.ProtoPackage + "'s resources.", func(b *strings.Builder) error { t.feeds(b, pkg); return nil }},
			{"adapters", "Store adapters over package " + pkg.ProtoPackage + "'s repositories.", func(b *strings.Builder) error { return t.adapters(b, pkg) }},
		} {
			var out strings.Builder
			if err := section.render(&out); err != nil {
				return err
			}
			if out.Len() != 0 {
				if err := emit(section.name, section.note, out.String(), sources); err != nil {
					return err
				}
			}
		}
	}
	return t.modules(p, tree, "")
}

type moduleNode struct {
	children map[string]*moduleNode
	exports  []string
}

func (t *Target) modules(p *protogen.Plugin, n *moduleNode, dir string) error {
	var out strings.Builder
	if dir == "" {
		out.WriteString("pub mod servicex;\n\n")
	}
	sort.Strings(n.exports)
	seen := map[string]bool{}
	for _, name := range n.exports {
		if seen[name] || n.children[name] != nil {
			return fmt.Errorf("service: duplicate Rust module %s/%s", dir, name)
		}
		seen[name] = true
		fmt.Fprintf(&out, "mod %s;\npub use %s::*;\n\n", name, name)
	}
	keys := make([]string, 0, len(n.children))
	for name := range n.children {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if dir == "" && name == "servicex" {
			return fmt.Errorf("service: proto package conflicts with Rust servicex module")
		}
		fmt.Fprintf(&out, "pub mod %s;\n", name)
		if err := t.modules(p, n.children[name], path.Join(dir, strings.TrimPrefix(name, "r#"))); err != nil {
			return err
		}
	}
	return t.emit(p, path.Join(dir, "mod.rs"), nil, "Generated service module exports.", out.String())
}
