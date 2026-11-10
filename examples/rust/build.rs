use prost::Message;
fn main() {
    println!("cargo:rerun-if-changed=descriptor.bin");
    let set = prost_types::FileDescriptorSet::decode(include_bytes!("descriptor.bin").as_slice())
        .unwrap();
    tonic_prost_build::configure()
        .include_file("proto.rs")
        .compile_fds(set)
        .unwrap();
}
