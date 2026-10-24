// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

// provenance.go stamps every generated file with what produced it: the plugin
// and protoc versions, the source protos, and the engine and vocabularies the
// names were derived through.

import (
	"fmt"
	"runtime/debug"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/header"
)

const (
	entityModule   = "github.com/the-protobuf-project/store"
	protokitModule = "github.com/the-protobuf-project/protokit"
)

// Unknown is the banner's spelling of a version the build did not record.
const Unknown = "(unknown)"

// Versions are the versions a banner names.
type Versions struct {
	Plugin string
	Entity string
	Engine string
}

// ResolveVersions reads the engine's and the entity reader's versions from the
// build info; plugin is the plugin's own.
func ResolveVersions(plugin string) Versions {
	return Versions{Plugin: orUnknown(plugin), Entity: moduleVersion(entityModule), Engine: moduleVersion(protokitModule)}
}

func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	for _, dep := range bi.Deps {
		if dep.Path == path && dep.Version != "" {
			return dep.Version
		}
	}
	return Unknown
}

// banner renders the header of one generated file.
func banner(p *protogen.Plugin, v Versions, sources []string, note string) string {
	return header.Render("//", header.Info{
		PluginVersion: orUnknown(v.Plugin),
		ProtocVersion: protocVersion(p),
		Source:        strings.Join(sources, ", "),
		Notes: []string{
			note,
			"annotations: entity.v1 " + orUnknown(v.Entity) + ", service.v1 " + orUnknown(v.Plugin),
			"engine:      protokit " + orUnknown(v.Engine),
		},
	})
}

func protocVersion(p *protogen.Plugin) string {
	v := p.Request.GetCompilerVersion()
	if v == nil {
		return ""
	}
	s := fmt.Sprintf("v%d.%d.%d", v.GetMajor(), v.GetMinor(), v.GetPatch())
	if suffix := v.GetSuffix(); suffix != "" {
		s += "-" + suffix
	}
	return s
}

func orUnknown(v string) string {
	if v == "" {
		return Unknown
	}
	return v
}
