// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

// cache.go renders cache=true's Go wiring: per package, cache.svc.go builds
// protoc-gen-cache's list caches in front of the repositories a store=true
// server persists through, for every resource whose (cache.v1.cache).list is
// enabled. protoc-gen-cache's record caches decorate the GORM stores under a
// repository, scoped to a tenant the deployment knows; they stay the
// application's to wire (the repository package's With<Model>Store).

import (
	"path"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/spec"
)

// The cache.v1 extension and fields read here, by number: the plugin reads
// them from the raw options rather than linking protoc-gen-cache's types.
const (
	cacheExtension   = 52001 // (cache.v1.cache) on MessageOptions
	cacheEnabled     = 1     // CacheOptions.enabled
	cacheListField   = 7     // CacheOptions.list
	cacheListEnabled = 1     // ListCacheOptions.enabled
)

// CacheAnnotations are a message's (cache.v1.cache) switches.
type CacheAnnotations struct {
	// Records is (cache.v1.cache).enabled: records read one at a time.
	Records bool
	// List is (cache.v1.cache).list.enabled: List pages.
	List bool
}

// Any reports whether either cache is on.
func (c CacheAnnotations) Any() bool { return c.Records || c.List }

// CacheOf reads m's (cache.v1.cache).
func CacheOf(m *protogen.Message) CacheAnnotations {
	var out CacheAnnotations
	opts := m.Desc.Options()
	if opts == nil {
		return out
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(opts)
	if err != nil {
		return out
	}
	for _, ext := range fieldBytes(raw, cacheExtension) {
		if fieldBool(ext, cacheEnabled) {
			out.Records = true
		}
		for _, list := range fieldBytes(ext, cacheListField) {
			if fieldBool(list, cacheListEnabled) {
				out.List = true
			}
		}
	}
	return out
}

// fieldBytes returns every length-delimited value of field num in b.
func fieldBytes(b []byte, num protowire.Number) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return out
		}
		b = b[l:]
		if n == num && typ == protowire.BytesType {
			v, l := protowire.ConsumeBytes(b)
			if l < 0 {
				return out
			}
			out = append(out, v)
			b = b[l:]
			continue
		}
		l = protowire.ConsumeFieldValue(n, typ, b)
		if l < 0 {
			return out
		}
		b = b[l:]
	}
	return out
}

// fieldBool reports the last value of bool field num in b.
func fieldBool(b []byte, num protowire.Number) bool {
	v := false
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return v
		}
		b = b[l:]
		if n == num && typ == protowire.VarintType {
			x, l := protowire.ConsumeVarint(b)
			if l < 0 {
				return v
			}
			v = x != 0
			b = b[l:]
			continue
		}
		l = protowire.ConsumeFieldValue(n, typ, b)
		if l < 0 {
			return v
		}
		b = b[l:]
	}
	return v
}

// cacheFile is cache.svc.go's data.
type cacheFile struct {
	Banner   string
	Package  string
	Imports  []string
	Services []cacheService
}

type cacheService struct {
	GoName string
	Lists  []cacheList
}

type cacheList struct {
	Field string // <S>Repositories field
	Model string // "Event"
	Pkg   string // the cache package's qualifier
	Type  string // "event.v1.Event", for errors
}

// cachePackage is protoc-gen-cache's Go package for r: its output's
// <database>/<schema package>cache, beside the store's <database>/<schema>.
func cachePackage(module string, r *spec.Resource) (pkgPath, name string) {
	db := path.Base(path.Dir(r.Repository.Path))
	name = r.Repository.Name + "cache"
	return path.Join(module, db, name), name
}

// cachedServices are the services of pkg with a list-cached resource.
func cachedServices(pkg *spec.Package) []*spec.Service {
	var out []*spec.Service
	for _, svc := range pkg.Services {
		for _, r := range svc.Resources {
			if svc.Has(r, spec.List) && CacheOf(r.Message).List {
				out = append(out, svc)
				break
			}
		}
	}
	return out
}

// renderCache renders pkg's cache.svc.go, when a service of it serves a
// list-cached resource.
func (t *Target) renderCache(p *protogen.Plugin, pkg *spec.Package, sources []string) error {
	svcs := cachedServices(pkg)
	if len(svcs) == 0 {
		return nil
	}
	im := newImports()
	im.std("context")
	im.use("github.com/the-protobuf-project/runtime-go/cache", "cache")
	f := &cacheFile{Package: pkg.GoPackage}
	for _, svc := range svcs {
		cs := cacheService{GoName: svc.GoName}
		for _, r := range svc.Resources {
			if !svc.Has(r, spec.List) || !CacheOf(r.Message).List {
				continue
			}
			im.use(repoxOf(r), "repox")
			pp, name := cachePackage(t.Settings.CacheModule, r)
			cs.Lists = append(cs.Lists, cacheList{
				Field: resourceField(r),
				Model: r.Message.GoIdent.GoName,
				Pkg:   im.use(pp, name),
				Type:  string(r.Message.Desc.FullName()),
			})
		}
		f.Services = append(f.Services, cs)
	}
	name := path.Join(pkg.Dir, "cache.svc.go")
	lines, err := im.lines(name)
	if err != nil {
		return err
	}
	f.Imports = lines
	f.Banner = banner(p, t.Versions, sources, "protoc-gen-cache's list caches in front of package "+pkg.ProtoPackage+"'s repositories.")
	return render(p, name, "cache.go.tmpl", f)
}

// resourceField is r's <S>Repositories field.
func resourceField(r *spec.Resource) string {
	return naming.PascalGo(naming.Pluralize(naming.SnakeCase(r.Message.GoIdent.GoName)))
}
