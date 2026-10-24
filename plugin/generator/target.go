// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package generator renders what package spec decided: the shared servicex
// runtime, one server per service, the per-package registration and the
// event type declarations.
package generator

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"os"
	"path"
	"sort"
	"text/template"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"

	"github.com/the-protobuf-project/service/plugin/servicev1"
	"github.com/the-protobuf-project/service/plugin/spec"
)

// TargetName is the protokit target this plugin registers.
const TargetName = "goservice"

//go:embed templates/*.tmpl
var templates embed.FS

var tmpl = template.Must(template.New("service").Funcs(funcs).ParseFS(templates, "templates/*.tmpl"))

// Settings are the plugin opts.
type Settings struct {
	// GoModule is the import path of this plugin's out directory; servicex is
	// GoModule/servicex.
	GoModule string
	// RepositoryModule is the import path of protoc-gen-store's repository
	// output (its go_module opt).
	RepositoryModule string
	// PackageSuffix names the generated packages (default "svc").
	PackageSuffix string
	// Bindings are the default binding templates; Config may override them
	// per proto package.
	Bindings spec.Bindings
	// Gateway and MCP enable the REST and MCP registrations.
	Gateway, MCP bool
	// ErrorDomain overrides the ErrorInfo domain otherwise taken from each
	// service's google.api.default_host.
	ErrorDomain string
}

// Target is the goservice target.
type Target struct {
	Settings Settings
	Config   *Config
	Versions Versions
}

// Plugin is the protokit plugin protoc-gen-service runs.
func Plugin(s Settings, cfg *Config, v Versions) protokit.Plugin {
	return protokit.Plugin{
		Registry: map[string]schema.Target{TargetName: &Target{Settings: s, Config: cfg, Versions: v}},
		Readers:  servicev1.Readers(),
		Layout:   cfg.Layout(),
	}
}

// Name implements schema.Target.
func (*Target) Name() string { return TargetName }

// Generate implements schema.Target; the target needs the full IR.
func (*Target) Generate(*protogen.Plugin, []*schema.Database) error {
	return fmt.Errorf("service: the goservice target needs the full IR; drive it with protokit.RunPlugin")
}

// GenerateIR implements schema.IRTarget.
func (t *Target) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	s := t.Settings
	if s.GoModule == "" {
		return fmt.Errorf("service: the go_module opt is required: the Go import path of this plugin's out " +
			"directory (e.g. go_module=github.com/me/gen/service), which the generated servers import servicex by")
	}
	if s.RepositoryModule == "" {
		return fmt.Errorf("service: the repository_module opt is required: the Go import path of " +
			"protoc-gen-store's repository output (its go_module opt, e.g. repository_module=github.com/me/gen/repository); " +
			"the generated servers persist through the repositories under it")
	}
	if s.PackageSuffix == "" {
		s.PackageSuffix = "svc"
	}
	model, err := spec.Build(p, ir, spec.Options{
		RepositoryModule: s.RepositoryModule,
		PackageSuffix:    s.PackageSuffix,
		BindingsFor:      t.Config.bindingsFor(s.Bindings),
		Gateway:          s.Gateway,
		MCP:              s.MCP,
		ErrorDomain:      s.ErrorDomain,
	})
	if err != nil {
		return err
	}
	for _, w := range model.Warnings {
		fmt.Fprintln(os.Stderr, "protoc-gen-service: warning:", w)
	}
	if len(model.Packages) == 0 {
		return nil
	}
	indexPackages(p)
	servicex := path.Join(s.GoModule, "servicex")
	runtime := map[string]any{
		"Banner":      banner(p, t.Versions, nil, "Shared runtime of the generated AIP standard-method servers."),
		"RepoxImport": path.Join(s.RepositoryModule, "repox"),
	}
	if err := render(p, "servicex/servicex.go", "servicex.go.tmpl", runtime); err != nil {
		return err
	}
	if err := render(p, "servicex/events.go", "events.go.tmpl", runtime); err != nil {
		return err
	}
	if err := renderText(p, "servicex/errors.svc.md", "errors.md.tmpl", map[string]any{
		"Domain": "", "Proto": "servicex", "Reasons": standardReasons,
	}); err != nil {
		return err
	}
	for _, pkg := range model.Packages {
		sources := packageSources(pkg)
		for _, svc := range pkg.Services {
			if !svc.Standard() {
				// Nothing to generate: every method is custom. The service
				// is still registered (Servers takes any implementation).
				continue
			}
			view, err := serviceFileView(pkg, svc, servicex, s.RepositoryModule)
			if err != nil {
				return err
			}
			view.Banner = banner(p, t.Versions, []string{svc.Proto.Location.SourceFile}, "AIP standard-method server for "+svc.FullName+".")
			if err := render(p, path.Join(pkg.Dir, snake(svc.GoName)+".svc.go"), "server.go.tmpl", view); err != nil {
				return err
			}
		}
		if len(pkg.Services) > 0 {
			reg, err := registerFileView(pkg)
			if err != nil {
				return err
			}
			reg.Banner = banner(p, t.Versions, sources, "Registration of package "+pkg.ProtoPackage+"'s servers with runtime-go's HybridServer.")
			if err := render(p, path.Join(pkg.Dir, "register.svc.go"), "register.go.tmpl", reg); err != nil {
				return err
			}
		}
		if err := t.extras(p, pkg, servicex, sources); err != nil {
			return err
		}
	}
	return nil
}

func packageSources(pkg *spec.Package) []string {
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, s := range pkg.Services {
		add(s.Proto.Location.SourceFile)
	}
	for _, r := range pkg.Feeds {
		add(r.Message.Location.SourceFile)
	}
	for _, a := range pkg.Adapters {
		add(a.Resource.Message.Location.SourceFile)
	}
	for _, e := range pkg.Errors {
		add(e.Enum.Location.SourceFile)
	}
	sort.Strings(out)
	return out
}

func render(p *protogen.Plugin, name, tpl string, view any) error {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, tpl, view); err != nil {
		return fmt.Errorf("service: render %s: %w", name, err)
	}
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("service: %s: generated code does not parse: %w\n\n%s", name, err, buf.String())
	}
	if _, err := p.NewGeneratedFile(name, "").Write(src); err != nil {
		return fmt.Errorf("service: write %s: %w", name, err)
	}
	return nil
}

// extras renders a package's feeds, error reasons and store adapters.
func (t *Target) extras(p *protogen.Plugin, pkg *spec.Package, servicex string, sources []string) error {
	if len(sources) == 0 {
		sources = []string{pkg.ProtoPackage}
	}
	feeds, err := feedsFileView(pkg, servicex)
	if err != nil {
		return err
	}
	if feeds != nil {
		feeds.Banner = banner(p, t.Versions, sources, "Change feeds of package "+pkg.ProtoPackage+"'s resources, for runtime-go/events.")
		if err := render(p, path.Join(pkg.Dir, "feeds.svc.go"), "feeds.go.tmpl", feeds); err != nil {
			return err
		}
	}
	errs, err := errorsFileView(pkg, servicex)
	if err != nil {
		return err
	}
	if errs != nil {
		errs.Banner = banner(p, t.Versions, sources, "Error reasons of package "+pkg.ProtoPackage+" (AIP-193).")
		if err := render(p, path.Join(pkg.Dir, "errors.svc.go"), "errors.go.tmpl", errs); err != nil {
			return err
		}
		if err := renderText(p, path.Join(pkg.Dir, "errors.svc.md"), "errors.md.tmpl", errs); err != nil {
			return err
		}
	}
	ad, warnings, err := adaptersFileView(pkg, servicex)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "protoc-gen-service: warning:", w)
	}
	if ad != nil {
		ad.Banner = banner(p, t.Versions, sources, "runtime-go store adapters over package "+pkg.ProtoPackage+"'s repositories.")
		if err := render(p, path.Join(pkg.Dir, "adapters.svc.go"), "adapters.go.tmpl", ad); err != nil {
			return err
		}
	}
	return nil
}

// renderText renders a non-Go file.
func renderText(p *protogen.Plugin, name, tpl string, view any) error {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, tpl, view); err != nil {
		return fmt.Errorf("service: render %s: %w", name, err)
	}
	if _, err := p.NewGeneratedFile(name, "").Write(buf.Bytes()); err != nil {
		return fmt.Errorf("service: write %s: %w", name, err)
	}
	return nil
}

// standardReasons are servicex's reasons, for its error reference fragment.
var standardReasons = []*spec.ErrorReason{
	{Name: "NOT_FOUND", CodeName: "NOT_FOUND", Description: "The resource named does not exist, or is deleted.", Fix: "Check the name; list what exists."},
	{Name: "ALREADY_EXISTS", CodeName: "ALREADY_EXISTS", Description: "A create's id is taken, or an Undelete names something not deleted.", Fix: "Choose another id, or leave it empty; read the existing one."},
	{Name: "CONCURRENT_MODIFICATION", CodeName: "ABORTED", Description: "The etag sent is no longer current, or another writer won the race for the same record.", Fix: "Read the resource again, reapply the change, retry with the new etag.", Retryable: true},
	{Name: "REQUIRED_FIELD", CodeName: "INVALID_ARGUMENT", Description: "A field the call needs is empty.", Fix: "Set it."},
	{Name: "INVALID_ID", CodeName: "INVALID_ARGUMENT", Description: "A `*_id` is not a valid id: lower-case letters, digits and hyphens, starting with a letter, at most 63 characters (AIP-122).", Fix: "Fix it, or leave it empty to have one assigned."},
	{Name: "MALFORMED_NAME", CodeName: "INVALID_ARGUMENT", Description: "A resource name is not shaped like the collection it should be in.", Fix: "Send the full name the create call returned."},
	{Name: "NOTHING_TO_UPDATE", CodeName: "INVALID_ARGUMENT", Description: "An update sets no field and names none in `update_mask`.", Fix: "Set the fields to change and name them in `update_mask`."},
	{Name: "UNKNOWN_MASK_PATH", CodeName: "INVALID_ARGUMENT", Description: "`update_mask` names a field the resource does not have.", Fix: "Name fields by their proto names."},
	{Name: "IMMUTABLE_FIELD", CodeName: "INVALID_ARGUMENT", Description: "An update changes a field that cannot change once set (AIP-203 IMMUTABLE).", Fix: "Leave it out of `update_mask`, or create a new resource."},
	{Name: "INVALID_ARGUMENT", CodeName: "INVALID_ARGUMENT", Description: "The request is wrong in a way no more specific reason names (a bad filter, a bad page token).", Fix: "Read the message."},
	{Name: "INVALID_STATE", CodeName: "FAILED_PRECONDITION", Description: "The resource's state forbids the call, in a way no more specific reason names.", Fix: "Read the message."},
}
