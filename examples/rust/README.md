# Generated Rust service example

This crate compiles the generated services with prost messages and tonic
bindings, including unary and all three streaming RPC shapes. The tests exercise
CRUD, field masks, dry runs, atomic etag enforcement, custom request metadata,
feed pagination, and event/idempotency record adapters.

Generation requires `rustfmt` on PATH (`rustup component add rustfmt`).
Generated files follow the proto package directories; `mod.rs` re-exports each
service and its package helpers. Shared runtime modules are under `servicex/`.

From the repository root:

```sh
go test ./plugin/rustgen
cargo test --locked --manifest-path examples/rust/Cargo.toml
cargo fmt --manifest-path examples/rust/Cargo.toml --check
```

After editing the Rust generator or its shared runtime template:

```sh
go test ./plugin/rustgen -run TestRustExamples -update-rust
cargo test --locked --manifest-path examples/rust/Cargo.toml
cargo fmt --manifest-path examples/rust/Cargo.toml --check
```

`descriptor.bin` and `src/generated/` are reproducible test fixtures. `build.rs`
builds prost/tonic bindings directly from that descriptor set, so compiling the
example does not require protoc. Production projects can use
`tonic_prost_build::configure().compile_protos(...)` and export a descriptor set
with `file_descriptor_set_path(...)`, then load it into `servicex::Options`.

The in-memory repository in `tests/service.rs` is a test fixture, not a database
backend: production repositories must implement the full list/filter contract,
server-generated values, and atomic conditional mutations.

`src/generated/streams/v1/entries.rs` contains all RPC kinds from
`plugin/rustgen/testdata/streaming/streaming.proto`:

| RPC | Kind |
|---|---|
| `Wrap` | Unary |
| `Watch` | Server streaming |
| `Upload` | Client streaming |
| `Exchange` | Bidirectional streaming |

Implement `EntriesCustom` and pass it to `EntriesServer::with_custom`.
See `tests/streaming.rs` for working handlers and a tonic client/server test
covering metadata, stream errors, empty input, and bidirectional messages sent
before the client closes its stream. Stream item types are derived from each
RPC's protobuf request and response types.
