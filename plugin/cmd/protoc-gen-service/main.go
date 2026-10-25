// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// protoc-gen-service generates the AIP standard-method service layer over
// protoc-gen-store's generated repositories: one gRPC server per service, a
// hooks interface for the business logic around each standard method, the
// registration of every server with runtime-go's HybridServer, and the
// CloudEvents types resources declare.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/header"

	"github.com/the-protobuf-project/service/plugin/generator"
	"github.com/the-protobuf-project/service/plugin/spec"
)

// version is set by -ldflags "-X main.version=..." at release.
var version = ""

func resolveVersion() string {
	if version != "" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	// A build from a checkout carries a VCS pseudo-version (and "+dirty" with
	// local edits), which changes with every commit; stamping it would make
	// generated output differ between identical builds. Only a tagged module
	// version (go install ...@vX.Y.Z) is reported.
	if v := bi.Main.Version; v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) && !strings.Contains(v, "+dirty") {
		return v
	}
	return "dev"
}

// pseudoVersion matches the timestamp-and-hash part of a Go pseudo-version.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)

func main() {
	v := resolveVersion()
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("protoc-gen-service %s\n", v)
		return
	}
	header.SetTool("protoc-gen-service")

	var flags flag.FlagSet
	goModule := flags.String("go_module", "", "Go import path of this plugin's out directory (servicex lives under it)")
	repositoryModule := flags.String("repository_module", "", "Go import path of protoc-gen-store's repository output (its go_module opt)")
	configPath := flags.String("config", "", "path to service.yaml: the store's datasources layout and per-package bindings")
	suffix := flags.String("package_suffix", "svc", "suffix of each generated package's name: <go package><suffix>")
	grpcPkg := flags.String("grpc_package", "{import_path}", "template of the gRPC stubs' package: {import_path}, {package}, {proto_dir}")
	gatewayPkg := flags.String("gateway_package", "{import_path}", "template of the grpc-gateway package")
	mcpPkg := flags.String("mcp_package", "{import_path}", "template of the protoc-gen-mcp package")
	gateway := flags.Bool("gateway", true, "register REST gateway handlers for services with google.api.http rules")
	mcp := flags.Bool("mcp", true, "register protoc-gen-mcp's Serve<Service>MCP for each service")
	domain := flags.String("error_domain", "", "google.rpc.ErrorInfo domain; default each service's google.api.default_host")
	strict := flags.String("strict", "", "per-rule severity for schema problems, as protoc-gen-store takes it")

	protogen.Options{ParamFunc: flags.Set}.Run(func(p *protogen.Plugin) error {
		p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		cfg, err := generator.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		settings := generator.Settings{
			GoModule:         *goModule,
			RepositoryModule: *repositoryModule,
			PackageSuffix:    *suffix,
			Bindings:         spec.Bindings{Grpc: *grpcPkg, Gateway: *gatewayPkg, MCP: *mcpPkg},
			Gateway:          *gateway,
			MCP:              *mcp,
			ErrorDomain:      *domain,
		}
		return protokit.RunPlugin(p,
			protokit.Options{Target: generator.TargetName, Strict: *strict, Version: v},
			generator.Plugin(settings, cfg, generator.ResolveVersions(v)))
	})
}
