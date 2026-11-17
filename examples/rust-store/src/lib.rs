//! The store=true and cache=true generated services over runtime-rs's
//! repositories: `repository` is protoc-gen-store's `lang=rust
//! target=repository` output for the same protos (RecordsRepository over any
//! records provider; the tests use SQLite in memory).
use std::sync::LazyLock;

use prost_reflect::DescriptorPool;

pub mod pb {
    include!(concat!(env!("OUT_DIR"), "/proto.rs"));
}
#[path = "generated/mod.rs"]
pub mod service;

#[path = "repository/lib.rs"]
pub mod repository;

/// The encoded descriptor set of the schema and its imports.
pub const DESCRIPTORS: &[u8] = include_bytes!("../descriptor.bin");

/// Every descriptor the messages reflect through.
pub static DESCRIPTOR_POOL: LazyLock<DescriptorPool> = LazyLock::new(|| {
    DescriptorPool::decode(DESCRIPTORS).expect("descriptor.bin is a descriptor set")
});
