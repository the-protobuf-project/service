// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package rustgen

// app.go renders the Rust application shell app=<name> asks for, the twin of
// the Go one: the typed configuration loaded by runtime-rs's tpp-config (the
// embedded release defaults, the development profile, files, then
// <NAME>_ environment variables), every server assembled from the handlers
// the application registers, and runtime-rs's hybrid gRPC/REST/MCP server
// serving them. It provisions; the handlers are the application's.

import (
	"fmt"
	"path"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/naming"

	"github.com/the-protobuf-project/service/plugin/spec"
)

// appService is one service the shell assembles.
type appService struct {
	field  string // Handlers/Servers field, "calendars"
	path   string // module path of its generated types
	svc    *spec.Service
	hookOf []appHook
}

// appHook is one resource's hooks slot of a service.
type appHook struct {
	field string // Hooks field, "calendars_calendar"
	res   string // resource ident, "calendar"
	typ   string // the resource's Rust type
}

// renderApp renders app/ for model.
func (t *Target) renderApp(p *protogen.Plugin, model *spec.Model) error {
	name := t.Settings.App
	var services []appService
	count := map[string]int{}
	for _, pkg := range model.Packages {
		for _, svc := range pkg.Services {
			count[ident(svc.GoName)]++
		}
	}
	for _, pkg := range model.Packages {
		mod := t.Settings.ServiceModule
		for _, part := range strings.Split(pkg.ProtoPackage, ".") {
			mod += "::" + ident(part)
		}
		for _, svc := range pkg.Services {
			field := ident(svc.GoName)
			if count[field] > 1 {
				field = ident(leaf(pkg.ProtoPackage) + "_" + svc.GoName)
			}
			as := appService{field: field, path: mod, svc: svc}
			for _, r := range svc.Resources {
				res := ident(r.Message.GoIdent.GoName)
				as.hookOf = append(as.hookOf, appHook{field: field + "_" + strings.TrimPrefix(res, "r#"), res: res, typ: t.typ(r.Message)})
			}
			services = append(services, as)
		}
	}
	if len(services) == 0 {
		return fmt.Errorf("service: app=%s: the run declares no service to serve", name)
	}
	sources := []string{}
	for _, pkg := range model.Packages {
		sources = append(sources, pkgSources(pkg)...)
	}

	module := t.appModule(name, services)
	config, release, dev := appConfig(name), appRelease(name), appDev(name)
	if t.Settings.Store {
		module = t.appStore(module, services)
		config, release, dev = t.storeConfig(name, config, release, dev)
	}
	if t.Settings.CDN {
		module = appCDN(module)
		config, release = cdnConfig(name, config, release)
	}
	if err := t.emit(p, "app/mod.rs", sources, "Application shell of "+name+": the servers assembled from their handlers, on the hybrid server.", module); err != nil {
		return err
	}
	if err := t.emit(p, "app/config.rs", sources, "Application shell of "+name+": the typed configuration and its loader.", config); err != nil {
		return err
	}
	for _, f := range []struct{ file, note, body string }{
		{name + ".release.toml", "the release defaults, embedded in the binary", release},
		{name + ".dev.toml", "the development profile", dev},
	} {
		banner := strings.ReplaceAll(t.banner(p, sources, "Application shell of "+name+": "+f.note+"."), "//", "#")
		if _, err := p.NewGeneratedFile(path.Join("app", f.file), "").Write([]byte(banner + "\n\n" + f.body)); err != nil {
			return err
		}
	}
	return nil
}

func leaf(protoPackage string) string {
	parts := strings.Split(protoPackage, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := parts[i]; !versionSegment(p) {
			return p
		}
	}
	return parts[len(parts)-1]
}

// versionSegment reports a proto package's version segment: "v1", "v2".
func versionSegment(p string) bool {
	return len(p) > 1 && p[0] == 'v' && strings.Trim(p[1:], "0123456789") == ""
}

func (t *Target) appModule(name string, services []appService) string {
	var b strings.Builder
	sx := t.Settings.ServiceModule + "::servicex"
	fmt.Fprintf(&b, `//! %[1]s's application shell: the configuration (config), every server
//! assembled from the handlers the application registers, and runtime-rs's
//! hybrid server serving them on gRPC, REST and MCP. The shell decides no
//! behaviour: the handlers and hooks are the application's.
//!
//! `+"```ignore"+`
//! #[tokio::main]
//! async fn main() -> Result<(), Box<dyn std::error::Error>> {
//!     let handlers = app::Handlers { /* one per RPC */ ..Default::default() };
//!     app::run(app::Provision::new(DESCRIPTORS, handlers)).await?;
//!     Ok(())
//! }
//! `+"```"+`

pub mod config;

use %[2]s;
use std::sync::Arc;

/// The handlers of every service: what the application registers, one per
/// RPC, the request in and the response out.
#[derive(Default, Clone)]
pub struct Handlers {
`, name, sx)
	for _, s := range services {
		fmt.Fprintf(&b, "/// %s.\npub %s: %s::%sHandlers,\n", s.svc.FullName, s.field, s.path, s.svc.GoName)
	}
	b.WriteString("}\n\n/// The hooks around each resource's standard methods, per service; unset runs none.\n#[derive(Default, Clone)]\npub struct Hooks {\n")
	for _, s := range services {
		for _, h := range s.hookOf {
			fmt.Fprintf(&b, "/// %s's %s hooks.\npub %s: Option<Arc<dyn servicex::Hooks<%s>>>,\n", s.svc.GoName, strings.TrimPrefix(h.res, "r#"), h.field, h.typ)
		}
	}
	b.WriteString(`}

/// Adjusts the hybrid server before it serves: interceptors, layers, more
/// services.
pub type Customize = Box<dyn FnOnce(grpc::HybridServer) -> grpc::HybridServer + Send>;

/// What the application provisions.
pub struct Provision {
    /// The encoded FileDescriptorSet of the schema and its imports: the
    /// field behavior the generated checks read, and the buf.validate rules
    /// and reflection the server serves.
    pub descriptors: &'static [u8],
    /// One handler per RPC.
    pub handlers: Handlers,
    /// The hooks around the standard methods.
    pub hooks: Hooks,
    /// The last word on the hybrid server.
    pub customize: Option<Customize>,
}

impl Provision {
    /// A provision of descriptors and handlers, with no hooks.
    pub fn new(descriptors: &'static [u8], handlers: Handlers) -> Self {
        Self {
            descriptors,
            handlers,
            hooks: Hooks::default(),
            customize: None,
        }
    }
}

/// Every server, built.
pub struct Servers {
`)
	for _, s := range services {
		fmt.Fprintf(&b, "/// %s.\npub %s: %s::%sServer,\n", s.svc.FullName, s.field, s.path, s.svc.GoName)
	}
	b.WriteString(`}

/// What can stop the shell.
#[derive(Debug)]
pub enum Error {
    /// The configuration did not load or validate.
    Config(::config::Error),
    /// The descriptors did not decode.
    Descriptors(String),
    /// Services were provisioned without a handler for some RPCs.
    Missing(Vec<servicex::MissingHandlers>),
    /// The hybrid server failed.
    Server(grpc::Error),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Config(e) => write!(f, "{e}"),
            Error::Descriptors(e) => write!(f, "descriptors: {e}"),
            Error::Missing(m) => {
                write!(f, "the provision is incomplete:")?;
                for m in m {
                    write!(f, "\n  {m}")?;
                }
                Ok(())
            }
            Error::Server(e) => write!(f, "server: {e}"),
        }
    }
}

impl std::error::Error for Error {}

/// Builds every server over options from handlers and hooks, naming every
/// RPC of every service left without a handler.
pub fn assemble(
    options: servicex::Options,
    handlers: Handlers,
    hooks: Hooks,
) -> Result<Servers, Vec<servicex::MissingHandlers>> {
    let mut missing = Vec::new();
`)
	for _, s := range services {
		fmt.Fprintf(&b, "let %s = match %s::%sServer::new(options.clone(), handlers.%s) {\nOk(server) => Some(server", s.field, s.path, s.svc.GoName, s.field)
		for _, h := range s.hookOf {
			fmt.Fprintf(&b, "\n.with_%s_hooks_shared(hooks.%s.clone().unwrap_or_else(|| Arc::new(servicex::NopHooks)))", h.res, h.field)
		}
		b.WriteString("),\nErr(e) => {\nmissing.push(e);\nNone\n}\n};\n")
	}
	b.WriteString("if !missing.is_empty() {\nreturn Err(missing);\n}\nOk(Servers {\n")
	for _, s := range services {
		fmt.Fprintf(&b, "%s: %s.expect(\"built: none is missing\"),\n", s.field, s.field)
	}
	b.WriteString(`})
}

/// The hybrid server over servers, configured by cfg: every service on gRPC,
/// with reflection and buf.validate over descriptors, CORS, TLS, MCP and the
/// shutdown timeout as the configuration says.
pub fn hybrid_server(
    cfg: &config::AppConfig,
    servers: Servers,
    descriptors: &'static [u8],
    customize: Option<Customize>,
) -> Result<grpc::HybridServer, Error> {
    let srv = &cfg.server;
    let mut server = grpc::HybridServer::new(cfg.options())
        .with_reflection(descriptors)
        .with_validation()
        .with_shutdown_timeout(srv.shutdown_timeout())
`)
	for _, s := range services {
		fmt.Fprintf(&b, ".with_service(servers.%s.into_service())\n", s.field)
	}
	b.WriteString(`;
    if !srv.mcp.path.is_empty() {
        server = server.with_mcp_endpoint(srv.mcp.path.clone());
    }
    if srv.mcp.stateless {
        server = server.with_stateless_mcp();
    }
    if srv.cors.enabled {
        let policy = srv.cors.policy();
        policy.validate().map_err(Error::Server)?;
        server = server.with_cors(policy);
    }
    if srv.tls.enabled() {
        server = server.with_certificates(srv.tls.cert_file.clone(), srv.tls.key_file.clone());
    }
    if let Some(customize) = customize {
        server = customize(server);
    }
    Ok(server)
}

/// Runs the application to completion: loads the configuration, assembles
/// every server from the provision, serves them until SIGINT or SIGTERM,
/// then stops gracefully.
pub async fn run(provision: Provision) -> Result<(), Error> {
    let (_, cfg) = config::load().map_err(Error::Config)?;
    let pool = prost_reflect::DescriptorPool::decode(provision.descriptors)
        .map_err(|e| Error::Descriptors(e.to_string()))?;
    let servers = assemble(servicex::Options::new(pool), provision.handlers, provision.hooks)
        .map_err(Error::Missing)?;
    hybrid_server(&cfg, servers, provision.descriptors, provision.customize)?
        .serve()
        .await
        .map_err(Error::Server)
}
`)
	return b.String()
}

func appConfig(name string) string {
	return fmt.Sprintf(`//! %[1]s's configuration, loaded by runtime-rs's tpp-config: the release
//! defaults compiled into the binary, the development profile
//! (%[1]s.dev.toml in the working directory, or embedded with
//! %[2]sPROFILE=dev), configuration files (%[2]sCONFIG), then %[2]s
//! environment variables, which override everything:
//! %[2]sSERVER__GRPC__PORT=50051. Sections of the application's own are
//! read from the same Config with Config::section.

use serde::Deserialize;

/// The release defaults, compiled into the binary.
pub const RELEASE: &str = include_str!("%[1]s.release.toml");
/// The development profile, compiled in for %[2]sPROFILE=dev.
pub const DEV: &str = include_str!("%[1]s.dev.toml");

/// The loader of %[1]s's configuration, for an application that adds
/// layers or providers of its own before loading.
pub fn loader() -> ::config::Loader {
    ::config::Loader::new(%[3]q)
        .release(::config::Source::toml(RELEASE))
        .dev_profile(::config::Source::toml(DEV))
}

/// Loads and validates the configuration: the merged layers, for the
/// application's own sections, and the shell's typed part.
pub fn load() -> Result<(::config::Config, AppConfig), ::config::Error> {
    let config = loader().load()?;
    let app = config.extract_validated()?;
    Ok((config, app))
}

/// The shell's part of the configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct AppConfig {
    /// Service identity.
    pub app: AppMeta,
    /// The hybrid server.
    pub server: ServerConfig,
}

/// Service identity.
#[derive(Debug, Clone, Deserialize)]
pub struct AppMeta {
    /// The service name.
    pub name: String,
    /// The service version.
    pub version: String,
    /// The long-form description shown to clients and MCP.
    #[serde(default)]
    pub description: String,
}

/// The hybrid gRPC/REST/MCP server.
#[derive(Debug, Clone, Deserialize)]
pub struct ServerConfig {
    /// development, debug, staging or production.
    pub environment: String,
    /// Starts the REST gateway alongside gRPC.
    #[serde(default)]
    pub enable_http: bool,
    /// Registers the gRPC health service.
    #[serde(default)]
    pub enable_health: bool,
    /// Starts the MCP server.
    #[serde(default)]
    pub enable_mcp: bool,
    /// Serves HTTP/3 on the HTTP port + 1; requires TLS.
    #[serde(default)]
    pub experimental_http3: bool,
    /// The gRPC listener.
    pub grpc: Listen,
    /// The REST gateway listener.
    pub http: Listen,
    /// The MCP server.
    #[serde(default)]
    pub mcp: Mcp,
    /// The certificate and key; both set enables TLS.
    #[serde(default)]
    pub tls: Tls,
    /// The HTTP port's cross-origin policy.
    #[serde(default)]
    pub cors: Cors,
    /// How long a stop waits for in-flight requests; 0 keeps the runtime's
    /// default.
    #[serde(default)]
    pub shutdown_timeout_seconds: u64,
}

impl ServerConfig {
    /// The shutdown timeout, defaulted.
    pub fn shutdown_timeout(&self) -> std::time::Duration {
        match self.shutdown_timeout_seconds {
            0 => grpc::DEFAULT_SHUTDOWN_TIMEOUT,
            s => std::time::Duration::from_secs(s),
        }
    }
}

/// A listener's host and port.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Listen {
    /// The interface to bind.
    #[serde(default)]
    pub host: String,
    /// The port.
    pub port: u16,
}

/// The MCP server.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Mcp {
    /// The interface to bind (ignored for stdio).
    #[serde(default)]
    pub host: String,
    /// The port (ignored for stdio).
    #[serde(default)]
    pub port: u16,
    /// stdio, streamable-http or sse.
    #[serde(default)]
    pub transport: String,
    /// Where one MCP server carrying every service's tools answers.
    #[serde(default)]
    pub path: String,
    /// Serves streamable HTTP without sessions.
    #[serde(default)]
    pub stateless: bool,
}

/// The certificate and key.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Tls {
    /// The PEM certificate.
    #[serde(default)]
    pub cert_file: String,
    /// The PEM key.
    #[serde(default)]
    pub key_file: String,
}

impl Tls {
    /// Whether both a certificate and a key are configured.
    pub fn enabled(&self) -> bool {
        !self.cert_file.is_empty() && !self.key_file.is_empty()
    }
}

/// The HTTP port's cross-origin policy. Lists set from the environment are
/// comma-separated.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Cors {
    /// Turns the policy on.
    #[serde(default)]
    pub enabled: bool,
    /// Exact origins, or "*".
    #[serde(default, deserialize_with = "::config::de::comma_list")]
    pub allowed_origins: Vec<String>,
    /// Methods a preflight allows; empty takes the runtime's defaults.
    #[serde(default, deserialize_with = "::config::de::comma_list")]
    pub allowed_methods: Vec<String>,
    /// Headers a preflight allows; empty takes the runtime's defaults.
    #[serde(default, deserialize_with = "::config::de::comma_list")]
    pub allowed_headers: Vec<String>,
    /// Response headers a browser script may read.
    #[serde(default, deserialize_with = "::config::de::comma_list")]
    pub exposed_headers: Vec<String>,
    /// Lets browsers send cookies and HTTP authentication.
    #[serde(default)]
    pub allow_credentials: bool,
    /// How long a browser may cache a preflight.
    #[serde(default)]
    pub max_age_seconds: u64,
}

impl Cors {
    /// The policy as the hybrid server takes it.
    pub fn policy(&self) -> grpc::CorsPolicy {
        grpc::CorsPolicy {
            allowed_origins: self.allowed_origins.clone(),
            allowed_methods: self.allowed_methods.clone(),
            allowed_headers: self.allowed_headers.clone(),
            exposed_headers: self.exposed_headers.clone(),
            allow_credentials: self.allow_credentials,
            max_age: std::time::Duration::from_secs(self.max_age_seconds),
        }
    }
}

impl ::config::Validate for AppConfig {
    fn validate(&self) -> Result<(), String> {
        let srv = &self.server;
        if self.app.name.is_empty() || self.app.version.is_empty() {
            return Err(format!("app.name={:?} app.version={:?} (want both)", self.app.name, self.app.version));
        }
        if srv.environment.parse::<grpc::options::ServerEnvironment>().is_err() {
            return Err(format!(
                "server.environment={:?} (want development|debug|staging|production)",
                srv.environment
            ));
        }
        if srv.enable_mcp && srv.mcp.transport.parse::<grpc::options::McpTransport>().is_err() {
            return Err(format!(
                "server.mcp.transport={:?} (want stdio|streamable-http|sse)",
                srv.mcp.transport
            ));
        }
        if !srv.mcp.path.is_empty() && !srv.mcp.path.starts_with('/') {
            return Err(format!("server.mcp.path={:?} (want an absolute path such as /mcp)", srv.mcp.path));
        }
        if srv.cors.enabled && srv.cors.allowed_origins.is_empty() {
            return Err("server.cors.enabled requires server.cors.allowed_origins".into());
        }
        if srv.cors.allow_credentials && srv.cors.allowed_origins.iter().any(|o| o == "*") {
            return Err("server.cors.allow_credentials cannot be combined with the \"*\" origin".into());
        }
        if srv.experimental_http3 && !srv.tls.enabled() {
            return Err("server.experimental_http3 requires server.tls.cert_file and server.tls.key_file".into());
        }
        Ok(())
    }
}

impl AppConfig {
    /// The hybrid server's options, from the validated configuration.
    pub fn options(&self) -> grpc::options::Options {
        let srv = &self.server;
        grpc::options::Options {
            service_name: self.app.name.clone(),
            description: self.app.description.clone(),
            version: self.app.version.clone(),
            grpc: grpc::options::GrpcOptions::new(srv.grpc.host.clone(), srv.grpc.port),
            http: grpc::options::HttpOptions::new(srv.http.host.clone(), srv.http.port),
            mcp: grpc::options::McpOptions {
                host: srv.mcp.host.clone(),
                port: srv.mcp.port,
                transport: srv.mcp.transport.parse().ok(),
            },
            enable_http: srv.enable_http,
            enable_health: srv.enable_health,
            enable_mcp: srv.enable_mcp,
            environment: srv.environment.parse().unwrap_or_default(),
            experimental_http3: srv.experimental_http3,
            ..Default::default()
        }
    }
}
`, name, envPrefix(name), name)
}

func envPrefix(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(name)) + "_"
}

func appRelease(name string) string {
	return fmt.Sprintf(`# Production configuration for %[1]s: production-safe, non-secret topology,
# compiled into the binary. Secrets stay blank here and come from %[2]s
# environment variables at deploy time.

[app]
name = %[1]q
version = "v0.1.0"
description = %[3]q

[server]
# development, debug, staging or production.
environment = "production"
enable_http = true
enable_health = true
enable_mcp = true
experimental_http3 = false
# How long a stop waits for in-flight requests; 0 = the runtime's default.
shutdown_timeout_seconds = 20

[server.grpc]
host = "0.0.0.0"
port = 50051

[server.http]
host = "0.0.0.0"
port = 8080

[server.mcp]
host = "0.0.0.0"
port = 8081
# stdio, streamable-http or sse.
transport = "streamable-http"
path = "/mcp"
stateless = true

[server.cors]
enabled = false
allowed_origins = []
max_age_seconds = 600

[server.tls]
cert_file = ""
key_file = ""
`, name, envPrefix(name), naming.PascalGo(name))
}

func appDev(name string) string {
	return fmt.Sprintf(`# Local development profile for %[1]s, over the release defaults: from
# %[1]s.dev.toml in the working directory when present, or embedded with
# %[2]sPROFILE=dev. Keys set here win; the rest fall through.

[server]
environment = "debug"
`, name, envPrefix(name))
}

// pkgSources are the source files of pkg.
func pkgSources(pkg *spec.Package) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range pkg.Services {
		if f := s.Proto.Location.SourceFile; !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
