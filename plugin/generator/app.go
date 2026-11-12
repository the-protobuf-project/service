// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

// app.go renders the application shell app=<name> asks for: the
// configuration, telemetry, database and server packages, and the command
// that runs them. It is the plumbing every service built over the generated
// layer otherwise writes by hand -- loading TOML, opening the database,
// building each server, registering it on the hybrid server, starting the
// background work -- so that a project starts with only its business logic
// to write, and that logic is provisioned, never generated: hooks, Logic
// implementations, server overrides and Extensions, handed in through one
// Provision.

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/service/plugin/spec"
)

// appView is the data of every shell file.
type appView struct {
	Banner  string
	Imports []string

	Name  string // "acme"
	Env   string // "ACME_", the environment variable prefix
	Title string // "Acme"

	// Import paths of the shell's packages.
	Config, Telemetry, Database, Server, App string

	Repos    []appRepo
	Services []*appService
	Packages []*appPackage
	// Errors are the qualifiers of the packages that declare error reasons.
	Errors []string

	Webhooks *appWebhooks
	Idem     *appIdem
	Feeds    []string // "calendarv1svc.EventFeed(r.Calendar.Events)"
	Purge    []appPurge

	// ErrorDocs are the packages' error references, StandardReasons the
	// generated servers' own, for the run's errors.md.
	ErrorDocs       []*errorsFile
	StandardReasons []*spec.ErrorReason

	// Domain is the first service's ErrorInfo domain, for the shell's own
	// refusals (idempotency); EventSource its CloudEvents source.
	Domain, EventSource string
}

// appRepo is one repository package, opened on the shared connection.
type appRepo struct {
	Field string // Repositories field, "Calendar"
	Pkg   string // qualifier, "calendar"
}

// appService is one service the shell assembles and registers.
type appService struct {
	Field    string // Services, Hooks, Logic and Overrides field
	GoName   string
	FullName string
	Pkg      string // the generated package's qualifier, "calendarv1svc"
	Grpc     string // the gRPC stubs' qualifier
	// Generated reports a generated server; without one (every method
	// custom and streaming), Overrides must name the implementation.
	Generated bool
	Hooks     bool
	Logic     []string // its Logic methods, for the startup check's message
	Repos     string   // the <S>Repositories literal
	Domain    string
}

// appPackage is one generated package, registered through its RegisterAll.
type appPackage struct {
	Pkg      string
	Services []*appService
}

type appWebhooks struct {
	Store string // the EventStore literal
}

type appIdem struct {
	Store string
}

// appPurge is one soft-deletable resource the purger sweeps.
type appPurge struct {
	Kind string // "event"
	List string // "r.Calendar.Events.List(ctx, \"calendars/-\", in)"
	Repo string // "r.Calendar.Events"
}

// appFile is one shell file: its output path, template, and imports.
type appFile struct {
	path, tmpl string
	imports    func(v *appView, im *imports)
	text       bool // not Go: written as it renders
}

// renderApp renders the shell for model under s.GoModule.
func (t *Target) renderApp(p *protogen.Plugin, model *spec.Model, servicex string) error {
	s := t.Settings
	v, err := appFileView(model, s)
	if err != nil {
		return err
	}
	v.StandardReasons = standardReasons
	for _, pkg := range model.Packages {
		doc, err := errorsFileView(pkg, servicex)
		if err != nil {
			return err
		}
		if doc != nil {
			v.ErrorDocs = append(v.ErrorDocs, doc)
		}
	}
	cmd := path.Join("cmd", s.App, "main.go")
	files := []appFile{
		{path: "app/config/config.go", tmpl: "app_config.go.tmpl", imports: stdOnly("cmp", "embed", "fmt", "net", "net/url", "strconv", "time", "github.com/the-protobuf-project/runtime-go/grpc/options")},
		{path: "app/config/load.go", tmpl: "app_load.go.tmpl", imports: stdOnly("errors", "fmt", "os", "slices", "strings", "sync",
			"github.com/knadh/koanf/parsers/toml", "github.com/knadh/koanf/providers/env/v2", "github.com/knadh/koanf/providers/rawbytes", "github.com/knadh/koanf/v2",
			"github.com/the-protobuf-project/runtime-go/grpc/options")},
		{path: "app/config/options.go", tmpl: "app_options.go.tmpl", imports: stdOnly("github.com/the-protobuf-project/runtime-go/grpc/options")},
		{path: "app/config/" + s.App + ".release.toml", tmpl: "app_release.toml.tmpl", text: true},
		{path: "app/config/" + s.App + ".dev.toml", tmpl: "app_dev.toml.tmpl", text: true},
		{path: "app/telemetry/telemetry.go", tmpl: "app_telemetry.go.tmpl", imports: stdOnly("_ embed", "fmt", "net", "os", "strconv", "sync",
			"github.com/knadh/koanf/parsers/toml", "github.com/knadh/koanf/providers/rawbytes", "github.com/knadh/koanf/v2",
			"github.com/the-protobuf-project/runtime-go/observability", "github.com/the-protobuf-project/telemetry/telemetry-go",
			"github.com/the-protobuf-project/telemetry/telemetry-go/options")},
		{path: "app/telemetry/meter.go", tmpl: "app_meter.go.tmpl", imports: stdOnly("github.com/the-protobuf-project/runtime-go/telemetry")},
		{path: "app/telemetry/telemetry.toml", tmpl: "app_telemetry.toml.tmpl", text: true},
		{path: "app/database/connection.go", tmpl: "app_connection.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "sync")
			im.use(v.Config, "config")
			im.use(repoxOfModule(t.Settings.RepositoryModule), "repox")
			im.use("github.com/the-protobuf-project/runtime-go/cache", "cache")
			im.use("gorm.io/gorm", "gorm")
		}},
		{path: "app/database/open.go", tmpl: "app_open.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "fmt")
			im.use(v.Config, "config")
			im.use("gorm.io/gorm", "gorm")
		}},
		{path: "app/database/translate.go", tmpl: "app_translate.go.tmpl", imports: stdOnly("errors", "fmt",
			"github.com/jackc/pgx/v5/pgconn", "gorm.io/driver/postgres", "gorm.io/gorm", "gorm.io/gorm/logger")},
		{path: "app/database/logger.go", tmpl: "app_logger.go.tmpl", imports: stdOnly("context", "errors", "time",
			"github.com/jackc/pgx/v5/pgconn", "gorm.io/gorm", "gorm.io/gorm/logger")},
		{path: "app/database/pool.go", tmpl: "app_pool.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "database/sql", "time")
			im.use(v.Telemetry, "telemetry")
		}},
		{path: "app/database/cache.go", tmpl: "app_cache.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "fmt")
			im.use(v.Config, "config")
			im.use(v.Telemetry, "telemetry")
			im.use("github.com/the-protobuf-project/runtime-go/cache", "cache")
			im.use("github.com/the-protobuf-project/runtime-go/cache/redis", "redis")
		}},
		{path: "app/server/repositories.go", tmpl: "app_repositories.go.tmpl", imports: func(v *appView, im *imports) {
			im.use(repoxOfModule(t.Settings.RepositoryModule), "repox")
			for _, r := range model.Repositories {
				im.use(r.Path, r.Name)
			}
		}},
		{path: "app/server/translator.go", tmpl: "app_translator.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context")
			im.use(servicex, "servicex")
			im.use(v.Telemetry, "telemetry")
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
			im.use("github.com/the-protobuf-project/runtime-go/grpc/problem", "problem")
			for _, pkg := range model.Packages {
				if len(pkg.Errors) > 0 {
					im.use(path.Join(s.GoModule, pkg.Dir), pkg.GoPackage)
				}
			}
		}},
		{path: "app/server/provision.go", tmpl: "app_provision.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "errors", "fmt", "strings")
			im.use(v.Config, "config")
			im.use(v.Database, "database")
			im.use(v.Telemetry, "telemetry")
			im.use(servicex, "servicex")
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
			for _, sv := range v.Services {
				if sv.Generated {
					im.use(path.Join(s.GoModule, pkgOf(model, sv).Dir), sv.Pkg)
				}
				im.use(serviceOf(model, sv).Grpc.Path, sv.Grpc)
			}
		}},
		{path: "app/server/assembly.go", tmpl: "app_assembly.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "errors")
			im.use(v.Config, "config")
			im.use(v.Database, "database")
			im.use(v.Telemetry, "telemetry")
			for _, sv := range v.Services {
				if sv.Generated {
					im.use(path.Join(s.GoModule, pkgOf(model, sv).Dir), sv.Pkg)
				}
			}
			if v.Webhooks != nil {
				im.std("fmt", "strings")
				im.use("github.com/the-protobuf-project/runtime-go/events", "events")
				for _, pkg := range model.Packages {
					if len(pkg.Feeds) > 0 || strings.HasPrefix(v.Webhooks.Store, "&"+pkg.GoPackage+".") {
						im.use(path.Join(s.GoModule, pkg.Dir), pkg.GoPackage)
					}
				}
			}
		}},
		{path: "app/server/interceptors.go", tmpl: "app_interceptors.go.tmpl", imports: func(v *appView, im *imports) {
			im.use("github.com/the-protobuf-project/runtime-go/grpc/hints", "hints")
			im.use("google.golang.org/grpc", "grpc")
			if v.Idem != nil {
				im.use(repoxOfModule(t.Settings.RepositoryModule), "repox")
				im.use("github.com/the-protobuf-project/runtime-go/grpc/idempotency", "idempotency")
				for _, pkg := range model.Packages {
					if strings.HasPrefix(v.Idem.Store, "&"+pkg.GoPackage+".") {
						im.use(path.Join(s.GoModule, pkg.Dir), pkg.GoPackage)
					}
				}
			}
		}},
		{path: "app/server/register.go", tmpl: "app_register.go.tmpl", imports: func(v *appView, im *imports) {
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
			for _, pkg := range v.Packages {
				im.use(path.Join(s.GoModule, pkgDir(model, pkg.Pkg)), pkg.Pkg)
			}
		}},
		{path: "app/server/server.go", tmpl: "app_server.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "crypto/tls", "fmt", "os/signal", "syscall", "time")
			im.use(v.Config, "config")
			im.use(v.Database, "database")
			im.use(v.Telemetry, "telemetry")
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
			im.use("github.com/the-protobuf-project/runtime-go/grpc/options", "options")
		}},
		{path: "app/server/cors.go", tmpl: "app_cors.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("fmt")
			im.use(v.Config, "config")
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
		}},
		{path: "app/app.go", tmpl: "app_app.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("fmt", "os")
			im.use(v.Config, "config")
			im.use(v.Server, "server")
			im.use(v.Telemetry, "telemetry")
			im.use("github.com/the-protobuf-project/runtime-go/grpc", "grpc")
		}},
		{path: "app/build.go", tmpl: "app_build.go.tmpl", imports: stdOnly("fmt", "runtime/debug")},
		{path: cmd, tmpl: "app_main.go.tmpl", imports: func(v *appView, im *imports) {
			im.byPath["time/tzdata"] = "_"
			im.use(v.App, "app")
			im.use(v.Server, "server")
		}},
		{path: "errors.md", tmpl: "app_errors.md.tmpl", text: true},
	}
	if len(v.Purge) > 0 {
		files = append(files, appFile{path: "app/server/purge.go", tmpl: "app_purge.go.tmpl", imports: func(v *appView, im *imports) {
			im.std("context", "errors", "fmt", "time")
			im.use(repoxOfModule(t.Settings.RepositoryModule), "repox")
			im.use("github.com/the-protobuf-project/runtime-go/telemetry", "telemetry")
			im.use("google.golang.org/protobuf/types/known/timestamppb", "timestamppb")
		}})
	}
	for _, f := range files {
		fv := *v
		fv.Banner = banner(p, t.Versions, PackageSourcesOf(model), "Application shell of "+s.App+": "+appNotes[f.tmpl]+".")
		if f.text {
			fv.Banner = strings.ReplaceAll(fv.Banner, "// ", "# ")
			fv.Banner = strings.ReplaceAll(fv.Banner, "//", "#")
			if err := renderText(p, f.path, f.tmpl, &fv); err != nil {
				return err
			}
			continue
		}
		im := newImports()
		f.imports(&fv, im)
		lines, err := im.lines(f.path)
		if err != nil {
			return err
		}
		fv.Imports = lines
		if err := render(p, f.path, f.tmpl, &fv); err != nil {
			return err
		}
	}
	return nil
}

// appNotes say what each shell file is, for its banner.
var appNotes = map[string]string{
	"app_config.go.tmpl":       "the typed configuration and its sections",
	"app_load.go.tmpl":         "loading the release defaults, the development profile and the environment",
	"app_options.go.tmpl":      "the hybrid server's options, from the configuration",
	"app_release.toml.tmpl":    "the release defaults, embedded in the binary",
	"app_dev.toml.tmpl":        "the development profile",
	"app_telemetry.go.tmpl":    "the process's telemetry pipeline",
	"app_meter.go.tmpl":        "the pipeline's meter",
	"app_telemetry.toml.tmpl":  "the telemetry pipeline's embedded defaults",
	"app_connection.go.tmpl":   "the shared database connection",
	"app_open.go.tmpl":         "opening the configured database",
	"app_translate.go.tmpl":    "Postgres errors as GORM sentinels",
	"app_logger.go.tmpl":       "the GORM logger, quiet about outcomes the repositories expect",
	"app_pool.go.tmpl":         "the connection pool's health in telemetry",
	"app_cache.go.tmpl":        "the cache provider",
	"app_repositories.go.tmpl": "every repository of the run, on one connection",
	"app_translator.go.tmpl":   "the error translator and tracer every server shares",
	"app_provision.go.tmpl":    "what an application provisions: hooks, logic, overrides and extensions",
	"app_assembly.go.tmpl":     "the servers, assembled on one connection, and their background work",
	"app_interceptors.go.tmpl": "the interceptor chain every surface runs",
	"app_register.go.tmpl":     "every server on gRPC, REST and MCP",
	"app_server.go.tmpl":       "the hybrid server and its lifecycle",
	"app_cors.go.tmpl":         "the HTTP port's CORS policy",
	"app_purge.go.tmpl":        "the purger, which ends soft deletes past their retention",
	"app_app.go.tmpl":          "the application and its entry point",
	"app_build.go.tmpl":        "the build's version and commit",
	"app_main.go.tmpl":         "the command",
	"app_errors.md.tmpl":       "the error reference of every package",
}

func stdOnly(paths ...string) func(*appView, *imports) {
	return func(_ *appView, im *imports) {
		for _, p := range paths {
			if name, rest, ok := strings.Cut(p, " "); ok {
				im.byPath[rest] = name // a blank import: "_ embed"
				continue
			}
			im.std(p)
		}
	}
}

// appFileView builds the shell's data from model.
func appFileView(model *spec.Model, s Settings) (*appView, error) {
	root := path.Join(s.GoModule, "app")
	v := &appView{
		Name:      s.App,
		Env:       strings.ToUpper(naming.SnakeCase(s.App)) + "_",
		Title:     naming.PascalGo(s.App),
		Config:    path.Join(root, "config"),
		Telemetry: path.Join(root, "telemetry"),
		Database:  path.Join(root, "database"),
		Server:    path.Join(root, "server"),
		App:       root,
	}
	repoField := map[string]string{}
	for _, r := range model.Repositories {
		f := naming.PascalGo(r.Name)
		repoField[r.Path] = f
		v.Repos = append(v.Repos, appRepo{Field: f, Pkg: r.Name})
	}
	repoExpr := func(r *spec.Resource) string {
		return "r." + repoField[r.Repository.Path] + "." + naming.PascalGo(naming.Pluralize(naming.SnakeCase(r.Message.GoIdent.GoName)))
	}

	// Services keep their name unless two packages share it.
	count := map[string]int{}
	for _, pkg := range model.Packages {
		for _, svc := range pkg.Services {
			count[svc.GoName]++
		}
	}
	seenRes := map[*spec.Resource]bool{}
	var resources []*spec.Resource
	addRes := func(r *spec.Resource) {
		if !seenRes[r] {
			seenRes[r] = true
			resources = append(resources, r)
		}
	}
	for _, pkg := range model.Packages {
		ap := &appPackage{Pkg: pkg.GoPackage}
		for _, svc := range pkg.Services {
			field := svc.GoName
			if count[svc.GoName] > 1 {
				field = naming.PascalGo(protoLeaf(pkg.ProtoPackage)) + svc.GoName
			}
			as := &appService{
				Field: field, GoName: svc.GoName, FullName: svc.FullName, Pkg: pkg.GoPackage,
				Grpc: svc.Grpc.Name, Generated: svc.Standard() || hasLogic(svc), Hooks: svc.Standard(), Domain: svc.Domain,
			}
			for _, m := range svc.Methods {
				if m.Kind == spec.Custom && !m.Proto.Desc.IsStreamingClient() && !m.Proto.Desc.IsStreamingServer() {
					as.Logic = append(as.Logic, m.Proto.GoName)
				}
			}
			var fields []string
			for _, r := range svc.Resources {
				addRes(r)
				fields = append(fields, naming.PascalGo(naming.Pluralize(naming.SnakeCase(r.Message.GoIdent.GoName)))+": "+repoExpr(r))
			}
			as.Repos = pkg.GoPackage + "." + svc.GoName + "Repositories{" + strings.Join(fields, ", ") + "}"
			if v.Domain == "" {
				v.Domain, v.EventSource = svc.Domain, svc.EventSource
			}
			v.Services = append(v.Services, as)
			ap.Services = append(ap.Services, as)
		}
		if len(ap.Services) > 0 {
			v.Packages = append(v.Packages, ap)
		}
		if len(pkg.Errors) > 0 {
			v.Errors = append(v.Errors, pkg.GoPackage)
		}
		for _, r := range pkg.Feeds {
			addRes(r)
			v.Feeds = append(v.Feeds, pkg.GoPackage+"."+r.Message.GoIdent.GoName+"Feed("+repoExpr(r)+")")
		}
		roles := map[string]*spec.Adapter{}
		for _, a := range pkg.Adapters {
			addRes(a.Resource)
			roles[a.Role] = a
		}
		if w, d, c := roles["Webhook"], roles["Delivery"], roles["FeedCursor"]; w != nil && d != nil && c != nil && v.Webhooks == nil {
			v.Webhooks = &appWebhooks{Store: fmt.Sprintf("&%s.EventStore{Webhooks: %s, Deliveries: %s, Cursors: %s}",
				pkg.GoPackage, repoExpr(w.Resource), repoExpr(d.Resource), repoExpr(c.Resource))}
		}
		if k := roles["IdempotencyKey"]; k != nil && v.Idem == nil {
			v.Idem = &appIdem{Store: fmt.Sprintf("&%s.IdempotencyStore{Keys: %s}", pkg.GoPackage, repoExpr(k.Resource))}
		}
	}
	if len(v.Services) == 0 {
		return nil, fmt.Errorf("service: app=%s: the run declares no service to serve", s.App)
	}
	if v.Webhooks != nil && len(v.Feeds) == 0 {
		v.Webhooks = nil // a dispatcher with nothing to send
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Type < resources[j].Type })
	for _, r := range resources {
		if !r.SoftDelete {
			continue
		}
		parent := ""
		if r.Parented {
			var parts []string
			for _, c := range r.ParentCollections() {
				parts = append(parts, c, "-")
			}
			parent = fmt.Sprintf("%q, ", strings.Join(parts, "/"))
		}
		v.Purge = append(v.Purge, appPurge{Kind: r.Singular, Repo: repoExpr(r), List: repoExpr(r) + ".List(ctx, " + parent + "in)"})
	}
	return v, nil
}

func protoLeaf(pkg string) string {
	parts := strings.Split(pkg, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		if !versionSegment(parts[i]) {
			return parts[i]
		}
	}
	return parts[len(parts)-1]
}

func versionSegment(s string) bool {
	return len(s) > 1 && s[0] == 'v' && s[1] >= '0' && s[1] <= '9'
}

func pkgOf(model *spec.Model, sv *appService) *spec.Package {
	for _, pkg := range model.Packages {
		if pkg.GoPackage == sv.Pkg {
			return pkg
		}
	}
	return nil
}

func pkgDir(model *spec.Model, goPkg string) string {
	for _, pkg := range model.Packages {
		if pkg.GoPackage == goPkg {
			return pkg.Dir
		}
	}
	return ""
}

func serviceOf(model *spec.Model, sv *appService) *spec.Service {
	for _, svc := range pkgOf(model, sv).Services {
		if svc.FullName == sv.FullName {
			return svc
		}
	}
	return nil
}

func repoxOfModule(module string) string { return path.Join(module, "repox") }

// PackageSourcesOf returns the sorted source files of every package in model.
func PackageSourcesOf(model *spec.Model) []string {
	seen := map[string]bool{}
	var out []string
	for _, pkg := range model.Packages {
		for _, f := range packageSources(pkg) {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}
