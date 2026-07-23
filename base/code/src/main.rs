use axum::{routing::get, Json, Router};
use codefly_sdk::Codefly;
use serde::Serialize;

#[derive(Serialize)]
struct HealthResponse {
    status: String,
}

async fn health() -> Json<HealthResponse> {
    Json(HealthResponse {
        status: "ok".to_string(),
    })
}

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt::init();

    // Runtime carrier names belong to the Codefly SDK. A generated service
    // resolves its own `rest` endpoint without knowing how Codefly encodes it.
    let addr = Codefly::from_env()
        .and_then(|codefly| codefly.query().api("rest").network_instance())
        .map(|network| network.host)
        .unwrap_or_else(|| "0.0.0.0:8080".to_string());

    let app = Router::new().route("/health", get(health));

    tracing::info!("listening on {}", addr);

    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .expect("failed to bind");

    axum::serve(listener, app).await.expect("server error");
}
