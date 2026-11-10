# Generated Rust service example

This crate compiles the generated services with prost messages and tonic
bindings, including unary and all three streaming RPC shapes. The tests exercise
CRUD, field masks, dry runs, atomic etag enforcement, custom request metadata,
feed pagination, and event/idempotency record adapters.

From the repository root:

```sh
go test ./plugin/rustgen
cargo test --locked --manifest-path examples/rust/Cargo.toml
```

After editing the Rust generator or its shared runtime template:

```sh
go test ./plugin/rustgen -run TestRustExamples -update-rust
cargo test --locked --manifest-path examples/rust/Cargo.toml
```

`descriptor.bin` and `src/generated/` are reproducible test fixtures. `build.rs`
builds prost/tonic bindings directly from that descriptor set, so compiling the
example does not require protoc. Production projects can use
`tonic_prost_build::configure().compile_protos(...)` and export a descriptor set
with `file_descriptor_set_path(...)`, then load it into `servicex::Options`.

The in-memory repository in `tests/service.rs` is a test fixture, not a database
backend: production repositories must implement the full list/filter contract,
server-generated values, and atomic conditional mutations.
