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

// TestAppShell renders the application shell over the calendar case: every
// file, the seams the custom method needs, and only the sections the run
// uses -- the calendar package has feeds but no webhook store, so no
// dispatcher, and soft-deletable resources, so a purger. With no datasources
// layout the store names the schema after the resource host
// (calendaracmeexample).
func TestAppShell(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "calendar"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	s := testSettings
	s.App = "acme"
	s.Store = true
	if err := protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, generator.Plugin(s, nil, testVersions)); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range p.Response().File {
		files[f.GetName()] = f.GetContent()
	}
	for _, name := range []string{
		"app/app.go", "app/build.go", "cmd/acme/main.go", "errors.md",
		"app/config/config.go", "app/config/load.go", "app/config/options.go", "app/config/acme.release.toml", "app/config/acme.dev.toml",
		"app/telemetry/telemetry.go", "app/telemetry/meter.go", "app/telemetry/telemetry.toml",
		"app/database/connection.go", "app/database/open.go", "app/database/translate.go", "app/database/logger.go", "app/database/pool.go", "app/database/cache.go",
		"app/server/repositories.go", "app/server/translator.go", "app/server/provision.go", "app/server/assembly.go",
		"app/server/interceptors.go", "app/server/register.go", "app/server/server.go", "app/server/cors.go", "app/server/purge.go",
	} {
		if _, ok := files[name]; !ok {
			t.Errorf("no %s", name)
		}
	}
	for file, wants := range map[string][]string{
		"app/server/provision.go": {
			"Calendars calendarv1svc.CalendarsLogic",
			"Calendars calendarv1svc.CalendarsHooks",
			"custom method QueryFreeBusy has no implementation; give one in Logic.Calendars",
		},
		"app/server/assembly.go": {
			"s.Calendars = calendarv1svc.NewCalendarsServer(calendarv1svc.CalendarsRepositories{Calendars: r.Calendaracmeexample.Calendars}, p.Hooks.Calendars",
			"s.Calendars.WithLogic(p.Logic.Calendars)",
			"s.Purger = NewPurger(",
		},
		"app/server/register.go": {"calendarv1svc.RegisterAll(calendarv1svc.Servers{Calendars: s.registeredCalendars(), Events: s.registeredEvents()})"},
		"app/config/load.go":     {`const EnvPrefix = "ACME_"`},
		"cmd/acme/main.go":       {"server.Provision{Unimplemented: true}"},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				got := ""
				for line := range strings.SplitSeq(files[file], "\n") {
					if head, _, _ := strings.Cut(want, "("); strings.Contains(line, head) {
						got = strings.TrimSpace(line)
					}
				}
				t.Errorf("%s lacks %q; it has %q", file, want, got)
			}
		}
	}
	if strings.Contains(files["app/server/assembly.go"], "Dispatcher") || strings.Contains(files["app/config/acme.release.toml"], "[webhooks]") {
		t.Error("a run with no webhook store generated webhook delivery")
	}
	if !strings.Contains(files["app/config/acme.release.toml"], "[retention]") {
		t.Error("a run with soft-deletable resources has no [retention]")
	}
}

// TestFlagRefusals checks the combinations the flags refuse: an app shell
// over the handler model, cache without store, and cache without the module
// its wiring imports.
func TestFlagRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*generator.Settings)
		want   string
	}{
		"app without store":               {func(s *generator.Settings) { s.App = "acme" }, "app=acme needs store=true"},
		"cache without store":             {func(s *generator.Settings) { s.Cache = true; s.CacheModule = "example.com/gen/cache" }, "cache=true requires store=true"},
		"cache without module":            {func(s *generator.Settings) { s.Store = true; s.Cache = true }, "cache=true needs the cache_module opt"},
		"store without repository_module": {func(s *generator.Settings) { s.Store = true; s.RepositoryModule = "" }, "repository_module opt is required"},
	} {
		t.Run(name, func(t *testing.T) {
			req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "directory"))
			p, err := protogen.Options{}.New(req)
			if err != nil {
				t.Fatal(err)
			}
			s := testSettings
			tc.mutate(&s)
			err = protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, generator.Plugin(s, nil, testVersions))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestAppShellCache checks that cache=true has the assembly wrap a
// list-cached service's repositories, and leaves the others alone.
func TestAppShellCache(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "cache"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatal(err)
	}
	s := testSettings
	s.App, s.Store, s.Cache, s.CacheModule = "acme", true, true, cacheModule
	if err := protokit.RunPlugin(p, protokit.Options{Target: generator.TargetName}, generator.Plugin(s, nil, testVersions)); err != nil {
		t.Fatal(err)
	}
	var assembly string
	for _, f := range p.Response().File {
		if f.GetName() == "app/server/assembly.go" {
			assembly = f.GetContent()
		}
	}
	for _, want := range []string{
		"catalogv1svc.CachedShopsRepositories(context.Background(), cp, d.Conn.Observe, shopsRepos)",
		"catalogv1svc.NewShopsServer(shopsRepos,",
		"catalogv1svc.NewProductsServer(catalogv1svc.ProductsRepositories{",
	} {
		if !strings.Contains(assembly, want) {
			t.Errorf("assembly.go lacks %q", want)
		}
	}
}
