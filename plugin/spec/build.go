// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"

	"github.com/the-protobuf-project/service/plugin/servicev1"
)

// Bindings are the Go package path templates of a proto package's gRPC,
// gateway and MCP bindings. A template may use {import_path} (the messages'
// Go import path), {package} (their Go package name) and {proto_dir} (the
// directory of the proto file declaring the service).
type Bindings struct {
	Grpc    string
	Gateway string
	MCP     string
}

// Options steer a build.
type Options struct {
	// RepositoryModule is the import path of protoc-gen-store's repository
	// output; a resource's repository is <RepositoryModule>/<database>/<schema>.
	RepositoryModule string
	// PackageSuffix names each generated package: <Go package name><suffix>.
	PackageSuffix string
	// BindingsFor returns the binding templates for a proto package.
	BindingsFor func(protoPackage string) Bindings
	// Gateway and MCP enable the REST and MCP registrations.
	Gateway, MCP bool
	// ErrorDomain overrides every service's default ErrorInfo domain.
	ErrorDomain string
}

// repoRef is where protoc-gen-store put one resource's repository.
type repoRef struct {
	pkg       GoPkg
	localName string
}

// Build classifies every service of every file in the request.
func Build(p *protogen.Plugin, ir *protokit.IR, o Options) (*Model, error) {
	repos := repositories(ir, o.RepositoryModule)
	b := &builder{opts: o, repos: repos, resources: map[protoreflect.FullName]*Resource{}, model: &Model{}}
	for _, f := range p.Files {
		for _, m := range f.Messages {
			b.index(m)
		}
	}

	pkgs := map[string]*Package{}
	pkgFor := func(f *protogen.File) *Package {
		pp := string(f.Desc.Package())
		pkg := pkgs[pp]
		if pkg == nil {
			goPkg := string(f.GoPackageName) + o.PackageSuffix
			pkg = &Package{
				ProtoPackage: pp,
				Dir:          path.Join(strings.ReplaceAll(pp, ".", "/"), goPkg),
				GoPackage:    goPkg,
			}
			pkgs[pp] = pkg
		}
		return pkg
	}
	for _, f := range p.Files {
		if !f.Generate {
			continue
		}
		for _, svc := range f.Services {
			if so := servicev1.Service(svc.Desc); so.GetSkip() {
				continue
			}
			s, err := b.service(f, svc)
			if err != nil {
				return nil, err
			}
			pkg := pkgFor(f)
			pkg.Services = append(pkg.Services, s)
		}
		if err := b.extras(f, pkgFor); err != nil {
			return nil, err
		}
	}
	for _, pkg := range pkgs {
		sort.Slice(pkg.Services, func(i, j int) bool { return pkg.Services[i].GoName < pkg.Services[j].GoName })
		b.model.Packages = append(b.model.Packages, pkg)
	}
	sort.Slice(b.model.Packages, func(i, j int) bool {
		return b.model.Packages[i].ProtoPackage < b.model.Packages[j].ProtoPackage
	})
	return b.model, nil
}

// repositories indexes the IR's repository resources by message, with the
// rule protoc-gen-store's repository target applies: a table built from a
// message (not a value object), with a synthesized key and a supported
// pattern.
func repositories(ir *protokit.IR, module string) map[protoreflect.FullName]repoRef {
	out := map[protoreflect.FullName]repoRef{}
	if ir == nil {
		return out
	}
	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				if t.Source == nil || t.ValueObject || !hasGeneratedKey(t.Columns) {
					continue
				}
				pkg := naming.GoPackage(s.Name)
				out[t.Source.FullName()] = repoRef{
					pkg:       GoPkg{Path: path.Join(module, db.Name, pkg), Name: pkg},
					localName: t.LocalName,
				}
			}
		}
	}
	return out
}

type builder struct {
	opts      Options
	repos     map[protoreflect.FullName]repoRef
	resources map[protoreflect.FullName]*Resource
	byMsg     map[protoreflect.FullName]*protogen.Message
	model     *Model
}

func (b *builder) index(m *protogen.Message) {
	if b.byMsg == nil {
		b.byMsg = map[protoreflect.FullName]*protogen.Message{}
	}
	b.byMsg[m.Desc.FullName()] = m
	for _, n := range m.Messages {
		b.index(n)
	}
}

func (b *builder) warn(format string, args ...any) {
	b.model.Warnings = append(b.model.Warnings, fmt.Sprintf(format, args...))
}

func (b *builder) service(f *protogen.File, svc *protogen.Service) (*Service, error) {
	bind := Bindings{}
	if b.opts.BindingsFor != nil {
		bind = b.opts.BindingsFor(string(f.Desc.Package()))
	}
	expand := func(tpl string) GoPkg {
		if tpl == "" || tpl == "{import_path}" {
			return GoPkg{Path: string(f.GoImportPath), Name: string(f.GoPackageName)}
		}
		p := strings.NewReplacer(
			"{import_path}", string(f.GoImportPath),
			"{package}", string(f.GoPackageName),
			"{proto_dir}", path.Dir(f.Desc.Path()),
		).Replace(tpl)
		return GoPkg{Path: p, Name: path.Base(p)}
	}
	s := &Service{
		Proto:    svc,
		GoName:   svc.GoName,
		FullName: string(svc.Desc.FullName()),
		Grpc:     expand(bind.Grpc),
	}
	if b.opts.Gateway && hasHTTP(svc) {
		gw := expand(bind.Gateway)
		s.Gateway = &gw
	}
	if b.opts.MCP {
		m := expand(bind.MCP)
		s.MCP = &m
	}
	host := defaultHost(svc.Desc)
	switch {
	case b.opts.ErrorDomain != "":
		s.Domain = b.opts.ErrorDomain
	case host != "":
		s.Domain = host
	default:
		s.Domain = string(f.Desc.Package())
	}
	if host != "" {
		s.EventSource = "//" + host
	} else {
		s.EventSource = "//" + string(f.Desc.Package())
	}

	seen := map[*Resource]bool{}
	for _, m := range svc.Methods {
		meth := b.method(svc, m)
		s.Methods = append(s.Methods, meth)
		if r := meth.Resource; r != nil && !seen[r] {
			seen[r] = true
			s.Resources = append(s.Resources, r)
		}
	}
	return s, nil
}

func hasHTTP(svc *protogen.Service) bool {
	for _, m := range svc.Methods {
		if proto.HasExtension(m.Desc.Options(), annotations.E_Http) {
			return true
		}
	}
	return false
}

func defaultHost(d protoreflect.ServiceDescriptor) string {
	if !proto.HasExtension(d.Options(), annotations.E_DefaultHost) {
		return ""
	}
	h, _ := proto.GetExtension(d.Options(), annotations.E_DefaultHost).(string)
	return h
}

// resource resolves m to a Resource, or explains why it cannot be one.
func (b *builder) resource(m *protogen.Message) (*Resource, string) {
	if m == nil {
		return nil, "no resource message"
	}
	if r, ok := b.resources[m.Desc.FullName()]; ok {
		return r, ""
	}
	rd, _ := proto.GetExtension(m.Desc.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
	if rd == nil || len(rd.GetPattern()) == 0 {
		return nil, fmt.Sprintf("%s has no google.api.resource pattern", m.Desc.FullName())
	}
	segs, ok := parsePattern(rd.GetPattern()[0])
	if !ok {
		return nil, fmt.Sprintf("%s's pattern %q is not collection/{var} pairs (singletons stay hand-written)", m.Desc.FullName(), rd.GetPattern()[0])
	}
	ref, ok := b.repos[m.Desc.FullName()]
	if !ok {
		return nil, fmt.Sprintf("protoc-gen-store generates no repository for %s (is its proto in this run, and does it carry an id?)", m.Desc.FullName())
	}
	if fieldNamed(m, "name") == nil {
		return nil, fmt.Sprintf("%s has no name field", m.Desc.FullName())
	}
	singular := rd.GetSingular()
	if singular == "" {
		singular = naming.SnakeCase(string(m.Desc.Name()))
	} else {
		singular = naming.SnakeCase(singular)
	}
	r := &Resource{
		Message:       m,
		Type:          rd.GetType(),
		Pattern:       rd.GetPattern()[0],
		Segments:      segs,
		Singular:      singular,
		Repository:    ref.pkg,
		RepoInterface: ref.localName + "Repository",
		Parented:      len(segs) > 1,
		HasEtag:       isString(fieldNamed(m, "etag")),
	}
	for _, f := range m.Fields {
		name := string(f.Desc.Name())
		switch {
		case behavior(f, annotations.FieldBehavior_OUTPUT_ONLY):
			r.OutputOnly = append(r.OutputOnly, name)
		case behavior(f, annotations.FieldBehavior_REQUIRED):
			r.Required = append(r.Required, name)
		}
		if behavior(f, annotations.FieldBehavior_IMMUTABLE) {
			r.Immutable = append(r.Immutable, name)
		}
		if name == "delete_time" && f.Message != nil &&
			f.Message.Desc.FullName() == "google.protobuf.Timestamp" &&
			behavior(f, annotations.FieldBehavior_OUTPUT_ONLY) {
			r.SoftDelete = true
		}
	}
	r.Events = events(m)
	if e := r.Events; e != nil {
		e.Feed = feedMask(m, e)
		for _, seg := range segs {
			if e.ScopeVar != "" && seg.Var == e.ScopeVar {
				e.ScopeCollection = seg.Collection
			}
		}
		if e.ScopeVar != "" && e.ScopeCollection == "" {
			b.warn("%s: (service.v1.event).scope %q is not a variable of its pattern %q; its events have no scope", m.Desc.FullName(), e.ScopeVar, segs)
		}
	}
	b.resources[m.Desc.FullName()] = r
	return r, ""
}

func parsePattern(pattern string) ([]Segment, bool) {
	parts := strings.Split(pattern, "/")
	if len(parts)%2 != 0 {
		return nil, false
	}
	var segs []Segment
	for i := 0; i < len(parts); i += 2 {
		v := parts[i+1]
		if parts[i] == "" || !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") || strings.ContainsAny(v[1:len(v)-1], "{}=*") {
			return nil, false
		}
		segs = append(segs, Segment{Collection: parts[i], Var: v[1 : len(v)-1]})
	}
	return segs, true
}

// hasGeneratedKey reports a synthesized surrogate key, the store's condition
// for a repository.
func hasGeneratedKey(cols []*schema.Column) bool {
	for _, c := range cols {
		if c.Generated != "" {
			return true
		}
	}
	return false
}

func fieldNamed(m *protogen.Message, name string) *protogen.Field {
	if m == nil {
		return nil
	}
	for _, f := range m.Fields {
		if string(f.Desc.Name()) == name {
			return f
		}
	}
	return nil
}

func isString(f *protogen.Field) bool {
	return f != nil && f.Desc.Kind() == protoreflect.StringKind && !f.Desc.IsList()
}

func isBool(f *protogen.Field) bool {
	return f != nil && f.Desc.Kind() == protoreflect.BoolKind && !f.Desc.IsList()
}

func isInt32(f *protogen.Field) bool {
	return f != nil && f.Desc.Kind() == protoreflect.Int32Kind && !f.Desc.IsList()
}

func isMessage(f *protogen.Field, full protoreflect.FullName) bool {
	return f != nil && f.Message != nil && !f.Desc.IsList() && f.Message.Desc.FullName() == full
}

func behavior(f *protogen.Field, want annotations.FieldBehavior) bool {
	bs, _ := proto.GetExtension(f.Desc.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, b := range bs {
		if b == want {
			return true
		}
	}
	return false
}
