# protoc-gen-service

`protoc-gen-service` generates the AIP standard-method service layer that
services otherwise write by hand, in **Go** and **Rust**: a server per gRPC
service that runs each standard method's checks (names, ids, masks, etags,
field behavior) and a hooks interface for the business logic around them; the
wiring that serves it; change feeds and record adapters; and error reasons
declared in proto (AIP-193).

It recognizes AIP-131..135 and AIP-164 by name and shape alone, so a
conforming API needs no annotation. A method that does not fit stays custom:
you write only those, and the generated server runs them through the same
tracing and error translation.

## Contents

- [Languages](#languages)
- [Two models: handlers (default) and `store=true`](#two-models-handlers-default-and-storetrue)
- [The standard methods](#the-standard-methods)
- [Setup](#setup)
  - [1. Install the plugin](#1-install-the-plugin)
  - [2. Add the dependencies](#2-add-the-dependencies)
  - [3. Write an AIP service](#3-write-an-aip-service)
  - [4. Generate and serve it](#4-generate-and-serve-it): [Go](#go-setup) · [Rust](#rust-setup)
  - [5. Persist through protoc-gen-store (`store=true`)](#5-persist-through-protoc-gen-store-storetrue)
  - [Troubleshooting](#troubleshooting)
- [Go](#go)
- [Rust](#rust)
- [Caching (`cache=true`)](#caching-cachetrue)
- [Reference](#reference)
- [Adding a language](#adding-a-language)
- [Developing](#developing)
- [License](#license)

## Languages

Both languages generate the same model from the same protos: the same checks,
hooks, error reasons and refusals, so a Go and a Rust service built from one API
behave alike.

| | Go | Rust (`lang=rust`) |
| --- | --- | --- |
| Servers | grpc-go servers; `RegisterAll` serves them on gRPC, REST (grpc-gateway) and MCP through [runtime-go](https://github.com/the-protobuf-project/runtime-go)'s `HybridServer` | tonic services (`into_service()`); with `app=`, served on gRPC, REST and MCP through [runtime-rs](https://github.com/the-protobuf-project/runtime-rs)'s hybrid server |
| Handler model (default) | `<S>Handlers` struct of funcs | `<S>Handlers` builder |
| `store=true` | over protoc-gen-store's Go repositories (`repox`) | over protoc-gen-store's Rust repositories (`tpp-database`) |
| `cache=true` | protoc-gen-cache's list caches over the repositories | protoc-gen-cache's record and list caches over the repositories |
| Error reasons | `errors.svc.go` + a Markdown reference | `errors.rs` |
| Change feeds, record adapters | `feeds.svc.go`, `adapters.svc.go` over runtime-go `events` and `grpc/idempotency` | `feeds.rs`, `adapters.rs`, standalone |
| App shell (`app=<name>`) | config, telemetry, database, server, `cmd/<name>`; needs `store=true` | config, handlers, assembly, hybrid server, `run`; with or without `store=true` |

Where one language has something the other doesn't yet, its section says so.

## Two models: handlers (default) and `store=true`

By default a generated server is a **handler model**: every RPC is answered
by a handler the application registers (`<S>Handlers`, the request in and
the response out: a query of your database, a call to another service), and
the server runs each standard method's checks and hooks around it. Nothing
about persistence is generated, and no other plugin's output is needed. Every
language generates the same model.

`store=true` generates **repository-backed** servers instead: each standard
method persists through [protoc-gen-store]'s generated repositories (Go:
`<S>Repositories` over `repox`; Rust: runtime-rs's
`database::repository::Repository`). This is the output the examples use,
with the change feeds, store adapters and application shell built over those
repositories. `cache=true` (with `store=true`) also wires
[protoc-gen-cache]'s caches from `cache.v1` annotations; see
[Caching](#caching-cachetrue).

| | handler model (default) | `store=true` |
|---|---|---|
| Server | every RPC answered by a registered handler; a server with an RPC left unanswered is refused (`MissingHandlers`) | standard methods answered from the repositories; custom methods yours (Go: `WithLogic`; Rust: handlers) |
| Needs | Go: `go_module`; Rust: nothing more | protoc-gen-store's repository output; Go: `repository_module`; Rust: `rust_repository_module` |
| Feeds, store adapters | not generated (a warning names them) | generated |
| Go `app=` | refused: the Go shell builds over the repositories | generated |

## The standard methods

Around every standard method's handler, in every language, the server runs:

| Method | Generated around the handler |
|---|---|
| Get, Purge | the name's shape |
| List | the parent's shape (AIP-159 `-` allowed), `ParentCheck`, `page_size` |
| Create | the id (AIP-122) and parent; the resource named, OUTPUT_ONLY cleared, REQUIRED checked; `ParentCheck`, `Validate`, `BeforeCreate`; `validate_only`; `AfterCreate` |
| Update | the name and `update_mask`; with the stored record (read through the service's own Get handler) its etag, the merge, IMMUTABLE and REQUIRED fields; `allow_missing`; `Validate`, `BeforeUpdate`; `validate_only`; `AfterUpdate`. The handler receives the record to write and the canonical mask, carrying the request's etag |
| Delete, Undelete | the name; with the stored record its etag and deleted state; `allow_missing`, `force`, `validate_only`; the before/after hooks |
| Custom (unary or streaming) | nothing: straight to its handler |

An Update handler receives the merged record carrying the caller's etag and
the canonical mask; handlers that write enforce the etag in the write. The
standard outcomes are returned as errors (Go: `servicex.ErrNotFound`,
`ErrAlreadyExists`, `ErrConflict`, `ErrInvalidArgument`; Rust: a
`tonic::Status`), rendered with AIP-193 reasons.

With `store=true`, the repositories answer each method:

| Method | Generated behaviour |
|---|---|
| Get (131) | name shape (`MALFORMED_NAME`), repository Get, `NOT_FOUND` naming the resource. |
| List (132) | parent shape (AIP-159 `-` allowed), `ParentCheck` hook, `page_size`, `page_token`, `filter`, `order_by`, `show_deleted`, `read_mask` onto `repox.ListInput`. |
| Create (133) | `<resource>_id` (AIP-122, `INVALID_ID`; empty mints one), parent shape and `ParentCheck`, OUTPUT_ONLY fields cleared, REQUIRED fields checked together (`REQUIRED_FIELD`), `Validate`, `BeforeCreate`, `validate_only`, `ALREADY_EXISTS`, `AfterCreate`. |
| Update (134) | `update_mask` (`*`, absent = populated fields, JSON names; `UNKNOWN_MASK_PATH`, `NOTHING_TO_UPDATE`), IMMUTABLE fields (`IMMUTABLE_FIELD`), etag (`ABORTED`/`CONCURRENT_MODIFICATION`), `allow_missing` (creates), `validate_only` (previews), hooks. |
| Delete (135) | etag, `allow_missing`, `validate_only`, `force` passed to `BeforeDelete`; soft delete when the resource has an OUTPUT_ONLY `delete_time`; returns Empty or the soft-deleted resource. |
| Undelete (164) | etag, `validate_only`, `BeforeUndelete`; `ALREADY_EXISTS` when not deleted. |
| Purge | single-resource `Purge<R>(name)` returning Empty. AIP-165's Purge is a bulk LRO, so this is an extension; leave it out of AIP-linted APIs. |

protovalidate is not repeated here: the runtime runs it in front of every
surface (Go: runtime-go's `WithValidation`; Rust: the hybrid server's
buf.validate over the descriptors).

## Setup

From nothing to a running service in Go or Rust, in the default handler model.
Steps 1–3 are the same for every language.

**You need:** [buf](https://buf.build/docs/installation) (or `protoc`) and your
language's toolchain. Go is **not** required unless you generate Go; for Rust,
`rustfmt` must be on the generator's `PATH` (`rustup component add rustfmt`),
since generation formats and parses every Rust file it writes.

### 1. Install the plugin

Install it any one way, and make sure it is on your `PATH`:

```sh
# Homebrew (macOS / Linux)
brew install the-protobuf-project/tap/protoc-gen-service

# Or a prebuilt binary (linux / darwin / windows, amd64 / arm64) from
# https://github.com/the-protobuf-project/service/releases, unpacked onto your PATH

# Or build it with Go
go install github.com/the-protobuf-project/service/plugin/cmd/protoc-gen-service@latest
```

Check it: `protoc-gen-service --version`. In CI, pin a release: the version is
written into every generated file's banner.

Go also uses the stock gRPC and MCP plugins beside it:

```sh
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
go install github.com/the-protobuf-project/mcp/plugin/cmd/protoc-gen-mcp@v1.7.5
```

### 2. Add the dependencies

```yaml
# buf.yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/googleapis/googleapis          # google.api.resource, http, field_behavior
  - buf.build/bufbuild/protovalidate         # buf.validate, if your protos use it
  - buf.build/the-protobuf-project/service   # service.v1: only for the optional options
```

```sh
buf dep update
```

### 3. Write an AIP service

Any AIP-conforming service works as it is; no annotation is required. A
resource and its standard methods:

```proto
// proto/acme/directory/v1/directory.proto
syntax = "proto3";
package acme.directory.v1;

import "google/api/annotations.proto";
import "google/api/client.proto";
import "google/api/field_behavior.proto";
import "google/api/resource.proto";
import "google/protobuf/empty.proto";
import "google/protobuf/field_mask.proto";
import "google/protobuf/timestamp.proto";

service Organizations {
  option (google.api.default_host) = "directory.acme.example";

  rpc GetOrganization(GetOrganizationRequest) returns (Organization) {
    option (google.api.http) = {get: "/v1/{name=organizations/*}"};
  }
  rpc ListOrganizations(ListOrganizationsRequest) returns (ListOrganizationsResponse) {
    option (google.api.http) = {get: "/v1/organizations"};
  }
  rpc CreateOrganization(CreateOrganizationRequest) returns (Organization) {
    option (google.api.http) = {post: "/v1/organizations" body: "organization"};
  }
  rpc UpdateOrganization(UpdateOrganizationRequest) returns (Organization) {
    option (google.api.http) = {patch: "/v1/{organization.name=organizations/*}" body: "organization"};
  }
  rpc DeleteOrganization(DeleteOrganizationRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {delete: "/v1/{name=organizations/*}"};
  }
}

message Organization {
  option (google.api.resource) = {
    type: "directory.acme.example/Organization"
    pattern: "organizations/{organization}"
    singular: "organization"
    plural: "organizations"
  };
  string name = 1 [(google.api.field_behavior) = IDENTIFIER];
  string display_name = 2 [(google.api.field_behavior) = REQUIRED];
  string registration = 3 [(google.api.field_behavior) = IMMUTABLE];
  google.protobuf.Timestamp create_time = 4 [(google.api.field_behavior) = OUTPUT_ONLY];
  google.protobuf.Timestamp update_time = 5 [(google.api.field_behavior) = OUTPUT_ONLY];
  string etag = 6 [(google.api.field_behavior) = OUTPUT_ONLY];
}

message GetOrganizationRequest {
  string name = 1 [
    (google.api.field_behavior) = REQUIRED,
    (google.api.resource_reference).type = "directory.acme.example/Organization"
  ];
}

message ListOrganizationsRequest {
  int32 page_size = 1 [(google.api.field_behavior) = OPTIONAL];
  string page_token = 2 [(google.api.field_behavior) = OPTIONAL];
  string filter = 3 [(google.api.field_behavior) = OPTIONAL];
  string order_by = 4 [(google.api.field_behavior) = OPTIONAL];
}

message ListOrganizationsResponse {
  repeated Organization organizations = 1;
  string next_page_token = 2;
}

message CreateOrganizationRequest {
  Organization organization = 1 [(google.api.field_behavior) = REQUIRED];
  string organization_id = 2 [(google.api.field_behavior) = OPTIONAL]; // empty mints one
  bool validate_only = 3 [(google.api.field_behavior) = OPTIONAL];
}

message UpdateOrganizationRequest {
  Organization organization = 1 [(google.api.field_behavior) = REQUIRED];
  google.protobuf.FieldMask update_mask = 2 [(google.api.field_behavior) = OPTIONAL];
  bool allow_missing = 3 [(google.api.field_behavior) = OPTIONAL];
  bool validate_only = 4 [(google.api.field_behavior) = OPTIONAL];
}

message DeleteOrganizationRequest {
  string name = 1 [
    (google.api.field_behavior) = REQUIRED,
    (google.api.resource_reference).type = "directory.acme.example/Organization"
  ];
  string etag = 2 [(google.api.field_behavior) = OPTIONAL];
  bool allow_missing = 3 [(google.api.field_behavior) = OPTIONAL];
  bool validate_only = 4 [(google.api.field_behavior) = OPTIONAL];
}
```

[`examples/proto/acme/directory/v1/directory.proto`](examples/proto/acme/directory/v1/directory.proto)
is this service with Undelete, soft delete, validation and a nested resource.

### 4. Generate and serve it

> [!NOTE]
> protoc asks every plugin for a Go import path, even when nothing it writes is
> Go. The templates below supply one with buf's
> [managed mode](https://buf.build/docs/generate/managed-mode/)
> (`go_package_prefix`), so your protos need no `option go_package`; for Rust
> the value is never used.

<a id="go-setup"></a>
#### Go

The messages, gRPC stubs, REST gateway and MCP bindings come from their own
plugins; protoc-gen-service writes the servers over them.

```yaml
# buf.gen.yaml
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: example.com/app/gen/go
  disable:
    - module: buf.build/googleapis/googleapis
    - module: buf.build/bufbuild/protovalidate
inputs:
  - directory: proto
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.11
    out: gen/go
    opt: module=example.com/app/gen/go
  - local: protoc-gen-go-grpc
    out: gen/go
    opt: module=example.com/app/gen/go
  - remote: buf.build/grpc-ecosystem/gateway:v2.27.4
    out: gen/go
    opt: module=example.com/app/gen/go
  - local: protoc-gen-mcp
    out: gen/go
    opt: [lang=go, module=example.com/app/gen/go]
  - local: protoc-gen-service
    out: gen/service
    strategy: all                         # one run sees every service
    opt:
      - go_module=example.com/app/gen/service
```

```sh
buf generate
# runtime-go is consumed at main. Require the grpc module's siblings at main
# too: grpc's go.mod pins older ones that only its own workspace replaces.
go get github.com/the-protobuf-project/runtime-go/grpc@main \
  github.com/the-protobuf-project/runtime-go/observability@main \
  github.com/the-protobuf-project/runtime-go/telemetry@main \
  github.com/the-protobuf-project/runtime-go/agents@main
go mod tidy
```

Register a handler per RPC and serve:

```go
import (
    directoryv1 "example.com/app/gen/go/acme/directory/v1"
    "example.com/app/gen/service/acme/directory/v1/directoryv1svc"
    "github.com/the-protobuf-project/runtime-go/grpc"
    "github.com/the-protobuf-project/runtime-go/grpc/options"
    "github.com/the-protobuf-project/runtime-go/grpc/servicex"
)

srv, err := directoryv1svc.NewOrganizationsServer(directoryv1svc.OrganizationsHandlers{
    GetOrganization: func(ctx context.Context, req *directoryv1.GetOrganizationRequest) (*directoryv1.Organization, error) {
        return db.Get(ctx, req.GetName()) // servicex.ErrNotFound when there is none
    },
    // ... one per RPC; a missing one is refused, naming it
}, directoryv1svc.NopOrganizationsHooks{})
if err != nil {
    log.Fatal(err)
}

hs := grpc.NewHybridServer(options.Options{
    ServiceName: "directory",
    GRPC:        options.GRPCOptions{Port: 9090},
    HTTP:        options.HTTPOptions{Port: 8080},
    EnableHTTP:  true,
    EnableMCP:   true,
}, append(directoryv1svc.RegisterAll(directoryv1svc.Servers{Organizations: srv}),
    grpc.WithValidation())...) // protovalidate in front of gRPC, REST and MCP
log.Fatal(hs.Start())
```

Next: [Go](#go) covers hooks, custom methods and the application shell.

<a id="rust-setup"></a>
#### Rust

protoc-gen-service writes the tonic service implementations over runtime-rs's
`grpc::servicex`; the prost messages and tonic bindings come from
`tonic-prost-build` in your `build.rs`.

```yaml
# buf.gen.yaml
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: example.com/unused             # see the note above
  disable:
    - module: buf.build/googleapis/googleapis
    - module: buf.build/bufbuild/protovalidate
inputs:
  - directory: proto
plugins:
  - local: protoc-gen-service
    out: src/service
    strategy: all
    opt:
      - lang=rust
```

```sh
buf generate
buf build proto --exclude-source-info -o descriptor.binpb
```

```toml
[dependencies]
# The servicex runtime the generated services build on, without the hybrid
# server (default-features = false). Add features = ["servicex-store"] for
# store=true, or drop default-features = false to serve REST and MCP too.
grpc = { package = "tpp-grpc", git = "https://github.com/the-protobuf-project/runtime-rs", branch = "main", default-features = false }
tonic = "0.14"
tonic-prost = "0.14"
prost = "0.14"
prost-types = "0.14"
prost-reflect = { version = "0.16", features = ["serde"] }
tokio = { version = "1", features = ["macros", "rt-multi-thread"] }

[build-dependencies]
prost = "0.14"
prost-types = "0.14"
tonic-prost-build = "0.14"
```

```rust
// build.rs
use prost::Message;

fn main() {
    println!("cargo:rerun-if-changed=descriptor.binpb");
    let set = prost_types::FileDescriptorSet::decode(std::fs::read("descriptor.binpb").unwrap().as_slice()).unwrap();
    tonic_prost_build::configure()
        // buf.validate's rules are read from the descriptors at run time.
        .extern_path(".buf.validate", "::protovalidate_rules_unused")
        .include_file("proto.rs") // one module tree, nested by proto package
        .compile_fds(set)
        .unwrap();
}
```

```rust
// src/lib.rs: the messages at crate::pb, the services at crate::service
pub mod pb {
    include!(concat!(env!("OUT_DIR"), "/proto.rs"));
}

#[path = "service/mod.rs"]
pub mod service;

pub static DESCRIPTORS: &[u8] = include_bytes!("../descriptor.binpb");
```

Register a handler per RPC and serve:

```rust
use my_app::pb::acme::directory::v1 as pb;
use my_app::service::{acme::directory::v1 as ds, servicex};

let pool = prost_reflect::DescriptorPool::decode(my_app::DESCRIPTORS)?;
let handlers = ds::OrganizationsHandlers::default()
    .get_organization(|_ctx, req: pb::GetOrganizationRequest| async move { db.get(&req.name).await })
    // ... one per RPC; a Delete handler returns Ok(())
    ;
let server = ds::OrganizationsServer::new(servicex::Options::new(pool), handlers)?; // refuses a missing handler
tonic::transport::Server::builder()
    .add_service(server.into_service())
    .serve("127.0.0.1:9090".parse()?)
    .await?;
```

For REST and MCP beside gRPC, generate the application shell (`app=<name>`):
see [Rust](#rust). Next: [Rust](#rust) covers the layout, hooks and the shell.

### 5. Persist through protoc-gen-store (`store=true`)

`store=true` answers every standard method from
[protoc-gen-store]'s generated repositories instead of your handlers; custom
methods stay yours. Run protoc-gen-store beside this plugin (and
[protoc-gen-cache] for `cache=true`), with the repositories of your language:

| | Go | Rust |
| --- | --- | --- |
| protoc-gen-store | `target=gorm` + `target=repository` ([setup](https://github.com/the-protobuf-project/store#go-setup)) | `lang=rust target=repository` into `src/repository` ([setup](https://github.com/the-protobuf-project/store#rust-setup)) |
| protoc-gen-service | `store=true`, `repository_module=<store's repository go_module>` | `lang=rust`, `store=true` (`rust_repository_module`, default `crate::repository`) |
| Complete example | [`buf.gen.example.yaml`](buf.gen.example.yaml), [`examples/`](examples) | [`examples/rust-store`](examples/rust-store) |

Both examples are built and tested in CI.

### Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `protoc-gen-service: program not found or is not executable` | The binary isn't on `PATH`. Check `protoc-gen-service --version`. |
| `unable to determine Go import path` | Add the `managed:` block from the templates above, or set `option go_package`. |
| Go: `New<S>Server` returns `servicex.MissingHandlers` / Rust: `new` returns `MissingHandlers` | An RPC has no handler. The error lists them; register each (a server never starts with an RPC nothing answers). |
| Go: `c.Tracer undefined (type *observability.Client …)` building runtime-go/grpc | runtime-go's siblings resolved to old versions. Require them at `main` (step 4, Go). |
| Go: `undefined: directoryv1.RegisterOrganizationsHandlerFromEndpoint` or `ServeOrganizationsMCP` | The gateway or MCP plugin didn't run. Add it, or turn the surface off with `gateway=false` / `mcp=false`. |
| Rust: generation fails with `rustfmt` not found | Install it on the generator's `PATH`: `rustup component add rustfmt`. |
| Rust: `unresolved import crate::pb::…` | The prost messages aren't at `crate::pb`, nested by package. Use `include_file("proto.rs")` as above, or set `rust_proto_module`. |
| A method you expected to be standard stays custom | It doesn't match AIP-131..135/164 by name and shape (request fields, response type, `google.api.resource`). The generated server leaves it to your code. |
| `app=` refused (Go) | The Go shell builds over the repositories: add `store=true`. |
| Generation fails naming a `datasources:` schema or binding template | Two imports would declare one name; set the named setting to separate them (imports are never aliased). |

## Go

Setup is in [Setup → Go](#go-setup).

### The handler model

Around each handler the server runs what [the standard methods](#the-standard-methods)
lists: the name's shape; List's parent, `ParentCheck` and `page_size`; Create's id,
parent, OUTPUT_ONLY clearing, REQUIRED, `ParentCheck`, `Validate`,
`BeforeCreate`, `validate_only`, `AfterCreate`; Update's mask and, with the
stored record read through the service's own Get handler, its etag,
IMMUTABLE fields, the merge, REQUIRED and `allow_missing`; Delete's and
Undelete's etag, deleted state, `allow_missing`, `force` and `validate_only`.
An Update handler receives the merged record carrying the caller's etag and
the canonical mask; handlers that write enforce the etag in the write.
Handlers return `servicex.ErrNotFound`, `ErrAlreadyExists`, `ErrConflict`
and `ErrInvalidArgument` (or errors wrapping them) for the standard
outcomes, rendered with the standard reasons (runtime-go's
[`grpc/servicex`](https://github.com/the-protobuf-project/runtime-go/tree/main/grpc/servicex)).
With `store=true` the translator also maps the repositories' reference errors.

### What it generates

For each proto package, into `<proto package path>/<go package>svc/`. The
handler model's `<service>.svc.go` holds `<S>Handlers` in place of
`<S>Repositories`; `feeds.svc.go` and `adapters.svc.go` need `store=true`:

| File | What |
|---|---|
| `<service>.svc.go` | `<S>Server` implementing the gRPC server interface; `<S>Repositories` (the store repositories it persists through); `<S>Hooks` and `Nop<S>Hooks`. |
| `register.svc.go` | `Servers` and `RegisterAll(Servers) []grpc.Option` (runtime-go): every server on gRPC, its grpc-gateway handlers and its `Serve<S>MCP`; `RegisterGRPC` for a standalone server. |
| `feeds.svc.go` | For resources with `(service.v1.event)`: CloudEvents type constants, `<R>EventType`, `<R>StateEventType`, `<R>FeedSpec(repo) events.FeedSpec[*R]`, `<R>Feed`, `EventTypes()`. |
| `errors.svc.go`, `errors.svc.md` | For enums with `(service.v1.error_domain)`: `ErrorDomain`, `Reason…` constants, sentinels, `Kinds()`, `Reasons()`, `NewTranslator()`, `Retryable()`, a `New<Reason>` constructor per reason; and the Markdown table for your error reference, between `protoc-gen-service:errors` markers. |
| `adapters.svc.go` | For resources with `(service.v1.adapter)`: `EventStore` (runtime-go `events.Store`) over webhook, delivery and feed cursor repositories; `IdempotencyStore` (`idempotency.Store`) over an idempotency key repository. |

The shared runtime (options, the `problem.Translator` wiring, request
checks) is runtime-go's `grpc/servicex`, and the store helpers (feed query
translation, store errors) are runtime-go's `database/repox`. Before
service v0.4.2 each run generated its own `servicex/`; regenerating removes
it.

### Hooks and custom methods

```go
type siteHooks struct {
	directoryv1svc.NopOrganizationalUnitsHooks // implement only what you need
	orgs directory.OrganizationRepository
}

// A site must belong to a live organization.
func (h siteHooks) ValidateOrganizationalUnit(ctx context.Context, _ servicex.Op, m *directoryv1.OrganizationalUnit, paths []string) error {
	if !servicex.Touches(paths, "organization") {
		return nil
	}
	org, err := h.orgs.Get(ctx, m.GetOrganization())
	if err != nil {
		return servicex.NotFoundAs(err, "directory.acme.example/Organization", m.GetOrganization())
	}
	if org.GetDeleteTime() != nil {
		return servicex.Blocked("ORGANIZATION_DELETED", "directory.acme.example/Organization", org.GetName(),
			"organization "+org.GetName()+" is deleted", "Restore it with UndeleteOrganization")
	}
	return nil
}

// Custom methods, and replacements of standard ones: embed and define.
type calendars struct {
	*calendarv1svc.CalendarsServer
}

func (s *calendars) QueryFreeBusy(ctx context.Context, req *calendarv1.QueryFreeBusyRequest) (*calendarv1.QueryFreeBusyResponse, error) {
	out := &calendarv1.QueryFreeBusyResponse{}
	err := s.Options().Run(ctx, "Calendars", "QueryFreeBusy", func(ctx context.Context) error { /* … */ return nil })
	return out, err
}

srv := grpc.NewHybridServer(opts, append(
	directoryv1svc.RegisterAll(directoryv1svc.Servers{
		OrganizationalUnits: directoryv1svc.NewOrganizationalUnitsServer(
			directoryv1svc.OrganizationalUnitsRepositories{OrganizationalUnits: repos.OrganizationalUnits},
			siteHooks{orgs: repos.Organizations},
			servicex.WithTranslator(bookingv1svc.NewTranslator()), servicex.WithTracer(traced)),
	}),
	grpc.WithValidation())...)
```

Hooks return `*problem.Problem` values (`servicex.BadField`, `Missing`,
`Blocked`, or a generated `New<Reason>`), errors wrapping a registered
sentinel, or gRPC statuses; the server's `problem.Translator` renders them.

### The application shell

`app=<name>` also generates everything a service built over these servers
otherwise writes by hand, so that a project starts with only its business
logic to write. It never generates that logic: it provisions a typed slot
for each piece and wires whatever fills it.

| Package | What |
|---|---|
| `app/config` | `AppConfig` with only the sections the run uses (`[server]`, `[database.postgres]`, `[cache]`, `[cdn]`, `[webhooks]` with a webhook store, `[retention]` with soft-deletable resources), validated; the embedded `<name>.release.toml`, the `<name>.dev.toml` profile (from the working directory, or embedded with `<NAME>_PROFILE=dev`), then `<NAME>_SECTION__KEY` environment variables; `Layers` and `Section` for the application's own sections. |
| `app/telemetry` | The process pipeline (OTLP from `<NAME>_OTLP_ENDPOINT`), the logging helpers, `Meter`. |
| `app/database` | The shared `Connection`: Postgres with pool bounds, error translation and a quiet logger, the pool monitor, the cache provider, and `Observe` for anything that must hear every write. |
| `app/server` | `Repositories` (every repository of the run on one connection); `Translator`/`ServerOptions` (every package's error kinds, each server keeping its own domain, and the tracer); `New` (every server, with what the `Provision` gives); `Interceptors` (hints, then idempotency with an idempotency-key store); `Options`/`Register` (every package's `RegisterAll`); the hybrid server with TLS, CORS, MCP and shutdown from config; background work (webhook delivery, the soft-delete purger, the pool monitor); `NewGRPCServer` for a bufconn test. |
| `app`, `cmd/<name>` | `App` and `Main`; the command is one line. |
| `errors.md` | The error reference of every package, in one document. |

What an application provisions, over `Deps` (config, connection, repositories):

```go
func provision(d *server.Deps) (server.Provision, error) {
	return server.Provision{
		Hooks: server.Hooks{Organizations: orgHooks{repos: d.Repos}},  // around standard methods
		Logic: server.Logic{Calendars: freeBusy{repos: d.Repos}},       // custom methods: required
		Override: func(s *server.Services) server.Overrides { ... },     // replace any server
		Options:    []grpc.Option{grpc.WithUnaryInterceptors(auth)},     // the hybrid server
		Extensions: []server.Extension{caldav},                          // mounts, background, cleanup
	}, nil
}

func main() { app.Main(app.Options{Provider: provision}) }
```

Every custom method, unary or streaming, is a method of a generated
`<S>Logic` interface, run through the server's tracer and translator. A
streaming method takes grpc-go's stream (`grpc.ServerStreamingServer[Resp]`,
`grpc.ClientStreamingServer[Req, Resp]`, `grpc.BidiStreamingServer[Req, Resp]`)
whose `Context` is the traced one; an error it returns reaches the client as a
translated status. A custom method with no
`Logic` (and no override) stops the assembly at startup with the list of what
is missing; the generated `cmd/<name>` is a skeleton and opts out with
`Provision{Unimplemented: true}`, serving those as UNIMPLEMENTED.

## Rust

Setup is in [Setup → Rust](#rust-setup). Generation needs `rustfmt` on the
generator's `PATH`: it formats and parses every Rust file using Rust 2024 style
and an isolated configuration, and missing rustfmt or invalid output fails
generation. Use the same rustfmt version when checking reproducible generated
fixtures. Rust generation does not need `go_package`, `go_module` or
`repository_module`; `rust_proto_module` (default `crate::pb`) and
`rust_service_module` (default `crate::service`) name where the messages and
the generated tree are mounted. `error_domain` applies in every language.

### Layout and handlers

The output uses the proto package hierarchy (`acme/directory/v1/`), with
`mod.rs` files exposing the same package-level API. Each service has its own
file (`organizations.rs`); `feeds.rs`, `errors.rs`, and `adapters.rs` are emitted
when needed. Shared behavior is runtime-rs's `grpc::servicex`, which the
generated root re-exports as `servicex`, so `crate::service::servicex` paths
keep working (`rust_servicex_module` names it otherwise). Every file carries
the same provenance header in every language.

When upgrading from an output that generated `servicex/` itself (before
protoc-gen-service v0.4.0), delete that directory and add the `tpp-grpc`
dependency; from the older flat layout, also remove `servicex.rs` and the
`<proto.package>.svc.rs` files before regenerating.

Every RPC is answered by a **handler** the application registers: the
request message in, the response message out -- a Diesel query, a call to
another service, any business logic. The plugin never generates the database
layer; it runs [the standard methods](#the-standard-methods)' checks and hooks
around its handler.

```rust
let handlers = ds::OrganizationsHandlers::default()
    .get_organization(|ctx, req: pb::GetOrganizationRequest| async move { db.get(&req.name).await })
    .create_organization(|ctx, req: pb::CreateOrganizationRequest| async move { db.insert(req).await })
    // ... one per RPC
    ;
let server = ds::OrganizationsServer::new(options, handlers)?   // MissingHandlers names any RPC left out
    .with_organization_hooks(MyHooks);                          // servicex::Hooks, optional
tonic::transport::Server::builder().add_service(server.into_service());
```

A server is never built with an RPC nothing answers: `new` returns
`servicex::MissingHandlers` listing them. Handlers that write enforce the etag
in the write. Handlers and hooks receive request metadata and extensions
through `servicex::Context`. `Options` takes a `prost_reflect::DescriptorPool`
containing your schema and its imports; it supplies field behavior and mask
metadata.

Change feeds and the event and idempotency record adapters read through the
`servicex::Repository<M>` trait, implemented over the same persistence layer.

### `store=true`

With `store=true` each service also gets `<S>Repositories`, one
`Arc<dyn database::repository::Repository<M>>` per resource (runtime-rs's
`tpp-database`), and `<S>Handlers::with_repositories(&repos)`, which answers
every standard method from them; custom methods stay yours to register. The
checks and hooks above run around these handlers unchanged, so the semantics
are those of every language's repository-backed servers: name checks, `ParentCheck`,
OUTPUT_ONLY clearing, REQUIRED and IMMUTABLE checks, `validate_only`,
`allow_missing` (an update finding nothing creates), etags (the update's
record carries the caller's, delete and undelete pass it as `if_match`),
soft delete (Delete returns the record as `get` still reads it), and List's
paging, filter, order, `show_deleted` and parent (`ListInput.parent`, AIP-159
`-` ids included). A create without an id leaves the name to the repository,
`<parent>/<collection>/` for a nested resource, and it mints a ULID, the same
name a Go repository mints. Repository errors map to the same codes and reasons
in both languages:

| `database::Error` | Code | Reason |
|---|---|---|
| `NotFound` | NOT_FOUND | `NOT_FOUND` |
| `AlreadyExists` | ALREADY_EXISTS | `ALREADY_EXISTS` |
| `Conflict` | ABORTED | `CONCURRENT_MODIFICATION` |
| `Invalid` | INVALID_ARGUMENT | `INVALID_ARGUMENT` |
| `Timeout` / `Unimplemented` (e.g. undelete without soft delete) / other | DEADLINE_EXCEEDED / UNIMPLEMENTED / INTERNAL | |

The crate depends on `tpp-database` (with the `orm-*` feature of
`rust_database`). With `app=<name>`, the shell adds `[database]` (`url`,
`max_connections`) to the configuration, `app::open_database`,
`app::Repositories` with `app::repositories(db, pool)` (calling each proto
package's `repositories` from `rust_repository_module`, which protoc-gen-store
`lang=rust target=repository` generates), and `app::Handlers::with_repositories`;
`app::run` opens the database, builds the repositories and fills the
handlers before assembling. See [examples/rust-store](examples/rust-store).

### The application shell

`app=<name>` generates the application shell under `app/`, over runtime-rs
(`tpp-config`, `tpp-grpc`), with or without `store=true`:

| Item | What |
|---|---|
| `app::config` | `AppConfig` (identity; the server's listeners, MCP, TLS, CORS and shutdown) with its `Validate` check; `loader()` over the embedded `<name>.release.toml` and `<name>.dev.toml`, then files and `<NAME>_SECTION__KEY` variables, which override everything; `load()` |
| `app::Handlers`, `app::Hooks` | Every service's handlers and every resource's hooks, in one place |
| `app::assemble` | Every server built from them, or `MissingHandlers` for every service with an RPC left unanswered |
| `app::hybrid_server` | Every service on runtime-rs's hybrid gRPC/REST/MCP server, with reflection and buf.validate over the descriptors, and CORS, TLS, MCP and the shutdown timeout from the configuration |
| `app::run` | Load, assemble, serve until SIGINT or SIGTERM, stop gracefully |

```rust
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut handlers = app::Handlers::default();
    handlers.organizations = handlers.organizations.get_organization(|ctx, req| async move { db.get(&req.name).await });
    // ... one per RPC
    app::run(app::Provision::new(DESCRIPTORS, handlers)).await?;
    Ok(())
}
```

The crate depends on `tpp-config` and `tpp-grpc` (with its `tls` feature), and
`serde` with `derive`; with `store=true` also `tpp-database`, and with
`cache=true` `tpp-cache`.

The crate's other dependencies are tonic 0.14, prost/prost-types 0.14, tonic-types 0.14,
prost-reflect 0.16 with `serde`, futures-core 0.3, and serde_json 1. See the
[compilable Rust example](examples/rust) for dependencies and behavior tests.
Not generated for Rust yet, where Go has them: an event dispatcher over the
feeds, and bridges from the record adapters to runtime-rs's persistence and
idempotency traits. Without `app=`, a Rust service registers on tonic only; the
shell's hybrid server adds REST and MCP.

## Caching (`cache=true`)

`cache=true` requires `store=true` and reads `(cache.v1.cache)` from your
protos.

- **Go:** for each service with a resource whose `(cache.v1.cache).list` is
  enabled, `cache.svc.go` declares `Cached<S>Repositories(ctx, provider,
  observe, repos)`, which puts protoc-gen-cache's list cache
  (`<cache_module>/<database>/<schema>cache`, `New<Model>ListCache(...).Wrap`)
  in front of the repository and has it observe the connection's writes. The
  Go app shell calls it when `[cache]` is enabled; a cache that fails to open
  is logged and left off. Record caches decorate the GORM stores under a
  repository and are scoped by a tenant only the deployment knows: wire them
  with the repository package's `With<Model>Store`.
  `cache.backend` picks the server: `redis` (the default; Redis or Dragonfly
  at `address`) or `workerskv` (Cloudflare Workers KV, the namespace
  `[cache.workerskv]` names, its `api_token` set through
  `<NAME>_CACHE__WORKERSKV__API_TOKEN`). Workers KV has no server-side sets,
  so a resource cached as `INDEXED` or `DOCUMENT` is refused when it opens.
  `[cdn]` (`enabled`, `zone_id`, `api_token`) opens a Cloudflare CDN purger
  that business logic reaches as `Connection.CDN()`, to purge a zone by URL,
  cache tag, host or prefix after a write commits.
- **Rust:** the app shell adds `[cache]` (`enabled`, `backend`, `address`,
  `prefix`, `database`, and `[cache.workerskv]`; Redis, Dragonfly or Workers
  KV through `tpp-cache`) and
  `app::cache_repositories(repos, Arc<cache::DB>)`, which passes every
  package with `cache.v1` annotations through
  `<rust_repository_module>::<package>::cache::cache_repositories`, the
  `cache.rs` protoc-gen-cache `lang=rust` writes beside protoc-gen-store's
  `mod.rs`. Without `app=` there is nothing to wire: wrap a package's
  repositories yourself before `with_repositories`.

## Reference

### Plugin options

| Opt | Default | |
|---|---|---|
| `lang` | `go` | The language to generate: `go` or `rust`. |
| `go_module` | required (Go) | Import path of the out directory. |
| `store` | `false` | Repository-backed servers over protoc-gen-store's repositories; without it, the handler model. |
| `repository_module` | required with `store=true` | protoc-gen-store's repository `go_module`. |
| `cache` | `false` | With `store=true`: wire protoc-gen-cache's caches from `cache.v1` annotations. Refused without `store=true`. |
| `cache_module` | required with `cache=true` (Go) | protoc-gen-cache's output import path. |
| `config` | | `service.yaml`: the store's `datasources`/`strip_version` layout (so repository packages resolve to the store's names) and `bindings`. |
| `grpc_package`, `gateway_package`, `mcp_package` | `{import_path}` | Where the bindings live: templates over `{import_path}`, `{package}`, `{proto_dir}`. |
| `gateway`, `mcp` | `true` | Register REST and MCP. |
| `error_domain` | `google.api.default_host` | ErrorInfo domain of the generated servers. |
| `package_suffix` | `svc` | |
| `app` | | Also generate the application shell of this name ([Go](#the-application-shell), [Rust](#the-application-shell-1)). Go: needs `store=true`. |
| `rust_repository_module` | `crate::repository` | Rust, `store=true`: where protoc-gen-store's `lang=rust` repositories live (`<module>::<package path>`), with protoc-gen-cache's `cache` module beside each. |
| `rust_servicex_module` | `grpc::servicex` | Rust: the servicex runtime the generated services build on (runtime-rs `tpp-grpc`, depended on as `grpc`); the generated root re-exports it as `servicex`. |
| `rust_database` | `postgres` | Rust app shell, `store=true`: the SQL dialect it opens (`postgres`, `mysql`, `sqlite`). |

`bindings` places the bindings per proto package, for a module whose stubs
live elsewhere (the BSR's `…/grpc/go/…/<pkg>grpc` layout, standalone gateway):

```yaml
bindings:
  - match: "protobuf.**"
    grpc_package: "buf.build/gen/go/the-protobuf-project/rfc/grpc/go/{proto_dir}/{package}grpc"
    gateway_package: "example.com/gen/rfc/{proto_dir}/{package}gw"
    mcp_package: "example.com/gen/rfc/{proto_dir}/{package}mcp"
```

Imports are never aliased. When two would declare one name, generation fails
and names the setting that separates them (a `datasources:` schema name, or a
binding template).

### Annotations

| Option | On | |
|---|---|---|
| `(service.v1.service) = {skip: true}` | service | Generate nothing for it. |
| `(service.v1.method) = {custom: true}` | method | Keep a standard-looking method custom. |
| `(service.v1.event) = {type, omit, scope}` | resource | Opt into the change feed; lifecycle events `<type>.created/updated/deleted`; `scope` names the pattern variable a webhook scope matches. Needs `update_time` and `etag`. |
| `(service.v1.state_event) = {type}` | state enum value | The event a record entering that state emits. |
| `(service.v1.error_domain) = {domain}` | enum | Its values are ErrorInfo reasons. |
| `(service.v1.error_reason) = {code, description, fix, detail, retryable, sentinel}` | enum value | One reason. |
| `(service.v1.adapter) = {role, fields}` | resource | A runtime-go store's record. |

Adapter roles and their conventional fields (rename with `fields`):

| Role | Fields (required in bold) |
|---|---|
| `ROLE_WEBHOOK` | **uri**, event_types, scope, paused (bool) or state (`*_PAUSED`), secret, previous_secret, rotate_time, create_time, delete_time |
| `ROLE_DELIVERY` | **event_id, event_type, subject, payload, state** (`*_PENDING/_SUCCEEDED/_FAILED`), **attempt_count, next_attempt_time, update_time, etag**, response_code, error_message, deliver_time, create_time |
| `ROLE_FEED_CURSOR` | **position** (Timestamp), **etag** |
| `ROLE_IDEMPOTENCY_KEY` | **method, request_id, state** (`*_IN_FLIGHT/_DONE`), **response**, create_time |

Extension numbers 53000-53006.

## Adding a language

A language renders the same model: which methods are standard, the checks and
hooks around each, the error reasons and the refusals are decided once, before
any language's templates run. A new language is done when:

- it generates the handler model and `store=true` with the same checks, hooks,
  reasons and refusals as the others, held by the shared golden cases;
- `store=true` binds protoc-gen-store's repositories for that language, so it
  shares a database with the others;
- it has a compiled example CI builds and tests, as `examples/rust` and
  `examples/rust-store` are for Rust;
- it has a [Setup](#setup) section and a section of its own in this README.

## Developing

```bash
go test ./...                                             # golden, determinism, IR agreement, refusals
go test ./plugin/generator -run TestGolden -update       # rewrite goldens
go build -o bin/protoc-gen-service ./plugin/cmd/protoc-gen-service
PATH="$PWD/bin:$PATH" buf generate --template buf.gen.example.yaml
./scripts/aip.sh                                          # api-linter
```

The examples (`examples/`) compile everything the plugins generate and run
the servers, feeds and adapters on PostgreSQL
(`SERVICE_TEST_DSN=… go test ./integration/...`), against runtime-go's `main`
(CI builds them in a workspace over a runtime-go checkout; see
`.github/workflows/test.yaml`). The Rust examples build and test with Cargo:

```bash
go test ./plugin/rustgen -update-rust                     # rewrite the Rust fixtures
cargo test --manifest-path examples/rust/Cargo.toml
cargo test --manifest-path examples/rust-store/Cargo.toml
```

## License

Apache-2.0.

[protoc-gen-store]: https://github.com/the-protobuf-project/store
[protoc-gen-cache]: https://github.com/the-protobuf-project/cache
