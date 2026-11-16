//! Exercises every generated RPC shape over a real tonic connection.
use service_rust_examples::{
    pb::streams::v1::{Entry, entries_client::EntriesClient},
    service::{
        servicex::{Options, ResponseStream},
        streams::v1::{EntriesHandlers, EntriesServer},
    },
};
use std::time::Duration;
use tokio_stream::wrappers::TcpListenerStream;
use tonic::{Request, Status, Streaming};

fn entry(value: &str) -> Entry {
    Entry {
        r#type: value.into(),
    }
}

// One handler per RPC, every shape: unary, server-, client- and bidirectional
// streaming.
fn handlers() -> EntriesHandlers {
    EntriesHandlers::default()
        .wrap(|_, value: String| async move { Ok(value.into_bytes()) })
        .watch(|ctx, value: Entry| async move {
            // Metadata must survive the generated forwarding layer.
            if ctx.metadata.get("x-test").and_then(|v| v.to_str().ok()) != Some("watch") {
                return Err(Status::invalid_argument("missing metadata"));
            }
            let out: ResponseStream<Entry> = Box::pin(tokio_stream::iter([
                Ok(value.clone()),
                Ok(value),
                Err(Status::aborted("stream failed")),
            ]));
            Ok(out)
        })
        .upload(|_, mut input: Streaming<Entry>| async move {
            let mut values = Vec::new();
            while let Some(value) = input.message().await? {
                values.push(value.r#type);
            }
            Ok(entry(&values.join(",")))
        })
        .exchange(|_, input: Streaming<Entry>| async move {
            // Forward each message as it arrives, without waiting for the client to close.
            let out: ResponseStream<Entry> = Box::pin(input);
            Ok(out)
        })
}

#[tokio::test]
async fn all_rpc_kinds_round_trip() {
    tokio::time::timeout(Duration::from_secs(10), async {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (shutdown, stopped) = tokio::sync::oneshot::channel();
        let options = Options::new(
            prost_reflect::DescriptorPool::decode(include_bytes!("../descriptor.bin").as_slice())
                .unwrap(),
        );
        let server = tokio::spawn(
            tonic::transport::Server::builder()
                .add_service(
                    EntriesServer::new(options, handlers())
                        .unwrap()
                        .into_service(),
                )
                .serve_with_incoming_shutdown(TcpListenerStream::new(listener), async {
                    let _ = stopped.await;
                }),
        );
        let mut client = EntriesClient::connect(format!("http://{address}"))
            .await
            .unwrap();

        assert_eq!(
            client.wrap("hello".to_owned()).await.unwrap().into_inner(),
            b"hello"
        );

        let mut request = Request::new(entry("server"));
        request
            .metadata_mut()
            .insert("x-test", "watch".parse().unwrap());
        let mut output = client.watch(request).await.unwrap().into_inner();
        assert_eq!(output.message().await.unwrap().unwrap().r#type, "server");
        assert_eq!(output.message().await.unwrap().unwrap().r#type, "server");
        assert_eq!(
            output.message().await.unwrap_err().code(),
            tonic::Code::Aborted
        );
        drop(output);

        assert_eq!(
            client
                .upload(tokio_stream::iter([entry("a"), entry("b")]))
                .await
                .unwrap()
                .into_inner()
                .r#type,
            "a,b"
        );
        assert_eq!(
            client
                .upload(tokio_stream::empty::<Entry>())
                .await
                .unwrap()
                .into_inner()
                .r#type,
            ""
        );

        let (send, recv) = tokio::sync::mpsc::channel(1);
        let mut output = client
            .exchange(tokio_stream::wrappers::ReceiverStream::new(recv))
            .await
            .unwrap()
            .into_inner();
        for value in ["first", "second"] {
            send.send(entry(value)).await.unwrap();
            assert_eq!(output.message().await.unwrap().unwrap().r#type, value);
        }
        drop(send);
        assert!(output.message().await.unwrap().is_none());
        drop(output);
        drop(client);
        shutdown.send(()).unwrap();
        server.await.unwrap().unwrap();
    })
    .await
    .expect("streaming RPCs must complete without deadlock");
}
