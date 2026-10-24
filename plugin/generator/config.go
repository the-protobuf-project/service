// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/store/plugin/entity"

	"github.com/the-protobuf-project/service/plugin/spec"
)

// Config is service.yaml: the store's layout block, repeated so this plugin
// derives the same database and schema for every resource the store did, and
// the per-package binding layout.
//
//	strip_version: true
//	datasources:
//	  - match: "protobuf.**"
//	    database: rfc
//	    schema: "{leaf}"
//	bindings:
//	  - match: "protobuf.**"
//	    grpc_package: "buf.build/gen/go/acme/rfc/grpc/go/{proto_dir}/{package}grpc"
//	    gateway_package: "example.com/gen/rfc/{proto_dir}/{package}gw"
//	    mcp_package: "example.com/gen/rfc/{proto_dir}/{package}mcp"
type Config struct {
	entity.LayoutConfig `yaml:",inline"`

	// Bindings place the gRPC, gateway and MCP packages of matching proto
	// packages, first match wins; an unset template falls back to the plugin
	// opt of the same name.
	Bindings []BindingRule `yaml:"bindings"`
}

// BindingRule is one bindings: entry.
type BindingRule struct {
	// Match is a proto package, or a prefix ending in ".**".
	Match          string `yaml:"match"`
	GrpcPackage    string `yaml:"grpc_package"`
	GatewayPackage string `yaml:"gateway_package"`
	MCPPackage     string `yaml:"mcp_package"`
}

// LoadConfig reads service.yaml; an empty path is no config.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("service: read config %s: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("service: parse config %s: %w", path, err)
	}
	return &c, nil
}

// Layout is the config's naming policy, nil without a config.
func (c *Config) Layout() protokit.LayoutResolver {
	if c == nil {
		return nil
	}
	return entity.Layout(&c.LayoutConfig)
}

// bindingsFor resolves a proto package's binding templates over defaults.
func (c *Config) bindingsFor(defaults spec.Bindings) func(string) spec.Bindings {
	return func(pkg string) spec.Bindings {
		out := defaults
		if c == nil {
			return out
		}
		for _, r := range c.Bindings {
			if !matches(r.Match, pkg) {
				continue
			}
			if r.GrpcPackage != "" {
				out.Grpc = r.GrpcPackage
			}
			if r.GatewayPackage != "" {
				out.Gateway = r.GatewayPackage
			}
			if r.MCPPackage != "" {
				out.MCP = r.MCPPackage
			}
			return out
		}
		return out
	}
}

func matches(pattern, pkg string) bool {
	if prefix, ok := strings.CutSuffix(pattern, ".**"); ok {
		return pkg == prefix || strings.HasPrefix(pkg, prefix+".")
	}
	return pattern == pkg
}
