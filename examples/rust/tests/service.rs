use calendar::calendars_server::Calendars;
use directory::organizations_server::Organizations;
use prost::Message;
use prost_reflect::{DynamicMessage, ReflectMessage, Value};
use service_rust_examples::{
    pb::acme::{
        calendar::v1 as calendar, directory::v1 as directory, idempotency::v1 as idempotency,
        webhook::v1 as webhook,
    },
    service::{
        acme::{
            calendar::v1 as cs, directory::v1 as ds, idempotency::v1 as ids, webhook::v1 as ws,
        },
        servicex as sx,
    },
};
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
};
use tonic::{Code, Request, Status};

fn options() -> sx::Options {
    sx::Options::new(
        prost_reflect::DescriptorPool::decode(include_bytes!("../descriptor.bin").as_slice())
            .unwrap(),
    )
}
// A deterministic test repository. It enforces etags within the mutation lock
// and caps list pages at one to exercise service and feed pagination.
struct Memory<M> {
    rows: Arc<Mutex<BTreeMap<String, M>>>,
    message: &'static str,
}
impl<M> Clone for Memory<M> {
    fn clone(&self) -> Self {
        Self {
            rows: self.rows.clone(),
            message: self.message,
        }
    }
}
impl<M> Memory<M> {
    fn new(message: &'static str) -> Self {
        Self {
            rows: Arc::new(Mutex::new(BTreeMap::new())),
            message,
        }
    }
}
fn string(m: &DynamicMessage, f: &str) -> String {
    m.get_field_by_name(f)
        .and_then(|v| v.as_str().map(str::to_string))
        .unwrap_or_default()
}
impl<M: Message + Default + Clone> Memory<M> {
    fn dynamic(&self, m: &M) -> DynamicMessage {
        DynamicMessage::decode(
            options()
                .descriptors
                .get_message_by_name(self.message)
                .unwrap(),
            m.encode_to_vec().as_slice(),
        )
        .unwrap()
    }
    fn stamp(&self, m: &mut DynamicMessage) {
        if m.descriptor().get_field_by_name("etag").is_some() {
            let old = string(m, "etag").parse::<u64>().unwrap_or(0);
            m.set_field_by_name("etag", Value::String((old + 1).to_string()));
        }
    }
    fn time(&self, m: &mut DynamicMessage, f: &str) {
        if let Some(fd) = m.descriptor().get_field_by_name(f)
            && let prost_reflect::Kind::Message(desc) = fd.kind()
        {
            let t = prost_types::Timestamp {
                seconds: 100,
                nanos: 0,
            };
            let d = DynamicMessage::decode(desc, t.encode_to_vec().as_slice()).unwrap();
            m.set_field_by_name(f, Value::Message(d));
        }
    }
    fn update_fields(&self, value: M, paths: Vec<String>) -> Result<M, Status> {
        let input = self.dynamic(&value);
        let name = string(&input, "name");
        let mut rows = self.rows.lock().unwrap();
        let old = rows.get(&name).ok_or_else(|| Status::not_found("absent"))?;
        let mut d = self.dynamic(old);
        let want = string(&input, "etag");
        if !want.is_empty() && want != string(&d, "etag") {
            return Err(Status::aborted("stale"));
        }
        for p in paths {
            let fd = d.descriptor().get_field_by_name(&p).unwrap();
            if input.has_field(&fd) {
                d.set_field(&fd, input.get_field(&fd).into_owned());
            } else {
                d.clear_field(&fd);
            }
        }
        self.stamp(&mut d);
        let out: M = d.transcode_to().unwrap();
        rows.insert(name, out.clone());
        Ok(out)
    }
}
#[tonic::async_trait]
impl<M: Message + Default + Clone + Send + Sync + 'static> sx::Repository<M> for Memory<M> {
    async fn get(&self, _: &sx::Context, name: &str) -> Result<M, Status> {
        self.rows
            .lock()
            .unwrap()
            .get(name)
            .cloned()
            .ok_or_else(|| Status::not_found("absent"))
    }
    async fn list(
        &self,
        _: &sx::Context,
        _: &str,
        input: sx::ListInput,
    ) -> Result<(Vec<M>, String), Status> {
        let rows = self.rows.lock().unwrap();
        let mut values = rows
            .values()
            .filter(|v| input.show_deleted || !self.dynamic(v).has_field_by_name("delete_time"));
        let offset = input.page_token.parse::<usize>().unwrap_or(0);
        let out: Vec<_> = values.by_ref().skip(offset).take(1).cloned().collect();
        let next = if values.next().is_some() {
            (offset + 1).to_string()
        } else {
            String::new()
        };
        Ok((out, next))
    }
    async fn create(&self, _: &sx::Context, parent: &str, value: M) -> Result<M, Status> {
        let mut rows = self.rows.lock().unwrap();
        let mut d = self.dynamic(&value);
        let mut name = string(&d, "name");
        if name.is_empty() {
            name = format!(
                "{}organizations/minted",
                if parent.is_empty() {
                    String::new()
                } else {
                    format!("{parent}/")
                }
            );
        }
        if rows.contains_key(&name) {
            return Err(Status::already_exists("duplicate"));
        };
        d.set_field_by_name("name", Value::String(name.clone()));
        self.stamp(&mut d);
        self.time(&mut d, "create_time");
        self.time(&mut d, "update_time");
        let out: M = d.transcode_to().unwrap();
        rows.insert(name, out.clone());
        Ok(out)
    }
    async fn update(&self, _: &sx::Context, value: M, paths: Vec<String>) -> Result<M, Status> {
        self.update_fields(value, paths)
    }
    async fn update_with_output_only(
        &self,
        _: &sx::Context,
        value: M,
        mut paths: Vec<String>,
        output: Vec<String>,
    ) -> Result<M, Status> {
        paths.extend(output);
        self.update_fields(value, paths)
    }
    async fn delete(&self, _: &sx::Context, name: &str, etag: &str) -> Result<Option<M>, Status> {
        let mut rows = self.rows.lock().unwrap();
        let value = rows.get(name).ok_or_else(|| Status::not_found("absent"))?;
        let mut d = self.dynamic(value);
        if !etag.is_empty() && etag != string(&d, "etag") {
            return Err(Status::aborted("stale"));
        }
        self.time(&mut d, "delete_time");
        self.stamp(&mut d);
        let out: M = d.transcode_to().unwrap();
        rows.insert(name.into(), out.clone());
        Ok(Some(out))
    }
    async fn undelete(&self, _: &sx::Context, name: &str, etag: &str) -> Result<M, Status> {
        let mut rows = self.rows.lock().unwrap();
        let value = rows.get(name).ok_or_else(|| Status::not_found("absent"))?;
        let mut d = self.dynamic(value);
        if !etag.is_empty() && etag != string(&d, "etag") {
            return Err(Status::aborted("stale"));
        }
        if !d.has_field_by_name("delete_time") {
            return Err(Status::already_exists("live"));
        }
        d.clear_field_by_name("delete_time");
        self.stamp(&mut d);
        let out: M = d.transcode_to().unwrap();
        rows.insert(name.into(), out.clone());
        Ok(out)
    }
    async fn purge(&self, _: &sx::Context, name: &str) -> Result<(), Status> {
        self.rows
            .lock()
            .unwrap()
            .remove(name)
            .ok_or_else(|| Status::not_found("absent"))?;
        Ok(())
    }
}
fn server(repo: Memory<directory::Organization>) -> ds::OrganizationsServer {
    ds::OrganizationsServer::new(options(), sx::Resource::new(repo))
}
async fn create(s: &ds::OrganizationsServer, id: &str) -> directory::Organization {
    s.create_organization(Request::new(directory::CreateOrganizationRequest {
        organization_id: id.into(),
        organization: Some(directory::Organization {
            display_name: "Acme".into(),
            registration: "R1".into(),
            ..Default::default()
        }),
        ..Default::default()
    }))
    .await
    .unwrap()
    .into_inner()
}

#[tokio::test]
async fn generated_crud_checks_masks_etags_and_previews() {
    let s = server(Memory::new("acme.directory.v1.Organization"));
    let original = create(&s, "acme").await;
    assert!(!original.etag.is_empty());
    assert!(original.create_time.is_some());
    let preview = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                name: original.name.clone(),
                display_name: "Preview".into(),
                registration: "R1".into(),
                ..Default::default()
            }),
            update_mask: Some(prost_types::FieldMask {
                paths: vec!["*".into()],
            }),
            validate_only: true,
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(preview.etag, original.etag);
    assert_eq!(preview.create_time, original.create_time);
    assert_eq!(
        s.get_organization(Request::new(directory::GetOrganizationRequest {
            name: original.name.clone()
        }))
        .await
        .unwrap()
        .into_inner()
        .display_name,
        "Acme"
    );
    let update = directory::UpdateOrganizationRequest {
        organization: Some(directory::Organization {
            name: original.name.clone(),
            display_name: "Changed".into(),
            etag: original.etag.clone(),
            ..Default::default()
        }),
        update_mask: Some(prost_types::FieldMask {
            paths: vec!["displayName".into()],
        }),
        ..Default::default()
    };
    let changed = s
        .update_organization(Request::new(update.clone()))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(changed.registration, "R1");
    assert_eq!(
        s.update_organization(Request::new(update))
            .await
            .unwrap_err()
            .code(),
        Code::Aborted
    );
    let err = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                name: original.name.clone(),
                registration: "R2".into(),
                ..Default::default()
            }),
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert_eq!(
        s.delete_organization(Request::new(directory::DeleteOrganizationRequest {
            name: original.name.clone(),
            etag: original.etag,
            ..Default::default()
        }))
        .await
        .unwrap_err()
        .code(),
        Code::Aborted
    );
    s.delete_organization(Request::new(directory::DeleteOrganizationRequest {
        name: original.name.clone(),
        etag: changed.etag,
        ..Default::default()
    }))
    .await
    .unwrap();
    assert!(
        s.undelete_organization(Request::new(directory::UndeleteOrganizationRequest {
            name: original.name
        }))
        .await
        .unwrap()
        .into_inner()
        .delete_time
        .is_none()
    );
}
#[tokio::test]
async fn required_names_allow_missing_and_paging() {
    let s = server(Memory::new("acme.directory.v1.Organization"));
    assert_eq!(
        s.create_organization(Request::new(directory::CreateOrganizationRequest {
            organization: Some(Default::default()),
            ..Default::default()
        }))
        .await
        .unwrap_err()
        .code(),
        Code::InvalidArgument
    );
    assert_eq!(
        s.get_organization(Request::new(directory::GetOrganizationRequest {
            name: "bad/name/shape".into()
        }))
        .await
        .unwrap_err()
        .code(),
        Code::InvalidArgument
    );
    let out = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                name: "organizations/a".into(),
                display_name: "A".into(),
                ..Default::default()
            }),
            allow_missing: true,
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(out.name, "organizations/a");
    // allow_missing chooses the id through the name, held to Create's rule.
    let err = s
        .update_organization(Request::new(directory::UpdateOrganizationRequest {
            organization: Some(directory::Organization {
                name: "organizations/Not_Valid".into(),
                display_name: "X".into(),
                ..Default::default()
            }),
            allow_missing: true,
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    create(&s, "b").await;
    let page = s
        .list_organizations(Request::new(Default::default()))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(page.organizations.len(), 1);
    assert!(!page.next_page_token.is_empty());
    let page = s
        .list_organizations(Request::new(directory::ListOrganizationsRequest {
            page_token: page.next_page_token,
            ..Default::default()
        }))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(page.organizations[0].name, "organizations/b");
    assert!(page.next_page_token.is_empty());
}
#[tokio::test]
async fn undelete_dry_run_validates_state() {
    let repo = Memory::new("acme.calendar.v1.Calendar");
    repo.rows.lock().unwrap().insert(
        "calendars/a".into(),
        calendar::Calendar {
            name: "calendars/a".into(),
            ..Default::default()
        },
    );
    let server = cs::CalendarsServer::new(options(), sx::Resource::new(repo));
    assert_eq!(
        server
            .undelete_calendar(Request::new(calendar::UndeleteCalendarRequest {
                name: "calendars/a".into(),
                validate_only: true,
                ..Default::default()
            }))
            .await
            .unwrap_err()
            .code(),
        Code::AlreadyExists
    );
}
struct Custom;
#[tonic::async_trait]
impl cs::CalendarsCustom for Custom {
    async fn query_free_busy(
        &self,
        request: Request<calendar::QueryFreeBusyRequest>,
    ) -> Result<tonic::Response<calendar::QueryFreeBusyResponse>, Status> {
        assert_eq!(request.metadata().get("x-tenant").unwrap(), "acme");
        Ok(tonic::Response::new(calendar::QueryFreeBusyResponse {
            busy: 42,
        }))
    }
}
#[tokio::test]
async fn custom_methods_preserve_request_metadata() {
    let server = cs::CalendarsServer::new(
        options(),
        sx::Resource::new(Memory::new("acme.calendar.v1.Calendar")),
    )
    .with_custom(Custom);
    let mut request = Request::new(calendar::QueryFreeBusyRequest::default());
    request
        .metadata_mut()
        .insert("x-tenant", "acme".parse().unwrap());
    assert_eq!(
        server
            .query_free_busy(request)
            .await
            .unwrap()
            .into_inner()
            .busy,
        42
    );
}
#[tokio::test]
async fn feed_fetch_follows_backend_pages_and_serializes_proto_json() {
    let repo = Memory::new("acme.calendar.v1.Event");
    for name in ["calendars/c/events/a", "calendars/c/events/b"] {
        repo.rows.lock().unwrap().insert(
            name.into(),
            calendar::Event {
                name: name.into(),
                etag: name.into(),
                summary: "meeting".into(),
                ..Default::default()
            },
        );
    }
    let feed = cs::event_feed(Arc::new(repo), options());
    let names = vec!["calendars/c/events/a".into(), "calendars/c/events/b".into()];
    let payloads = feed.fetch(&sx::Context::default(), &names).await.unwrap();
    assert_eq!(payloads.len(), 2);
    let event = calendar::Event {
        name: names[0].clone(),
        create_time: Some(prost_types::Timestamp {
            seconds: 1,
            nanos: 0,
        }),
        update_time: Some(prost_types::Timestamp {
            seconds: 2,
            nanos: 0,
        }),
        delete_time: Some(prost_types::Timestamp {
            seconds: 2,
            nanos: 0,
        }),
        ..Default::default()
    };
    let change = feed.change(&event).unwrap();
    assert_eq!(change.event_type, cs::EVENT_DELETED_EVENT);
    assert_eq!(change.scope, "calendars/c");
}
#[tokio::test]
async fn generated_adapters_round_trip_cursors_deliveries_and_idempotency() {
    let ctx = sx::Context::default();
    let store = ws::event_store(
        Arc::new(Memory::<webhook::Webhook>::new("acme.webhook.v1.Webhook")),
        Arc::new(Memory::<webhook::Delivery>::new("acme.webhook.v1.Delivery")),
        Arc::new(Memory::<webhook::FeedCursor>::new(
            "acme.webhook.v1.FeedCursor",
        )),
        options(),
    );
    let cursor = store
        .create_cursor(
            &ctx,
            sx::Cursor {
                feed: "feed".into(),
                position: Some(prost_types::Timestamp {
                    seconds: 1,
                    nanos: 0,
                }),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    store
        .update_cursor(
            &ctx,
            sx::Cursor {
                position: Some(prost_types::Timestamp {
                    seconds: 2,
                    nanos: 0,
                }),
                ..cursor.clone()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        store.update_cursor(&ctx, cursor).await.unwrap_err().code(),
        Code::Aborted
    );
    store
        .create_delivery(
            &ctx,
            sx::Delivery {
                name: "webhooks/w/deliveries/d".into(),
                payload: "{}".into(),
                event_id: "event".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let deliveries = store
        .due_deliveries(
            &ctx,
            prost_types::Timestamp {
                seconds: 200,
                nanos: 0,
            },
            1,
        )
        .await
        .unwrap();
    let mut d = deliveries[0].clone();
    d.state = sx::DeliveryState::Succeeded;
    d.attempt_count = 1;
    let updated = store
        .update_delivery(&ctx, d, &["state".into(), "attempt_count".into()])
        .await
        .unwrap();
    assert_eq!(updated.state, sx::DeliveryState::Succeeded);
    assert_eq!(updated.attempt_count, 1);
    assert_eq!(updated.payload, "{}");
    let keys = ids::idempotency_store(
        Arc::new(Memory::<idempotency::IdempotencyKey>::new(
            "acme.idempotency.v1.IdempotencyKey",
        )),
        options(),
    );
    let key = sx::IdempotencyKey {
        name: "idempotencyKeys/k".into(),
        method: "/api/Create".into(),
        request_id: "id".into(),
        ..Default::default()
    };
    keys.create(&ctx, key.clone()).await.unwrap();
    keys.settle(
        &ctx,
        sx::IdempotencyKey {
            state: sx::KeyState::Done,
            response: "recorded".into(),
            ..key
        },
    )
    .await
    .unwrap();
    let got = keys.get(&ctx, "idempotencyKeys/k").await.unwrap();
    assert_eq!(got.state, sx::KeyState::Done);
    assert_eq!(got.response, "recorded");
    assert_eq!(got.method, "/api/Create");
}

struct ConcurrentDelete {
    repo: Memory<directory::Organization>,
}
#[tonic::async_trait]
impl sx::Hooks<directory::Organization> for ConcurrentDelete {
    async fn before_delete(&self, _: &sx::Context, name: &str, _: bool) -> Result<(), Status> {
        self.repo.rows.lock().unwrap().get_mut(name).unwrap().etag = "concurrent".into();
        Ok(())
    }
}
#[tokio::test]
async fn delete_checks_etag_in_the_mutation_after_hooks() {
    let repo = Memory::new("acme.directory.v1.Organization");
    let s = ds::OrganizationsServer::new(
        options(),
        sx::Resource::new(repo.clone()).with_hooks(ConcurrentDelete { repo }),
    );
    let created = create(&s, "race").await;
    let err = s
        .delete_organization(Request::new(directory::DeleteOrganizationRequest {
            name: created.name.clone(),
            etag: created.etag,
            ..Default::default()
        }))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Aborted);
    assert!(
        s.get_organization(Request::new(directory::GetOrganizationRequest {
            name: created.name
        }))
        .await
        .unwrap()
        .into_inner()
        .delete_time
        .is_none()
    );
}
