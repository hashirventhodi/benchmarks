use axum::{
    extract::State,
    response::{
        sse::{Event, Sse},
        IntoResponse,
    },
    routing::get,
    Router,
};
use futures::stream::Stream;
use std::convert::Infallible;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::broadcast;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::StreamExt;

#[derive(Clone)]
struct AppState {
    tx: broadcast::Sender<String>,
    count: Arc<AtomicI64>,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (tx, _) = broadcast::channel::<String>(64);
    let count = Arc::new(AtomicI64::new(0));
    let state = AppState {
        tx: tx.clone(),
        count: Arc::clone(&count),
    };

    // Broadcaster: tick every second.
    {
        let tx = tx.clone();
        let count = Arc::clone(&count);
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(Duration::from_secs(1));
            tick.tick().await;
            loop {
                tick.tick().await;
                let now = SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_secs();
                let msg = format!("tick {now}");
                let sent = tx.send(msg).unwrap_or(0);
                eprintln!("clients={} sent={}", count.load(Ordering::Relaxed), sent);
            }
        });
    }

    let app = Router::new()
        .route("/stream", get(stream_handler))
        .route("/count", get(count_handler))
        .with_state(state);

    let addr = "0.0.0.0:8285";
    let listener = tokio::net::TcpListener::bind(addr).await?;
    eprintln!("rust stream listening on {addr}");
    axum::serve(listener, app).await?;
    Ok(())
}

async fn stream_handler(
    State(state): State<AppState>,
) -> Sse<impl Stream<Item = Result<Event, Infallible>>> {
    state.count.fetch_add(1, Ordering::Relaxed);
    let count = Arc::clone(&state.count);

    let rx = state.tx.subscribe();
    let bs = BroadcastStream::new(rx);

    let hello = futures::stream::once(async {
        Ok::<_, Infallible>(Event::default().event("hello").data("ok"))
    });

    let ticks = bs.filter_map(|res| res.ok()).map(|msg| {
        Ok::<_, Infallible>(Event::default().data(msg))
    });

    // Drop guard via custom struct: decrement on connection drop.
    struct Guard(Arc<AtomicI64>);
    impl Drop for Guard {
        fn drop(&mut self) {
            self.0.fetch_sub(1, Ordering::Relaxed);
        }
    }
    let guard = Guard(count);

    // Combine streams; guard lives for the lifetime of the stream.
    let stream = async_stream::stream! {
        let _g = guard;
        let combined = hello.chain(ticks);
        futures::pin_mut!(combined);
        while let Some(item) = combined.next().await {
            yield item;
        }
    };

    Sse::new(stream)
}

async fn count_handler(State(state): State<AppState>) -> impl IntoResponse {
    state.count.load(Ordering::Relaxed).to_string()
}
