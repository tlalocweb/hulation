//! HLAP wire protocol — banner emission, envelope decoding,
//! envelope writing, per-verb dispatch.
//!
//! See HULAAGENT_PLAN.md §HLAP for the wire spec this implements.
//!
//! Phase 4 step 2a (current) scope:
//!   - Banner emitted exactly once on connect.
//!   - One-JSON-object-per-line decoding of incoming envelopes.
//!   - BUILD verb: forwards to hula via HulaClient, returns
//!     `{ok:true,done:true,session,build_id,status}` in a single
//!     combined envelope (short-output form allowed by the spec).
//!     Log streaming follows in step 2b.
//!   - Unknown verbs still get `unknown_verb`.
//!
//! Multiplexed bookkeeping (max_inflight, in-flight stream map) and
//! the session registry for cancel routing land in step 3 — this
//! commit treats `session` IDs as throwaway, agent-only strings.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;

use crate::client::HulaClient;
use crate::error::HlapError;

/// HLAP protocol version emitted in the banner. Bumped only on
/// breaking wire changes.
pub const HLAP_VERSION: u32 = 1;

/// hulaagent binary major version emitted in the banner. Informational;
/// clients should branch on `hlap` not `hulaagent`.
pub const HULAAGENT_VERSION: u32 = 1;

/// Per-connection cap on in-flight verbs. The accept loop will reject
/// new verb envelopes with `err:"max_inflight"` once this many are
/// in flight on a single connection. Step 2a is still sequential;
/// step 3 wires the cap when the in-flight map lands.
pub const MAX_INFLIGHT: u32 = 16;

/// Banner sent once on connect, before any read.
#[derive(Serialize)]
pub struct Banner {
    pub hulaagent: u32,
    pub hlap: u32,
    pub streams: bool,
    pub max_inflight: u32,
}

impl Banner {
    pub fn current() -> Self {
        Self {
            hulaagent: HULAAGENT_VERSION,
            hlap: HLAP_VERSION,
            streams: true,
            max_inflight: MAX_INFLIGHT,
        }
    }
}

/// Decoded incoming envelope. `verb` and `stream` are required and
/// extracted up front so the handler can route + reject early; the
/// remaining fields ride in `extra` for the verb implementation to
/// pull what it needs.
#[derive(Debug, Deserialize)]
pub struct VerbEnvelope {
    pub verb: String,
    pub stream: u32,
    #[serde(flatten)]
    pub extra: Map<String, Value>,
}

/// Decode one JSON line into a VerbEnvelope, mapping any failure to
/// an HlapError with enough detail to render a client-facing err.
pub fn decode_envelope(line: &str) -> Result<VerbEnvelope, HlapError> {
    let v: Value = serde_json::from_str(line).map_err(|e| {
        HlapError::bad_envelope(format!("invalid JSON: {}", e))
    })?;
    let obj = v.as_object().ok_or_else(|| {
        HlapError::bad_envelope("envelope must be a JSON object")
    })?;
    let stream = obj
        .get("stream")
        .and_then(|s| s.as_u64())
        .and_then(|n| u32::try_from(n).ok())
        .ok_or_else(|| HlapError::bad_envelope("missing or non-u32 \"stream\" field"))?;
    let verb = obj
        .get("verb")
        .and_then(|s| s.as_str())
        .ok_or_else(|| HlapError::bad_envelope("missing or non-string \"verb\" field"))?
        .to_string();
    let mut extra = obj.clone();
    extra.remove("verb");
    extra.remove("stream");
    Ok(VerbEnvelope { verb, stream, extra })
}

/// Serialize and write a JSON envelope as one line (terminator: `\n`).
/// Caller is responsible for flush ordering across multiple writes.
pub async fn write_envelope<T: Serialize>(
    stream: &mut tokio::net::unix::OwnedWriteHalf,
    env: &T,
) -> std::io::Result<()> {
    let mut bytes = serde_json::to_vec(env).map_err(|e| {
        std::io::Error::new(std::io::ErrorKind::InvalidData, e)
    })?;
    bytes.push(b'\n');
    stream.write_all(&bytes).await
}

/// Build an err envelope ready to serialise. The serialised form has
/// no `done` key (the spec is explicit: an err terminates the stream
/// without a `done:true` follow-up).
pub fn err_envelope(e: &HlapError) -> Value {
    let mut m = Map::new();
    if let Some(s) = e.stream {
        m.insert("stream".into(), Value::from(s));
    }
    m.insert("err".into(), Value::from(e.err.clone()));
    m.insert("code".into(), Value::from(e.code.as_str()));
    if let Some(d) = &e.detail {
        m.insert("detail".into(), Value::from(d.clone()));
    }
    Value::Object(m)
}

/// Mint an opaque session id. v1 uses nanos-since-epoch + a tiny
/// per-process counter; not cryptographically random but session IDs
/// are cancellation keys (the agent's own registry mediates cancel
/// routing), not security tokens. Step 3 may swap to a random source
/// when the cancel verb lands.
fn mint_session_id() -> String {
    use std::sync::atomic::{AtomicU64, Ordering};
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let ns = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    let n = COUNTER.fetch_add(1, Ordering::Relaxed);
    format!("sess_{:x}_{:x}", ns, n)
}

/// Per-connection handler. Runs until the client closes the socket
/// or a fatal I/O error occurs.
///
/// Step-2a behaviour: emits the banner, decodes envelopes
/// sequentially, dispatches `build` via `HulaClient`, returns
/// `unknown_verb` for anything else. Step 3 makes dispatch
/// concurrent across streams.
pub async fn serve_connection(stream: UnixStream, client: Arc<HulaClient>) -> std::io::Result<()> {
    let (reader, mut writer) = stream.into_split();
    let mut reader = BufReader::new(reader);

    write_envelope(&mut writer, &Banner::current()).await?;

    let mut line = String::new();
    loop {
        line.clear();
        let n = reader.read_line(&mut line).await?;
        if n == 0 {
            return Ok(());
        }
        if line.trim().is_empty() {
            continue;
        }

        let env = match decode_envelope(line.trim_end_matches('\n')) {
            Ok(e) => e,
            Err(e) => {
                write_envelope(&mut writer, &err_envelope(&e)).await?;
                continue;
            }
        };

        let stream_id = env.stream;
        let verb = env.verb.clone();
        let result = dispatch(&env, &client).await;
        match result {
            Ok(reply) => {
                write_envelope(&mut writer, &reply).await?;
            }
            Err(e) => {
                write_envelope(&mut writer, &err_envelope(&e)).await?;
            }
        }
        // Silence the unused-warning if we ever reorder fields later.
        let _ = (stream_id, verb);
    }
}

/// Route an envelope to its verb handler. Returns the success
/// envelope to write, or an HlapError that the caller serialises.
async fn dispatch(env: &VerbEnvelope, client: &HulaClient) -> Result<Value, HlapError> {
    match env.verb.as_str() {
        "build" => dispatch_build(env, client).await,
        _ => Err(HlapError::unknown_verb(env.stream, &env.verb)),
    }
}

/// BUILD verb. Required field: `site`. Step 2a returns one combined
/// terminal envelope (no log envelopes). Permission gating happens
/// server-side via the agent-mTLS middleware + registry, not here.
async fn dispatch_build(env: &VerbEnvelope, client: &HulaClient) -> Result<Value, HlapError> {
    let site = env
        .extra
        .get("site")
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| HlapError::missing_field(env.stream, "site"))?;

    let resp = client
        .trigger_build(site)
        .await
        .map_err(|e| HlapError::from_client_err(env.stream, e))?;

    let session = mint_session_id();
    let mut m = Map::new();
    m.insert("stream".into(), Value::from(env.stream));
    m.insert("ok".into(), Value::from(true));
    m.insert("done".into(), Value::from(true));
    m.insert("session".into(), Value::from(session));
    m.insert("build_id".into(), Value::from(resp.build_id));
    m.insert("status".into(), Value::from(resp.status));
    Ok(Value::Object(m))
}
