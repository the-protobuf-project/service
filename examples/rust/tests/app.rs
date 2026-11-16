//! The generated application shell: its configuration (defaults, environment
//! overrides, validation), the assembly refusing a provision with RPCs left
//! unanswered, and runtime-rs's hybrid server serving the assembled services
//! over a real connection.
use service_rust_examples::{
    pb::acme::directory::v1::{self as directory, organizations_client::OrganizationsClient},
    service::{app, servicex},
};
use tonic::{Code, Status};

const DESCRIPTORS: &[u8] = include_bytes!("../descriptor.bin");

fn options() -> servicex::Options {
    servicex::Options::new(prost_reflect::DescriptorPool::decode(DESCRIPTORS).unwrap())
}

/// Registers a handler that answers UNIMPLEMENTED for each named RPC: a
/// stand-in for the application's logic, which this test does not exercise.
macro_rules! stub {
    ($handlers:expr; $($method:ident),* $(,)?) => {
        $handlers $( .$method(|_, _| async { Err(Status::unimplemented("stub")) }) )*
    };
}

/// A complete provision: every RPC of every service has a handler, and
/// GetOrganization a real one.
fn handlers() -> app::Handlers {
    let mut h = app::Handlers::default();
    h.holds = stub!(h.holds; get_hold, list_holds, create_hold, release_hold);
    h.reservations = stub!(h.reservations; get_reservation, list_reservations, create_reservation, update_reservation);
    h.calendars = stub!(h.calendars; get_calendar, list_calendars, create_calendar, update_calendar, delete_calendar, undelete_calendar, query_free_busy);
    h.events = stub!(h.events; get_event, list_events, create_event, update_event, delete_event, undelete_event, expand_event);
    h.organizational_units = stub!(h.organizational_units; get_organizational_unit, list_organizational_units, create_organizational_unit, update_organizational_unit, delete_organizational_unit, undelete_organizational_unit);
    h.organizations = stub!(h.organizations; list_organizations, create_organization, update_organization, delete_organization, undelete_organization)
        .get_organization(|_, req: directory::GetOrganizationRequest| async move {
            Ok(directory::Organization {
                name: req.name,
                display_name: "Acme".into(),
                ..Default::default()
            })
        });
    h.documents = stub!(h.documents; get_document, delete_document, purge_document, get_document_by_hand, undelete_document, list_pages, get_settings);
    h.entries = stub!(h.entries; wrap, watch, upload, exchange);
    h
}

#[test]
fn assembly_names_every_rpc_left_without_a_handler() {
    let Err(missing) = app::assemble(options(), app::Handlers::default(), app::Hooks::default())
    else {
        panic!("an empty provision was assembled");
    };
    assert_eq!(missing.len(), 8, "one entry per service");
    let organizations = missing
        .iter()
        .find(|m| m.service == "acme.directory.v1.Organizations")
        .unwrap();
    assert!(organizations.methods.contains(&"GetOrganization"));

    // One RPC short is still refused, naming only it.
    let mut h = handlers();
    h.organizations.undelete_organization = None;
    let Err(missing) = app::assemble(options(), h, app::Hooks::default()) else {
        panic!("a provision missing UndeleteOrganization was assembled");
    };
    assert_eq!(missing.len(), 1);
    assert_eq!(missing[0].methods, ["UndeleteOrganization"]);

    assert!(app::assemble(options(), handlers(), app::Hooks::default()).is_ok());
}

#[test]
fn configuration_defaults_environment_and_validation() {
    let cfg: app::config::AppConfig = app::config::loader()
        .without_env()
        .load()
        .unwrap()
        .extract_validated()
        .unwrap();
    assert_eq!(cfg.app.name, "acme");
    assert_eq!(cfg.server.grpc.port, 50051);
    assert_eq!(cfg.server.environment, "production");
    assert_eq!(cfg.options().grpc.port, 50051);

    // The environment overrides the defaults, typed as the field reads it.
    // SAFETY: this test is the only one in this binary to touch ACME_ variables.
    unsafe { std::env::set_var("ACME_SERVER__GRPC__PORT", "6001") };
    let (_, cfg) = app::config::load().unwrap();
    assert_eq!(cfg.server.grpc.port, 6001);
    unsafe { std::env::remove_var("ACME_SERVER__GRPC__PORT") };

    // A value the server cannot run on is refused, naming the key.
    let err = app::config::loader()
        .without_env()
        .release(config::Source::toml("[server]\nenvironment = \"prod\"\n"))
        .load()
        .unwrap()
        .extract_validated::<app::config::AppConfig>()
        .unwrap_err();
    assert!(err.to_string().contains("server.environment"), "{err}");
}

#[tokio::test]
async fn the_hybrid_server_serves_the_assembled_services() {
    let cfg: app::config::AppConfig = app::config::loader()
        .without_env()
        .release(config::Source::toml(
            r#"
            [server]
            environment = "development"
            enable_http = false
            enable_mcp = false
            [server.grpc]
            host = "127.0.0.1"
            port = 0
            "#,
        ))
        .load()
        .unwrap()
        .extract_validated()
        .unwrap();
    let servers = app::assemble(options(), handlers(), app::Hooks::default()).unwrap();
    let mut server = app::hybrid_server(&cfg, servers, DESCRIPTORS, None).unwrap();
    server.start().await.unwrap();
    let address = server.grpc_addr().expect("listening");

    let mut client = OrganizationsClient::connect(format!("http://{address}"))
        .await
        .unwrap();
    // The registered handler answers.
    let org = client
        .get_organization(directory::GetOrganizationRequest {
            name: "organizations/acme".into(),
        })
        .await
        .unwrap()
        .into_inner();
    assert_eq!(org.display_name, "Acme");
    // The generated checks run before it: a malformed name never reaches it.
    let err = client
        .get_organization(directory::GetOrganizationRequest {
            name: "calendars/acme".into(),
        })
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);

    server.stop().await.unwrap();
}
