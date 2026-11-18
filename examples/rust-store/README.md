# Generated Rust services, `store=true` and `cache=true`

The directory, calendar and catalog protos generated with `store=true`,
`cache=true`, `rust_database=sqlite` and `app=acme` (`src/generated/`): the
servers answer their standard methods from runtime-rs
`database::repository::Repository`s, and the app shell opens the records
database and the cache.

`src/repository/` is the other plugins' real output for the same protos:
protoc-gen-store's `lang=rust target=repository` (`RecordsRepository` per
resource, `repositories(db, pool)`) and protoc-gen-cache's `lang=rust`
(`catalog/v1/cache.rs`, `cache_repositories`). The tests run every standard
method over SQLite in memory, and the catalog's shops through the cache on
tpp-cache's in-memory driver.

runtime-rs is a git dependency on its `main` branch; `Cargo.lock` pins the
commit, so CI's `--locked` builds reproduce. The "Generated Rust services" job
runs this crate's fmt, clippy and tests.

`src/generated/` is checked by `go test ./plugin/rustgen` (`-update-rust`
rewrites it). Regenerate `src/repository/` with protoc-gen-store
(`lang=rust target=repository crate=repository rust_proto_module=crate::pb
config=examples/layout.yaml`) and protoc-gen-cache (`lang=rust
rust_repository_module=crate::repository`) over the same protos.

```sh
go test ./plugin/rustgen -run TestRustStoreExamples -update-rust
cargo test --manifest-path examples/rust-store/Cargo.toml
cargo fmt --manifest-path examples/rust-store/Cargo.toml --check
cargo clippy --manifest-path examples/rust-store/Cargo.toml --all-targets -- -D warnings
```
