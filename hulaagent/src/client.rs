//! mTLS HTTP client to hula.
//!
//! Built once per agent process from the PEM blobs in the loaded
//! agent yaml. Each verb dispatcher (BUILD in step 2a; the rest in
//! step 2b/3) calls a typed method on the client and gets back a
//! parsed response or a structured ClientError that the HLAP layer
//! maps to an err envelope.
//!
//! Server-cert verification uses system roots + the optional CA in
//! the agent yaml (additive). Client-cert auth presents the agent's
//! leaf via reqwest::Identity. SNI / hostname pinning follows
//! `cfg.agent.hula_host`; the URL we build always uses that host.

use serde::Deserialize;

use crate::config::AgentConfig;

/// Errors from the HTTP client. The HLAP layer maps these onto wire
/// err strings + HlapCode values; everything below is hidden from
/// the HLAP client.
#[derive(Debug)]
pub enum ClientError {
    /// reqwest::Client::build or Identity parsing failed at startup.
    /// Surfaces as a process-exit error from main, not a per-verb
    /// envelope — we only build the client once and refuse to run if
    /// it can't be constructed.
    Build(String),
    /// Network-layer failure (DNS, TCP, TLS handshake) reaching hula.
    Network(String),
    /// Hula returned a non-2xx status; status + body for caller to
    /// surface via the err envelope.
    Http { status: u16, body: String },
    /// Body parsed but didn't match the expected JSON shape for the
    /// verb's response.
    Decode(String),
}

impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ClientError::Build(s) => write!(f, "build client: {}", s),
            ClientError::Network(s) => write!(f, "network: {}", s),
            ClientError::Http { status, body } => {
                let snippet: String = body.chars().take(200).collect();
                write!(f, "hula returned HTTP {}: {}", status, snippet)
            }
            ClientError::Decode(s) => write!(f, "decode response: {}", s),
        }
    }
}

impl std::error::Error for ClientError {}

/// Response shape for `POST /api/agent/build`. Mirrors the Go
/// handler's triggerBuildResponse — keep in sync. Only `build_id`
/// is consumed by the verb dispatcher today; `status` here is the
/// trigger-time status ("build_triggered") and we rely on the
/// log-stream's terminal envelope for the actual final status.
/// Kept on the struct for forward-compat and so the field gets
/// validated as present in the JSON.
#[derive(Debug, Deserialize)]
pub struct TriggerBuildResponse {
    pub build_id: String,
    #[allow(dead_code)]
    pub status: String,
}

/// Long-lived mTLS HTTP client. Constructed once at startup; cloned
/// cheaply via Arc by the HLAP connection handlers.
pub struct HulaClient {
    inner: reqwest::Client,
    base_url: String,
}

impl HulaClient {
    pub fn new(cfg: &AgentConfig) -> Result<Self, ClientError> {
        // reqwest::Identity::from_pem wants the cert + key concatenated
        // in one PEM bundle. The agent yaml stores them as separate
        // strings; we splice them here. Newline between is mandatory
        // when the cert doesn't already end with one (yaml block
        // scalars sometimes drop trailing newlines).
        let mut bundle = String::with_capacity(cfg.agent.mtls.cert.len() + cfg.agent.mtls.key.len() + 1);
        bundle.push_str(&cfg.agent.mtls.cert);
        if !cfg.agent.mtls.cert.ends_with('\n') {
            bundle.push('\n');
        }
        bundle.push_str(&cfg.agent.mtls.key);

        let identity = reqwest::Identity::from_pem(bundle.as_bytes())
            .map_err(|e| ClientError::Build(format!("identity: {}", e)))?;

        let mut builder = reqwest::Client::builder()
            .use_rustls_tls()
            .identity(identity)
            // Don't follow redirects — internal endpoints shouldn't
            // ever 30x, and a 30x would mask permission/routing bugs.
            .redirect(reqwest::redirect::Policy::none())
            // Hula's build-trigger handler returns quickly (it's
            // async on the server side); short timeout catches a
            // wedged listener early without affecting normal flow.
            .timeout(std::time::Duration::from_secs(30));

        if !cfg.agent.mtls.ca.trim().is_empty() {
            let ca = reqwest::Certificate::from_pem(cfg.agent.mtls.ca.as_bytes())
                .map_err(|e| ClientError::Build(format!("ca: {}", e)))?;
            builder = builder.add_root_certificate(ca);
        }

        let inner = builder
            .build()
            .map_err(|e| ClientError::Build(format!("client: {}", e)))?;

        let base_url = format!("https://{}", cfg.agent.hula_host);

        Ok(Self { inner, base_url })
    }

    /// POST /api/agent/build — synchronous trigger; hula returns the
    /// new build_id immediately and the build runs in the background.
    /// `open_build_stream` consumes the build's log output once a
    /// build_id is in hand.
    pub async fn trigger_build(&self, site: &str) -> Result<TriggerBuildResponse, ClientError> {
        let url = format!("{}/api/agent/build", self.base_url);
        let resp = self
            .inner
            .post(&url)
            .json(&serde_json::json!({ "site": site }))
            .send()
            .await
            .map_err(|e| ClientError::Network(e.to_string()))?;

        let status = resp.status();
        let bytes = resp
            .bytes()
            .await
            .map_err(|e| ClientError::Network(format!("read body: {}", e)))?;

        if !status.is_success() {
            let body = String::from_utf8_lossy(&bytes).into_owned();
            return Err(ClientError::Http {
                status: status.as_u16(),
                body,
            });
        }

        serde_json::from_slice::<TriggerBuildResponse>(&bytes)
            .map_err(|e| ClientError::Decode(format!("{}; body was: {}", e, String::from_utf8_lossy(&bytes))))
    }

    /// GET /api/agent/build/{build_id}/stream — opens an NDJSON
    /// streaming response. The caller drives the body stream via
    /// `Response::bytes_stream()` and parses one `\n`-delimited JSON
    /// object per envelope. Override timeout to None so a long-
    /// running build doesn't trip the per-request 30s budget the
    /// client was constructed with.
    pub async fn open_build_stream(&self, build_id: &str) -> Result<reqwest::Response, ClientError> {
        let url = format!("{}/api/agent/build/{}/stream", self.base_url, build_id);
        let resp = self
            .inner
            .get(&url)
            // Streaming response can outlast the client's default
            // 30s timeout. Per-request override.
            .timeout(std::time::Duration::from_secs(3600))
            .send()
            .await
            .map_err(|e| ClientError::Network(e.to_string()))?;

        if !resp.status().is_success() {
            let status = resp.status().as_u16();
            let body = resp.text().await.unwrap_or_default();
            return Err(ClientError::Http { status, body });
        }
        Ok(resp)
    }
}
