// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package rustgen emits tonic service implementations over typed async repositories.
package rustgen

import (
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

// Settings controls where generated Rust modules resolve their dependencies.
type Settings struct {
	// ProtoModule contains the prost messages and tonic bindings.
	ProtoModule string
	// ServiceModule contains this target's generated root mod.rs.
	ServiceModule string
	// ServicexModule is the runtime the generated services build on,
	// runtime-rs's grpc::servicex by default (tpp-grpc, depended on as grpc,
	// with default-features = false; servicex-store for store=true). The
	// generated root re-exports it as servicex, so crate::service::servicex
	// paths keep working.
	ServicexModule string
	// ErrorDomain overrides the service's google.api.default_host.
	ErrorDomain string
	// App, when set, also renders the application shell of that name under
	// app/ (see app.go).
	App string
	// Store generates repository-backed servers: every standard method's
	// handler is filled from a per-service struct of runtime-rs
	// database::repository::Repository (<S>Repositories), and the app shell
	// opens the records database and builds them (store.go).
	Store bool
	// Cache, with Store, has the app shell wrap each package's repositories
	// with protoc-gen-cache's cache_repositories.
	Cache bool
	// RepositoryModule contains protoc-gen-store's lang=rust repositories:
	// per proto package, Repositories and repositories(db, pool), and, for a
	// package with cache.v1 annotations, protoc-gen-cache's cache module
	// (cache::cache_repositories) beside them.
	RepositoryModule string
	// Database is the SQL dialect the app shell opens with Store: postgres,
	// mysql, sqlite or d1 (Cloudflare D1, over its REST API rather than a
	// sqlx pool).
	Database string
	// CDN has the app shell open a Cloudflare CDN purger from [cdn]
	// (app::open_cdn), over runtime-rs's cache::cdn. Opt-in, unlike Go's,
	// because tpp-cache's cdn feature links a network client.
	CDN bool
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
	if t.Settings.ServicexModule == "" {
		t.Settings.ServicexModule = "grpc::servicex"
	}
	if t.Settings.RepositoryModule == "" {
		t.Settings.RepositoryModule = "crate::repository"
	}
	if t.Settings.Database == "" {
		t.Settings.Database = "postgres"
	}
	if t.Settings.Cache && !t.Settings.Store {
		return generator.ErrCacheWithoutStore
	}
	if t.Settings.Store && sqlxPool[t.Settings.Database] == "" && t.Settings.Database != "d1" {
		return fmt.Errorf("service: rust_database=%s: want postgres, mysql, sqlite or d1", t.Settings.Database)
	}
	for _, v := range []string{t.Settings.ProtoModule, t.Settings.ServiceModule, t.Settings.ServicexModule, t.Settings.RepositoryModule} {
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
	if t.Settings.App != "" {
		if tree.children["app"] != nil {
			return fmt.Errorf("service: app=%s: a proto package named app conflicts with the Rust app module", t.Settings.App)
		}
		if err := t.renderApp(p, model); err != nil {
			return err
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
		// The runtime the services build on, under the name they use for it.
		fmt.Fprintf(&out, "pub use %s as servicex;\n\n", t.Settings.ServicexModule)
		if t.Settings.App != "" {
			out.WriteString("pub mod app;\n\n")
		}
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
