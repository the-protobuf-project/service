use prost::Message;
fn main() {
    println!("cargo:rerun-if-changed=descriptor.bin");
    let set = prost_types::FileDescriptorSet::decode(include_bytes!("descriptor.bin").as_slice())
        .unwrap();
    let mut config = tonic_prost_build::configure()
        // buf.validate's rules are options read from the descriptors at run
        // time; no message here holds one, so it needs no Rust types.
        .extern_path(".buf.validate", "::protovalidate_rules_unused")
        .include_file("proto.rs");
    // The repositories read and write records through their descriptors:
    // every message of the served packages reflects (ReflectMessage).
    for file in &set.file {
        if !matches!(
            file.package(),
            "acme.directory.v1" | "acme.calendar.v1" | "catalog.v1"
        ) {
            continue;
        }
        for message in &file.message_type {
            let name = format!("{}.{}", file.package(), message.name());
            config = config
                .type_attribute(&name, "#[derive(::prost_reflect::ReflectMessage)]")
                .type_attribute(
                    &name,
                    format!(r#"#[prost_reflect(message_name = "{name}")]"#),
                )
                .type_attribute(
                    &name,
                    r#"#[prost_reflect(descriptor_pool = "crate::DESCRIPTOR_POOL")]"#,
                );
        }
    }
    config.compile_fds(set).unwrap();
}
