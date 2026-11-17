//! store=true servers over an in-memory Repository: the generated checks and
//! hooks around repository-backed handlers, with the Go repository-backed
//! servers' semantics and AIP-193 reasons.
use std::sync::Arc;
use std::time::Duration;

use cache::core::{Capabilities, DatabaseSpec, MemoryDriver, MemorySets, build_database};
use database::orm::OrmProvider;
use database::orm::sqlx::sqlite::SqlitePoolOptions;
use database::records::{DB, Provider};
use service_rust_store_examples::{
    DESCRIPTOR_POOL,
    pb::acme::calendar::v1::{self as calendar, events_server::Events},
    pb::acme::directory::v1::{self as directory, organizations_server::Organizations},
    pb::catalog::v1::{self as catalog, shops_server::Shops},
    repository,
    service::{app, servicex},
};
use tonic::{Code, Request, Status};
use tonic_types::StatusExt;

fn pool() -> prost_reflect::DescriptorPool {
    DESCRIPTOR_POOL.clone()
}

/// An in-memory SQLite database with each package's schema attached and
/// every resource's table created from protoc-gen-store's descriptors.
async fn sqlite() -> DB {
    let sql = SqlitePoolOptions::new()
        .max_connections(1)
        .idle_timeout(None)
        .max_lifetime(None)
        .connect("sqlite::memory:")
        .await
        .unwrap();
    for attach in [
        "ATTACH DATABASE ':memory:' AS calendar",
        "ATTACH DATABASE ':memory:' AS directory",
        "ATTACH DATABASE ':memory:' AS catalog_example",
    ] {
        database::orm::sqlx::query(attach)
            .execute(&sql)
            .await
            .unwrap();
    }
    let db = OrmProvider::new(sql).set_database("").await.unwrap();
    let pool = pool();
    let message = |name: &str| pool.get_message_by_name(name).unwrap();
    for resource in [
        repository::acme::calendar::v1::calendar_resource(message("acme.calendar.v1.Calendar")),
        repository::acme::calendar::v1::event_resource(message("acme.calendar.v1.Event")),
        repository::acme::directory::v1::organization_resource(message(
            "acme.directory.v1.Organization",
        )),
        repository::acme::directory::v1::organizational_unit_resource(message(
            "acme.directory.v1.OrganizationalUnit",
        )),
        repository::catalog::v1::shop_resource(message("catalog.v1.Shop")),
        repository::catalog::v1::product_resource(message("catalog.v1.Product")),
    ] {
        db.schema.ensure_schema(&resource).await.unwrap();
    }
    db
}

/// Every package's repositories, protoc-gen-store's, over SQLite.
async fn repos() -> app::Repositories {
    app::repositories(&sqlite().await, &pool()).unwrap()
}

/// Every handler: the standard ones from repos, the custom ones stubbed.
fn handlers(repos: &app::Repositories) -> app::Handlers {
    let mut h = app::Handlers::default().with_repositories(repos);
    h.calendars = h
        .calendars
        .query_free_busy(|_, _| async { Err(Status::unimplemented("stub")) });
    h.events = h
        .events
        .expand_event(|_, _| async { Err(Status::unimplemented("stub")) });
    h
}

async fn servers() -> app::Servers {
    servers_over(&repos().await)
}

fn servers_over(repos: &app::Repositories) -> app::Servers {
    let options = servicex::Options::new(pool());
    app::assemble(options, handlers(repos), app::Hooks::default())
        .unwrap_or_else(|m| panic!("{m:?}"))
}

/// The status's ErrorInfo reason.
fn reason(s: &Status) -> String {
    s.get_details_error_info()
        .map(|i| i.reason)
        .unwrap_or_default()
}

fn org(name: &str) -> directory::Organization {
    directory::Organization {
        name: name.into(),
        display_name: "Acme".into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn repositories_answer_every_standard_method() {
    let Err(missing) = app::assemble(
        servicex::Options::new(pool()),
        app::Handlers::default().with_repositories(&repos().await),
        app::Hooks::default(),
    ) else {
        panic!("custom methods without handlers were assembled");
    };
    let mut methods: Vec<_> = missing.iter().flat_map(|m| m.methods.clone()).collect();
    methods.sort();
    assert_eq!(methods, ["ExpandEvent", "QueryFreeBusy"]);
}

#[tokio::test]
async fn crud_over_the_repository() {
    let s = servers().await.organizations;
    let created = s
        .create_organization(Request::new(directory::CreateOrganizationRequest {
            organization_id: "acme".into(),
            organization: Some(org("")),
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(created.name, "organizations/acme");
    assert!(!created.etag.is_empty(), "the repository mints an etag");
    assert!(created.create_time.is_some());

    // No id: the repository mints a ULID, as Go's repox does.
    let minted = s
        .create_organization(Request::new(directory::CreateOrganizationRequest {
            organization: Some(org("")),
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    let id = minted.name.strip_prefix("organizations/").unwrap();
    assert_eq!(id.len(), 26, "{}", minted.name);

    let dup = s
        .create_organization(Request::new(directory::CreateOrganizationRequest {
            organization_id: "acme".into(),
            organization: Some(org("")),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(
        (dup.code(), reason(&dup).as_str()),
        (Code::AlreadyExists, "ALREADY_EXISTS")
    );

    let missing = s
        .create_organization(Request::new(directory::CreateOrganizationRequest {
            organization_id: "empty".into(),
            organization: Some(directory::Organization::default()),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(reason(&missing), "REQUIRED_FIELD");

    // A stale etag: CONCURRENT_MODIFICATION.
    let stale = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                etag: "stale".into(),
                display_name: "Renamed".into(),
                ..org("organizations/acme")
            }),
            update_mask: Some(prost_types::FieldMask {
                paths: vec!["display_name".into()],
            }),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(
        (stale.code(), reason(&stale).as_str()),
        (Code::Aborted, "CONCURRENT_MODIFICATION")
    );

    // validate_only previews and writes nothing.
    let preview = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                display_name: "Preview".into(),
                ..org("organizations/acme")
            }),
            update_mask: Some(prost_types::FieldMask {
                paths: vec!["display_name".into()],
            }),
            validate_only: true,
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(preview.display_name, "Preview");

    let updated = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                etag: created.etag.clone(),
                display_name: "Renamed".into(),
                ..org("organizations/acme")
            }),
            update_mask: Some(prost_types::FieldMask {
                paths: vec!["displayName".into()],
            }),
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(updated.display_name, "Renamed");
    assert_ne!(updated.etag, created.etag);
    assert!(
        updated.create_time.is_some(),
        "an update keeps the output-only fields"
    );

    // IMMUTABLE: registration cannot change once set.
    s.create_organization(Request::new(directory::CreateOrganizationRequest {
        organization_id: "registered".into(),
        organization: Some(directory::Organization {
            registration: "R1".into(),
            ..org("")
        }),
        ..Default::default()
    }))
    .await
    .unwrap();
    let immutable = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                registration: "R2".into(),
                ..org("organizations/registered")
            }),
            update_mask: Some(prost_types::FieldMask {
                paths: vec!["registration".into()],
            }),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(reason(&immutable), "IMMUTABLE_FIELD");

    // allow_missing creates.
    let upserted = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(org("organizations/new")),
            allow_missing: true,
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(upserted.name, "organizations/new");

    // Soft delete, guarded by the etag.
    let current = s
        .get_organization(Request::new(directory::GetOrganizationRequest {
            name: "organizations/acme".into(),
        }))
        .await
        .unwrap()
        .into_inner();
    let stale = s
        .delete_organization(Request::new(directory::DeleteOrganizationRequest {
            name: "organizations/acme".into(),
            etag: "stale".into(),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(reason(&stale), "CONCURRENT_MODIFICATION");
    s.delete_organization(Request::new(directory::DeleteOrganizationRequest {
        name: "organizations/acme".into(),
        etag: current.etag,
        ..Default::default()
    }))
    .await
    .unwrap();
    let deleted = s
        .get_organization(Request::new(directory::GetOrganizationRequest {
            name: "organizations/acme".into(),
        }))
        .await
        .unwrap()
        .into_inner();
    assert!(
        deleted.delete_time.is_some(),
        "Get returns the soft-deleted record"
    );
    let again = s
        .delete_organization(Request::new(directory::DeleteOrganizationRequest {
            name: "organizations/acme".into(),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(
        (again.code(), reason(&again).as_str()),
        (Code::NotFound, "NOT_FOUND")
    );

    // List skips it unless show_deleted.
    let list = |show_deleted| {
        let s = &s;
        async move {
            s.list_organizations(Request::new(directory::ListOrganizationsRequest {
                show_deleted,
                ..Default::default()
            }))
            .await
            .unwrap()
            .into_inner()
            .organizations
            .len()
        }
    };
    assert_eq!(list(false).await, 3);
    assert_eq!(list(true).await, 4);

    s.undelete_organization(Request::new(directory::UndeleteOrganizationRequest {
        name: "organizations/acme".into(),
    }))
    .await
    .unwrap();
    let live = s
        .undelete_organization(Request::new(directory::UndeleteOrganizationRequest {
            name: "organizations/acme".into(),
        }))
        .await
        .unwrap_err();
    assert_eq!(
        (live.code(), reason(&live).as_str()),
        (Code::AlreadyExists, "ALREADY_EXISTS")
    );

    let nothing = s
        .get_organization(Request::new(directory::GetOrganizationRequest {
            name: "organizations/none".into(),
        }))
        .await
        .unwrap_err();
    assert_eq!(
        (nothing.code(), reason(&nothing).as_str()),
        (Code::NotFound, "NOT_FOUND")
    );
    let malformed = s
        .get_organization(Request::new(directory::GetOrganizationRequest {
            name: "orgs/x".into(),
        }))
        .await
        .unwrap_err();
    assert_eq!(reason(&malformed), "MALFORMED_NAME");
}

#[tokio::test]
async fn list_is_scoped_to_its_parent() {
    let s = servers().await.events;
    for (calendar, id) in [
        ("calendars/a", "one"),
        ("calendars/a", "two"),
        ("calendars/b", "three"),
    ] {
        s.create_event(Request::new(calendar::CreateEventRequest {
            parent: calendar.into(),
            event_id: id.into(),
            event: Some(calendar::Event {
                summary: id.into(),
                start_time: Some(prost_types::Timestamp::default()),
                ..Default::default()
            }),
        }))
        .await
        .unwrap();
    }
    let list = |parent: &'static str, page_size| {
        let s = &s;
        async move {
            s.list_events(Request::new(calendar::ListEventsRequest {
                parent: parent.into(),
                page_size,
                ..Default::default()
            }))
            .await
            .unwrap()
            .into_inner()
        }
    };
    assert_eq!(list("calendars/a", 0).await.events.len(), 2);
    assert_eq!(list("calendars/b", 0).await.events.len(), 1);
    assert_eq!(
        list("calendars/-", 0).await.events.len(),
        3,
        "AIP-159 reads across parents"
    );
    let page = list("calendars/a", 1).await;
    assert_eq!(page.events.len(), 1);
    assert!(!page.next_page_token.is_empty());
}

#[test]
fn repository_errors_carry_the_standard_codes() {
    use database::Error;
    let cases = [
        (Error::NotFound, Code::NotFound),
        (Error::AlreadyExists, Code::AlreadyExists),
        (Error::Conflict, Code::Aborted),
        (Error::Invalid("bad filter".into()), Code::InvalidArgument),
        (Error::Timeout, Code::DeadlineExceeded),
    ];
    for (e, code) in cases {
        assert_eq!(servicex::store_status(e, "t", "n").code(), code);
    }
}

/// cache=true: the shell passes catalog.v1's repositories through
/// protoc-gen-cache's cache_repositories; the shops' List is then served from
/// the cache, and a write moves its pages on.
#[tokio::test]
async fn cached_repositories_serve_the_standard_methods() {
    let driver = Arc::new(MemoryDriver::new());
    let cache_db = Arc::new(build_database(
        driver.clone(),
        Capabilities::new()
            .with_sets(Arc::new(MemorySets::new()))
            .with_leases(driver),
        DatabaseSpec {
            prefix: "acme".into(),
            namespace: "acme".into(),
            default_ttl: Duration::from_secs(60),
            ..DatabaseSpec::default()
        },
    ));
    let repos = app::cache_repositories(repos().await, cache_db);
    let s = servers_over(&repos).shops;
    let list = || async {
        s.list_shops(Request::new(catalog::ListShopsRequest::default()))
            .await
            .unwrap()
            .into_inner()
            .shops
            .len()
    };
    assert_eq!(list().await, 0);
    s.create_shop(Request::new(catalog::CreateShopRequest {
        shop_id: "corner".into(),
        shop: Some(catalog::Shop {
            display_name: "Corner".into(),
            ..Default::default()
        }),
    }))
    .await
    .unwrap();
    assert_eq!(list().await, 1, "the create moved the cached pages on");
    let shop = s
        .get_shop(Request::new(catalog::GetShopRequest {
            name: "shops/corner".into(),
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(shop.display_name, "Corner");
}
