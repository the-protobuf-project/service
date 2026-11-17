use prost::Message;
fn main() {
    println!("cargo:rerun-if-changed=descriptor.bin");
    let set = prost_types::FileDescriptorSet::decode(include_bytes!("descriptor.bin").as_slice())
        .unwrap();
    tonic_prost_build::configure()
        // buf.validate's rules are options read from the descriptors at run
        // time; no message here holds one, so it needs no Rust types.
        .extern_path(".buf.validate", "::protovalidate_rules_unused")
        .include_file("proto.rs")
        .compile_fds(set)
        .unwrap();
}
