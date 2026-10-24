// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

// view.go turns the spec into template data: qualified type names, getter
// names, Go literals and the import set. Imports are never aliased: each is
// referred to by the package name it declares, and two imports declaring the
// same name are refused with a pointer to the layout setting that separates
// them.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/spec"
)

var funcs = template.FuncMap{
	"quote": strconv.Quote,
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

// imports collects one file's imports.
type imports struct {
	byPath map[string]string // path -> declared package name
}

func newImports() *imports { return &imports{byPath: map[string]string{}} }

// use records path, declaring name, and returns the qualifier to write.
func (im *imports) use(path, name string) string {
	im.byPath[path] = name
	return name
}

func (im *imports) std(paths ...string) {
	for _, p := range paths {
		im.byPath[p] = p[strings.LastIndex(p, "/")+1:]
	}
}

// lines renders the import block, standard library first, refusing two
// imports that declare one name.
func (im *imports) lines(file string) ([]string, error) {
	byName := map[string]string{}
	var std, other []string
	for p, n := range im.byPath {
		if prev, ok := byName[n]; ok {
			return nil, fmt.Errorf("service: %s would import two packages both named %s (%s and %s), which only an "+
				"import alias could resolve, and generated code never aliases. Give one of them another name: a "+
				"datasources: rule naming the repository schema, or a bindings: template naming the gRPC, gateway or "+
				"MCP package", file, n, prev, p)
		}
		byName[n] = p
		if strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
			other = append(other, strconv.Quote(p))
		} else {
			std = append(std, strconv.Quote(p))
		}
	}
	sort.Strings(std)
	sort.Strings(other)
	if len(std) > 0 && len(other) > 0 {
		std = append(std, "")
	}
	return append(std, other...), nil
}

// qualify names a message's Go type from file f's point of view.
func (im *imports) qualify(m *protogen.Message) string {
	return im.use(string(m.GoIdent.GoImportPath), goPackageOf(m)) + "." + m.GoIdent.GoName
}

func goPackageOf(m *protogen.Message) string {
	// protogen records each message's file package name on its location's
	// file; the import path's declared name is the file's go package name.
	return pkgNames[m.GoIdent.GoImportPath]
}

// pkgNames maps every file's import path to its declared package name. It
// is filled before views are built (see indexPackages).
var pkgNames = map[protogen.GoImportPath]string{}

func indexPackages(p *protogen.Plugin) {
	for _, f := range p.Files {
		pkgNames[f.GoImportPath] = string(f.GoPackageName)
	}
}

type serverFile struct {
	Banner  string
	Package string
	Imports []string
	S       *serviceView
}

type serviceView struct {
	GoName, FullName string
	Grpc             string // qualifier of the gRPC stubs
	Domain           string
	Resources        []*resourceView
	Methods          []*methodView
	Hooks            []hookView
	Custom           []string // custom method names, for the doc comment
	EmptyQ           string   // emptypb qualifier
}

type resourceView struct {
	GoName      string // "Organization"
	PB          string // "organizationv1.Organization"
	Field       string // Repositories field, "Organizations"
	Repo        string // "organization.OrganizationRepository"
	Type        string
	Singular    string
	Var         string // unexported prefix, "organization"
	Collection  string // own collection id, "events"
	Collections string // Go literal
	Parent      string // Go literal of the parent collections
	Parented    bool
	HasEtag     bool
	Required    string // Go literal or "nil"
	OutputOnly  string
	Immutable   string
	HasImmut    bool
	// Remove is the repository call that removes a record for good: Purge
	// for a soft-deletable resource, Delete for any other.
	Remove string
	// Hooks this resource's methods call.
	HookCreate, HookUpdate, HookDelete, HookUndelete, HookParent bool
}

type hookView struct {
	Doc, Name, Params, Call string
}

type methodView struct {
	Kind   string
	GoName string
	Req    string
	Resp   string
	R      *resourceView
	// Getters, "" when the request lacks the field.
	Name, Parent, Body, BodyField, ID, IDField, UpdateMask, Etag, AllowMissing, ValidateOnly, Force string
	PageSize, PageToken, Filter, OrderBy, ShowDeleted, ReadMask                                  string
	Items, Next                                                                                  string
	ReturnsEmpty                                                                                 bool
	HasCreate                                                                                    bool // the service also creates this resource (allow_missing)
}

func goList(xs []string) string {
	if len(xs) == 0 {
		return "[]string(nil)"
	}
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

func getter(f *protogen.Field) string {
	if f == nil {
		return ""
	}
	return "Get" + f.GoName + "()"
}

func snake(s string) string { return naming.SnakeCase(s) }

func serviceFileView(pkg *spec.Package, svc *spec.Service, servicex, _ string) (*serverFile, error) {
	im := newImports()
	im.std("context")
	im.use(servicex, "servicex")
	v := &serviceView{
		GoName:      svc.GoName,
		FullName:    svc.FullName,
		Grpc:        im.use(svc.Grpc.Path, svc.Grpc.Name),
		Domain:      svc.Domain,
	}
	rv := map[*spec.Resource]*resourceView{}
	for _, r := range svc.Resources {
		x := &resourceView{
			GoName:      r.Message.GoIdent.GoName,
			PB:          im.qualify(r.Message),
			Field:       naming.PascalGo(naming.Pluralize(naming.SnakeCase(r.Message.GoIdent.GoName))),
			Repo:        im.use(r.Repository.Path, r.Repository.Name) + "." + r.RepoInterface,
			Type:        r.Type,
			Singular:    r.Singular,
			Var:         naming.CamelFirst(svc.GoName) + r.Message.GoIdent.GoName,
			Collection:  r.Collection(),
			Collections: goList(r.Collections()),
			Parent:      goList(r.ParentCollections()),
			Parented:    r.Parented,
			HasEtag:     r.HasEtag,
			Required:    goList(r.Required),
			OutputOnly:  goList(r.OutputOnly),
			Immutable:   goList(r.Immutable),
			HasImmut:    len(r.Immutable) > 0,
			Remove:      map[bool]string{true: "Purge", false: "Delete"}[r.SoftDelete],
			HookCreate:  svc.Has(r, spec.Create),
			HookUpdate:  svc.Has(r, spec.Update),
			HookDelete:  svc.Has(r, spec.Delete),
			HookUndelete: svc.Has(r, spec.Undelete),
			HookParent:  r.Parented && (svc.Has(r, spec.Create) || svc.Has(r, spec.List)),
		}
		if svc.Has(r, spec.Update) {
			for _, m := range svc.Methods {
				if m.Kind == spec.Update && m.Resource == r && m.AllowMissing != nil {
					x.HookCreate = true
					x.HookParent = x.HookParent || r.Parented
				}
			}
		}
		rv[r] = x
		v.Resources = append(v.Resources, x)
		v.Hooks = append(v.Hooks, hooks(x, im)...)
	}
	for _, m := range svc.Methods {
		if m.Kind == spec.Custom {
			v.Custom = append(v.Custom, m.Proto.GoName)
			continue
		}
		r := rv[m.Resource]
		mv := &methodView{
			Kind:         m.Kind.String(),
			GoName:       m.Proto.GoName,
			Req:          im.qualify(m.Proto.Input),
			R:            r,
			Name:         getter(m.Name),
			Parent:       getter(m.Parent),
			Body:         getter(m.Body),
			ID:           getter(m.ID),
			UpdateMask:   getter(m.UpdateMask),
			Etag:         getter(m.Etag),
			AllowMissing: getter(m.AllowMissing),
			ValidateOnly: getter(m.ValidateOnly),
			Force:        getter(m.Force),
			PageSize:     getter(m.PageSize),
			PageToken:    getter(m.PageToken),
			Filter:       getter(m.Filter),
			OrderBy:      getter(m.OrderBy),
			ShowDeleted:  getter(m.ShowDeleted),
			ReadMask:     getter(m.ReadMask),
			ReturnsEmpty: m.ReturnsEmpty,
		}
		if m.Body != nil {
			mv.BodyField = string(m.Body.Desc.Name())
		}
		if m.ID != nil {
			mv.IDField = string(m.ID.Desc.Name())
		}
		if !m.ReturnsEmpty {
			mv.Resp = im.qualify(m.Proto.Output)
		}
		if m.Items != nil {
			mv.Items, mv.Next = m.Items.GoName, m.NextPageToken.GoName
		}
		if m.Kind == spec.List {
			im.use(repoxOf(m.Resource), "repox")
		}
		if m.ReturnsEmpty {
			v.EmptyQ = im.use("google.golang.org/protobuf/types/known/emptypb", "emptypb")
		}
		if m.Kind == spec.Create || (m.Kind == spec.Update && (m.AllowMissing != nil || m.ValidateOnly != nil)) {
			im.use("google.golang.org/protobuf/proto", "proto")
		}
		v.Methods = append(v.Methods, mv)
	}
	lines, err := im.lines(pkg.Dir + "/" + snake(svc.GoName) + ".svc.go")
	if err != nil {
		return nil, err
	}
	return &serverFile{Package: pkg.GoPackage, Imports: lines, S: v}, nil
}


// hooks are the hook methods r's generated methods call, in a stable order.
func hooks(r *resourceView, im *imports) []hookView {
	ctx := "ctx context.Context"
	var out []hookView
	add := func(doc, name, params string) {
		out = append(out, hookView{Doc: doc, Name: name + r.GoName, Params: params})
	}
	if r.HookCreate || r.HookUpdate {
		add(fmt.Sprintf("Validate%s checks the %s about to be written, after the standard checks: a reference that must name a live resource, a rule between fields. paths is the update mask (empty: every field), nil on create.", r.GoName, strings.ReplaceAll(r.Singular, "_", " ")),
			"Validate", fmt.Sprintf("%s, op servicex.Op, m *%s, paths []string", ctx, r.PB))
	}
	if r.HookParent {
		add(fmt.Sprintf("ParentCheck%s checks the parent the %s is created or listed under, after its shape is checked: that it exists and is live, say. A List parent may hold AIP-159 wildcards.", r.GoName, strings.ReplaceAll(r.Singular, "_", " ")),
			"ParentCheck", fmt.Sprintf("%s, op servicex.Op, parent string", ctx))
	}
	if r.HookCreate {
		add(fmt.Sprintf("BeforeCreate%s runs last before a create is written (or, with validate_only, returned): m has its name, when the caller chose one, and no server-owned field.", r.GoName),
			"BeforeCreate", fmt.Sprintf("%s, parent string, m *%s", ctx, r.PB))
		add(fmt.Sprintf("AfterCreate%s runs after a create is written; an error fails the call, though the record stays.", r.GoName),
			"AfterCreate", fmt.Sprintf("%s, m *%s", ctx, r.PB))
	}
	if r.HookUpdate {
		add(fmt.Sprintf("BeforeUpdate%s runs last before an update is written (or previewed).", r.GoName),
			"BeforeUpdate", fmt.Sprintf("%s, m *%s, paths []string", ctx, r.PB))
		add(fmt.Sprintf("AfterUpdate%s runs after an update is written.", r.GoName),
			"AfterUpdate", fmt.Sprintf("%s, m *%s", ctx, r.PB))
	}
	if r.HookDelete {
		add(fmt.Sprintf("BeforeDelete%s runs before a delete; returning an error refuses it -- a parent that still has children, say. force is the request's force (AIP-135), false when it has none.", r.GoName),
			"BeforeDelete", fmt.Sprintf("%s, name string, force bool", ctx))
		add(fmt.Sprintf("AfterDelete%s runs after a delete.", r.GoName),
			"AfterDelete", fmt.Sprintf("%s, name string", ctx))
	}
	if r.HookUndelete {
		add(fmt.Sprintf("BeforeUndelete%s runs before an undelete; returning an error refuses it -- a parent that is itself deleted, say.", r.GoName),
			"BeforeUndelete", fmt.Sprintf("%s, name string", ctx))
	}
	return out
}

type registerFile struct {
	Banner   string
	Package  string
	Imports  []string
	Services []registerService
}

type registerService struct {
	GoName   string
	Grpc     string // "organizationv1grpc"
	Gateway  string // "" when none
	MCP      string // "" when none
	Standard bool
}

func registerFileView(pkg *spec.Package) (*registerFile, error) {
	im := newImports()
	im.std("context")
	im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
	f := &registerFile{Package: pkg.GoPackage}
	for _, s := range pkg.Services {
		rs := registerService{GoName: s.GoName, Grpc: im.use(s.Grpc.Path, s.Grpc.Name), Standard: s.Standard()}
		if s.Gateway != nil {
			rs.Gateway = im.use(s.Gateway.Path, s.Gateway.Name)
		}
		if s.MCP != nil {
			rs.MCP = im.use(s.MCP.Path, s.MCP.Name)
		}
		f.Services = append(f.Services, rs)
	}
	lines, err := im.lines(pkg.Dir + "/register.svc.go")
	if err != nil {
		return nil, err
	}
	f.Imports = lines
	return f, nil
}
