# protoc-gen-service

`protoc-gen-service` generates the AIP standard-method service layer that
consumers of [protoc-gen-store] otherwise write by hand: one gRPC server per
service, with a hooks interface for the business logic around each standard
method, and the wiring that serves it on gRPC, REST and MCP through
runtime-go's HybridServer. It also generates change feeds and store adapters
for runtime-go's `events` and `grpc/idempotency` packages, and error reasons
declared in proto (AIP-193).

It recognizes AIP-131..135 and AIP-164 by name and shape alone, so a
conforming API needs no annotation. A method that does not fit stays custom:
the generated server leaves it to the embedded `Unimplemented…Server`, and you
write only those.

## What it generates

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
      - repository_module=example.com/gen/repository
      - config=layout.yaml                          # the store's datasources, plus bindings
```

| Opt | Default | |
|---|---|---|
| `go_module` | required | Import path of the out directory. |
| `repository_module` | required | protoc-gen-store's repository `go_module`. |
| `config` | | `service.yaml`: the store's `datasources`/`strip_version` layout (so repository packages resolve to the store's names) and `bindings`. |
| `grpc_package`, `gateway_package`, `mcp_package` | `{import_path}` | Where the bindings live: templates over `{import_path}`, `{package}`, `{proto_dir}`. |
| `gateway`, `mcp` | `true` | Register REST and MCP. |
| `error_domain` | `google.api.default_host` | ErrorInfo domain of the generated servers. |
| `package_suffix` | `svc` | |

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
