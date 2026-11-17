# protoc-gen-service

`protoc-gen-service` generates the AIP standard-method service layer that
services otherwise write by hand: one gRPC server per service, running each
standard method's checks (names, ids, masks, etags, field behavior) and a
hooks interface for the business logic around it, and the wiring that serves it on gRPC, REST and MCP through
runtime-go's HybridServer. It also generates change feeds and store adapters
for runtime-go's `events` and `grpc/idempotency` packages, and error reasons
declared in proto (AIP-193).

It recognizes AIP-131..135 and AIP-164 by name and shape alone, so a
conforming API needs no annotation. A method that does not fit stays custom:
the generated server leaves it to the embedded `Unimplemented…Server`, and you
write only those.

## Two models: handlers (default) and `store=true`

By default a generated server is a **handler model**: every RPC is answered
by a handler the application registers (`<S>Handlers`, the request in and
the response out: a query of your database, a call to another service), and
the server runs each standard method's checks and hooks around it. Nothing
about persistence is generated, and no other plugin's output is needed. Go
and Rust generate the same model.

`store=true` generates **repository-backed** servers instead: each standard
method persists through [protoc-gen-store]'s generated repositories (Go:
`<S>Repositories` over `repox`; Rust: runtime-rs's
`database::repository::Repository`). This is the output the examples use,
with the change feeds, store adapters and Go application shell built over
those repositories. `cache=true` (with `store=true`) also wires
[protoc-gen-cache]'s caches from `cache.v1` annotations; see
[Caching](#caching-cachetrue).

| | handler model (default) | `store=true` |
|---|---|---|
| Server | `New<S>Server(<S>Handlers, <S>Hooks, ...) (*<S>Server, error)`, refusing missing handlers (`servicex.MissingHandlers`) | `New<S>Server(<S>Repositories, <S>Hooks, ...)`, custom methods through `WithLogic` |
| Needs | `go_module` | `go_module`, `repository_module`, protoc-gen-store's repository output |
| Feeds, store adapters | not generated (a warning names them) | generated |
| Go `app=` | refused: the Go shell builds over the repositories | generated |

### The handler model (Go)

```go
srv, err := directoryv1svc.NewOrganizationsServer(directoryv1svc.OrganizationsHandlers{
	GetOrganization: func(ctx context.Context, req *directoryv1.GetOrganizationRequest) (*directoryv1.Organization, error) {
		return db.Get(ctx, req.GetName()) // servicex.ErrNotFound when there is none
	},
	// ... one per RPC, custom and streaming ones included
}, orgHooks{}, servicex.WithTranslator(directoryv1svc.NewTranslator()))
```

Around each handler the server runs what the Rust table below lists: the
name's shape; List's parent, `ParentCheck` and `page_size`; Create's id,
parent, OUTPUT_ONLY clearing, REQUIRED, `ParentCheck`, `Validate`,
`BeforeCreate`, `validate_only`, `AfterCreate`; Update's mask and, with the
stored record read through the service's own Get handler, its etag,
IMMUTABLE fields, the merge, REQUIRED and `allow_missing`; Delete's and
Undelete's etag, deleted state, `allow_missing`, `force` and `validate_only`.
An Update handler receives the merged record carrying the caller's etag and
the canonical mask; handlers that write enforce the etag in the write.
Handlers return `servicex.ErrNotFound`, `ErrAlreadyExists`, `ErrConflict`
and `ErrInvalidArgument` (or errors wrapping them) for the standard
outcomes, rendered with the reasons below.

## What it generates (`store=true`)

For each proto package, into `<proto package path>/<go package>svc/`:

| File | What |
|---|---|
| `<service>.svc.go` | `<S>Server` implementing the gRPC server interface; `<S>Repositories` (the store repositories it persists through); `<S>Hooks` and `Nop<S>Hooks`. |
| `register.svc.go` | `Servers` and `RegisterAll(Servers) []grpc.Option` (runtime-go): every server on gRPC, its grpc-gateway handlers and its `Serve<S>MCP`; `RegisterGRPC` for a standalone server. |
| `feeds.svc.go` | For resources with `(service.v1.event)`: CloudEvents type constants, `<R>EventType`, `<R>StateEventType`, `<R>FeedSpec(repo) events.FeedSpec[*R]`, `<R>Feed`, `EventTypes()`. |
| `errors.svc.go`, `errors.svc.md` | For enums with `(service.v1.error_domain)`: `ErrorDomain`, `Reason…` constants, sentinels, `Kinds()`, `Reasons()`, `NewTranslator()`, `Retryable()`, a `New<Reason>` constructor per reason; and the Markdown table for your error reference, between `protoc-gen-service:errors` markers. |
| `adapters.svc.go` | For resources with `(service.v1.adapter)`: `EventStore` (runtime-go `events.Store`) over webhook, delivery and feed cursor repositories; `IdempotencyStore` (`idempotency.Store`) over an idempotency key repository. |

And once per run, `servicex/`: the shared runtime (options, the
`problem.Translator` wiring, request checks, feed query translation) and
`servicex/errors.svc.md`, the table of the standard reasons.

### The standard methods

| Method | Generated behaviour |
|---|---|
| Get (131) | name shape (`MALFORMED_NAME`), repository Get, `NOT_FOUND` naming the resource. |
| List (132) | parent shape (AIP-159 `-` allowed), `ParentCheck` hook, `page_size`, `page_token`, `filter`, `order_by`, `show_deleted`, `read_mask` onto `repox.ListInput`. |
| Create (133) | `<resource>_id` (AIP-122, `INVALID_ID`; empty mints one), parent shape and `ParentCheck`, OUTPUT_ONLY fields cleared, REQUIRED fields checked together (`REQUIRED_FIELD`), `Validate`, `BeforeCreate`, `validate_only`, `ALREADY_EXISTS`, `AfterCreate`. |
| Update (134) | `update_mask` (`*`, absent = populated fields, JSON names; `UNKNOWN_MASK_PATH`, `NOTHING_TO_UPDATE`), IMMUTABLE fields (`IMMUTABLE_FIELD`), etag (`ABORTED`/`CONCURRENT_MODIFICATION`), `allow_missing` (creates), `validate_only` (previews), hooks. |
| Delete (135) | etag, `allow_missing`, `validate_only`, `force` passed to `BeforeDelete`; soft delete when the resource has an OUTPUT_ONLY `delete_time`; returns Empty or the soft-deleted resource. |
| Undelete (164) | etag, `validate_only`, `BeforeUndelete`; `ALREADY_EXISTS` when not deleted. |
| Purge | single-resource `Purge<R>(name)` returning Empty. AIP-165's Purge is a bulk LRO, so this is an extension; leave it out of AIP-linted APIs. |

protovalidate is not repeated: runtime-go's `WithValidation` runs it in front
of all three surfaces.

## Usage

```yaml
# buf.gen.yaml
plugins:
  - local: protoc-gen-store
    out: gen/repository
    strategy: all
    opt: [target=repository, go_module=example.com/gen/repository, gorm_module=example.com/gen/gorm, config=layout.yaml]
  - local: protoc-gen-service
    out: gen/service
    strategy: all
    opt:
      - go_module=example.com/gen/service          # import path of this out dir
      - store=true                                  # repository-backed servers
      - repository_module=example.com/gen/repository
      - config=layout.yaml                          # the store's datasources, plus bindings
```

| Opt | Default | |
|---|---|---|
| `go_module` | required | Import path of the out directory. |
| `store` | `false` | Repository-backed servers over protoc-gen-store's repositories; without it, the handler model. |
| `repository_module` | required with `store=true` | protoc-gen-store's repository `go_module`. |
| `cache` | `false` | With `store=true`: wire protoc-gen-cache's caches from `cache.v1` annotations. Refused without `store=true`. |
| `cache_module` | required with `cache=true` (Go) | protoc-gen-cache's output import path. |
| `config` | | `service.yaml`: the store's `datasources`/`strip_version` layout (so repository packages resolve to the store's names) and `bindings`. |
| `grpc_package`, `gateway_package`, `mcp_package` | `{import_path}` | Where the bindings live: templates over `{import_path}`, `{package}`, `{proto_dir}`. |
| `gateway`, `mcp` | `true` | Register REST and MCP. |
| `error_domain` | `google.api.default_host` | ErrorInfo domain of the generated servers. |
| `package_suffix` | `svc` | |
| `app` | | Also generate the application shell of this name (below). Go: needs `store=true`. |
| `rust_repository_module` | `crate::repository` | Rust, `store=true`: where protoc-gen-store's `lang=rust` repositories live (`<module>::<package path>`), with protoc-gen-cache's `cache` module beside each. |
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

### Options

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

### The application shell

`app=<name>` also generates everything a service built over these servers
otherwise writes by hand, so that a project starts with only its business
logic to write. It never generates that logic: it provisions a typed slot
for each piece and wires whatever fills it.

| Package | What |
|---|---|
| `app/config` | `AppConfig` with only the sections the run uses (`[server]`, `[database.postgres]`, `[cache]`, `[webhooks]` with a webhook store, `[retention]` with soft-deletable resources), validated; the embedded `<name>.release.toml`, the `<name>.dev.toml` profile (from the working directory, or embedded with `<NAME>_PROFILE=dev`), then `<NAME>_SECTION__KEY` environment variables; `Layers` and `Section` for the application's own sections. |
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

## Rust services

Select `lang=rust` on the same plugin to generate tonic service implementations.
Install `rustfmt` (`rustup component add rustfmt`) on the generator's PATH.
Generation formats and parses every Rust file using Rust 2024 style and an
isolated configuration; missing rustfmt or invalid output fails generation.
Use the same rustfmt version when checking reproducible generated fixtures.

```sh
go build -o bin/protoc-gen-service ./plugin/cmd/protoc-gen-service
```

```yaml
plugins:
  - local: protoc-gen-service
    out: src/service
    strategy: all
    opt:
      - lang=rust
      - rust_proto_module=crate::pb
      - rust_service_module=crate::service
```

Generate prost messages and tonic server bindings separately with
`tonic-prost-build`, preserving the proto package module hierarchy under `pb`.
Add `pub mod service;` to your crate. Rust generation does not require
`go_package`, `go_module`, or `repository_module`. Both Rust module options
shown above are their defaults; `error_domain` also applies to Rust.

The output uses the proto package hierarchy (`acme/directory/v1/`), with
`mod.rs` files exposing the same package-level API. Each service has its own
file (`organizations.rs`); `feeds.rs`, `errors.rs`, and `adapters.rs` are emitted
when needed. Shared behavior lives in `servicex/mod.rs`, `servicex/feeds.rs`, and
`servicex/adapters.rs`. Every file carries the same provenance header as Go.
When upgrading from the flat layout, remove the old generated `servicex.rs`
and `<proto.package>.svc.rs` files before regenerating. The fixture update
command performs this cleanup for `examples/rust`.
Every RPC is answered by a **handler** the application registers: the
request message in, the response message out -- a Diesel query, a call to
another service, any business logic. The plugin never generates the database
layer; it runs each standard method's checks and hooks around its handler:

| Method | Generated around the handler |
|---|---|
| Get, Purge | the name's shape |
| List | the parent's shape (AIP-159 `-` allowed), `ParentCheck`, `page_size` |
| Create | the id (AIP-122) and parent; the resource named, OUTPUT_ONLY cleared, REQUIRED checked; `ParentCheck`, `Validate`, `BeforeCreate`; `validate_only`; `AfterCreate` |
| Update | the name and `update_mask`; with the stored record (read through the service's own Get handler) its etag, the merge, IMMUTABLE and REQUIRED fields; `allow_missing`; `Validate`, `BeforeUpdate`; `validate_only`; `AfterUpdate`. The handler receives the record to write and the canonical mask, carrying the request's etag |
| Delete, Undelete | the name; with the stored record its etag and deleted state; `allow_missing`, `force`, `validate_only`; the before/after hooks |
| Custom (unary or streaming) | nothing: straight to its handler |

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

### Rust `store=true`

With `store=true` each service also gets `<S>Repositories`, one
`Arc<dyn database::repository::Repository<M>>` per resource (runtime-rs's
`tpp-database`), and `<S>Handlers::with_repositories(&repos)`, which answers
every standard method from them; custom methods stay yours to register. The
checks and hooks above run around these handlers unchanged, so the semantics
are the Go repository-backed servers': name checks, `ParentCheck`,
OUTPUT_ONLY clearing, REQUIRED and IMMUTABLE checks, `validate_only`,
`allow_missing` (an update finding nothing creates), etags (the update's
record carries the caller's, delete and undelete pass it as `if_match`),
soft delete (Delete returns the record as `get` still reads it), and List's
paging, filter, order, `show_deleted` and parent (`ListInput.parent`, AIP-159
`-` ids included). A create without an id leaves the name to the repository,
`<parent>/<collection>/` for a nested resource, and it mints a ULID, as Go's
`repox` does. Repository errors map as Go maps `repox`'s:

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
- **Rust:** the app shell adds `[cache]` (`enabled`, `address`, `prefix`,
  `database`; Redis or Dragonfly through `tpp-cache`) and
  `app::cache_repositories(repos, Arc<cache::DB>)`, which passes every
  package with `cache.v1` annotations through
  `<rust_repository_module>::<package>::cache::cache_repositories`, the
  `cache.rs` protoc-gen-cache `lang=rust` writes beside protoc-gen-store's
  `mod.rs`. Without `app=` there is nothing to wire: wrap a package's
  repositories yourself before `with_repositories`.

`app=<name>` also generates the Rust application shell under `app/`, over
runtime-rs (`tpp-config`, `tpp-grpc`):

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

The Rust target uses tonic 0.14, prost/prost-types 0.14, tonic-types 0.14,
prost-reflect 0.16 with `serde`, futures-core 0.3, and serde_json 1. See the
[compilable Rust example](examples/rust) for dependencies and behavior tests.
Rust currently supplies tonic registration and standalone record adapters;
automatic REST/MCP registration, an event dispatcher, and bridges to
runtime-rs persistence/idempotency traits are not generated.

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
(`SERVICE_TEST_DSN=… go test ./integration/...`). They import runtime-go's
`events`, `grpc/problem` and `grpc/idempotency`; until those are on
runtime-go's main branch, build the examples in a workspace that also uses a
local runtime-go checkout (CI does the same; see `.github/workflows/test.yaml`).

## License

Apache-2.0.

[protoc-gen-store]: https://github.com/the-protobuf-project/store
[protoc-gen-cache]: https://github.com/the-protobuf-project/cache
