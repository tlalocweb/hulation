//! HLAP wire protocol — banner emission, envelope decoding,
//! envelope writing, per-verb dispatch.
//!
//! See HULAAGENT_PLAN.md §HLAP for the wire spec this implements.
//!
//! Phase 4 step 2b scope:
//!   - Banner emitted exactly once on connect.
//!   - One-JSON-object-per-line decoding of incoming envelopes.
//!   - BUILD verb: forwards to hula via HulaClient, then opens the
//!     log-stream endpoint and emits one `{"stream":N,"log":"..."}`
//!     envelope per log line, finally a terminating
//!     `{ok:true,done:true,build_id,status}` envelope.
//!   - Unknown verbs still get `unknown_verb`.
//!
//! Verb dispatchers take the connection's write half directly so
//! they can emit multiple envelopes per verb. Multiplexed
//! bookkeeping across verbs (in-flight stream map, cancel routing)
//! lands in step 3 — this commit serialises verb dispatch within
//! a single connection.

use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::unix::OwnedWriteHalf;
use tokio::net::UnixStream;

use crate::client::HulaClient;
use crate::error::{HlapCode, HlapError};

/// HLAP protocol version emitted in the banner. Bumped only on
/// breaking wire changes.
pub const HLAP_VERSION: u32 = 1;

/// hulaagent binary major version emitted in the banner. Informational;
/// clients should branch on `hlap` not `hulaagent`.
pub const HULAAGENT_VERSION: u32 = 1;

/// Per-connection cap on in-flight verbs. Step 3 wires the cap when
/// the in-flight map lands; today verbs are serialised on the
/// connection so it's effectively 1.
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
        .ok_or_else(|| HlapError {
            // Stream parsed cleanly above, so echo it back on this
            // err even though verb itself was malformed.
            stream: Some(stream),
            err: "bad_envelope".into(),
            code: HlapCode::BadEnvelope,
            detail: Some("missing or non-string \"verb\" field".into()),
        })?
        .to_string();
    let mut extra = obj.clone();
    extra.remove("verb");
    extra.remove("stream");
    Ok(VerbEnvelope { verb, stream, extra })
}

/// Serialize and write a JSON envelope as one line (terminator: `\n`).
/// Caller is responsible for flush ordering across multiple writes.
pub async fn write_envelope<T: Serialize>(
    stream: &mut OwnedWriteHalf,
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

/// Mint an opaque session id. v1 uses nanos-since-epoch + a per-
/// process counter; not cryptographically random but session IDs
/// are cancellation keys (the agent's own registry mediates cancel
/// routing), not security tokens. Step 3 may swap to a random
/// source when the cancel verb lands.
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

/// Map an io::Error from a writer call onto an HlapError. The
/// stream context is the verb that was writing when the failure
/// hit. Used to render an err envelope as the last write attempt
/// before the connection torn down.
fn io_to_hlap(stream: u32, e: std::io::Error) -> HlapError {
    HlapError {
        stream: Some(stream),
        err: "io_error".into(),
        code: HlapCode::InternalError,
        detail: Some(e.to_string()),
    }
}

/// Per-connection handler. Runs until the client closes the socket
/// or a fatal I/O error occurs.
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

        if let Err(e) = dispatch(&env, &client, &mut writer).await {
            write_envelope(&mut writer, &err_envelope(&e)).await?;
        }
    }
}

/// Route an envelope to its verb handler. Verb dispatchers write
/// their own envelopes directly via the writer (so they can stream);
/// the returned Result<()> is only for error termination — an Err
/// is converted to a single err envelope.
async fn dispatch(
    env: &VerbEnvelope,
    client: &HulaClient,
    writer: &mut OwnedWriteHalf,
) -> Result<(), HlapError> {
    match env.verb.as_str() {
        "build" => dispatch_build(env, client, writer).await,
        _ => Err(HlapError::unknown_verb(env.stream, &env.verb)),
    }
}

/// BUILD verb. Required field: `site`. Triggers the build on hula,
/// emits an initial OK envelope, then streams `log` envelopes
/// extracted from hula's NDJSON log-stream endpoint, then a
/// terminating OK envelope with the build's final status.
///
/// Permission gating happens server-side at hula via the agent-mTLS
/// middleware + registry — hulaagent forwards the verb and surfaces
/// whatever hula returns (403 → err:"forbidden" envelope).
async fn dispatch_build(
    env: &VerbEnvelope,
    client: &HulaClient,
    writer: &mut OwnedWriteHalf,
) -> Result<(), HlapError> {
    let site = env
        .extra
        .get("site")
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| HlapError::missing_field(env.stream, "site"))?;

    let trigger = client
        .trigger_build(site)
        .await
        .map_err(|e| HlapError::from_client_err(env.stream, e))?;

    let session = mint_session_id();

    // Initial OK envelope — signals "verb accepted, stream coming".
    // build_id rides here so a client that doesn't care about logs
    // can act on it immediately.
    {
        let mut m = Map::new();
        m.insert("stream".into(), Value::from(env.stream));
        m.insert("ok".into(), Value::from(true));
        m.insert("session".into(), Value::from(session.clone()));
        m.insert("streaming".into(), Value::from(true));
        m.insert("build_id".into(), Value::from(trigger.build_id.clone()));
        write_envelope(writer, &Value::Object(m))
            .await
            .map_err(|e| io_to_hlap(env.stream, e))?;
    }

    // Open the log-stream endpoint. If this fails (hula 404'd the
    // build_id, network blip, etc.) we emit an err envelope and
    // return — the stream ends without `done:true`.
    let resp = client
        .open_build_stream(&trigger.build_id)
        .await
        .map_err(|e| HlapError::from_client_err(env.stream, e))?;

    // Drain the NDJSON body. Each line is one server envelope:
    //   {"type":"log","line":"..."}
    //   {"type":"end","status":"...","build_id":"...","error":""}
    // We translate to HLAP envelopes as they arrive, flushing
    // implicitly via tokio's socket write.
    let mut body = resp.bytes_stream();
    let mut buf: Vec<u8> = Vec::new();
    let mut saw_end = false;

    while let Some(chunk) = body.next().await {
        let chunk = chunk.map_err(|e| HlapError {
            stream: Some(env.stream),
            err: "network_error".into(),
            code: HlapCode::Network,
            detail: Some(format!("log stream: {}", e)),
        })?;
        buf.extend_from_slice(&chunk);

        // Process complete `\n`-delimited lines from the buffer.
        // Leftover bytes (a partial line at the end of the chunk)
        // stay in `buf` for the next iteration.
        while let Some(nl_idx) = buf.iter().position(|&b| b == b'\n') {
            let line: Vec<u8> = buf.drain(..=nl_idx).collect();
            // Strip the trailing newline. Empty lines (e.g. CRLF or
            // a stray \n in the stream) are skipped.
            let line_bytes = &line[..line.len().saturating_sub(1)];
            if line_bytes.iter().all(|&b| b == b' ' || b == b'\t' || b == b'\r') {
                continue;
            }

            let parsed: Value = serde_json::from_slice(line_bytes).map_err(|e| HlapError {
                stream: Some(env.stream),
                err: "internal_error".into(),
                code: HlapCode::InternalError,
                detail: Some(format!("ndjson decode: {}", e)),
            })?;

            let env_type = parsed
                .get("type")
                .and_then(|v| v.as_str())
                .unwrap_or("");
            match env_type {
                "log" => {
                    let log_line = parsed
                        .get("line")
                        .and_then(|v| v.as_str())
                        .unwrap_or("");
                    let mut m = Map::new();
                    m.insert("stream".into(), Value::from(env.stream));
                    m.insert("log".into(), Value::from(log_line));
                    write_envelope(writer, &Value::Object(m))
                        .await
                        .map_err(|e| io_to_hlap(env.stream, e))?;
                }
                "end" => {
                    saw_end = true;
                    let status = parsed
                        .get("status")
                        .and_then(|v| v.as_str())
                        .unwrap_or("unknown")
                        .to_string();
                    let mut m = Map::new();
                    m.insert("stream".into(), Value::from(env.stream));
                    m.insert("ok".into(), Value::from(true));
                    m.insert("done".into(), Value::from(true));
                    m.insert("session".into(), Value::from(session.clone()));
                    m.insert("build_id".into(), Value::from(trigger.build_id.clone()));
                    m.insert("status".into(), Value::from(status));
                    if let Some(err_text) = parsed
                        .get("error")
                        .and_then(|v| v.as_str())
                        .filter(|s| !s.is_empty())
                    {
                        m.insert("error".into(), Value::from(err_text));
                    }
                    write_envelope(writer, &Value::Object(m))
                        .await
                        .map_err(|e| io_to_hlap(env.stream, e))?;
                }
                _ => {
                    // Unknown NDJSON type — forward-compat: ignore.
                    // Hula might add new types (warn, progress, etc.)
                    // without our knowing; better to drop them than
                    // tear the stream down.
                }
            }
        }
    }

    // Body closed without a `type:"end"` envelope. Shouldn't happen
    // against a well-behaved hula, but surface it cleanly so the
    // client doesn't wait forever on a half-open stream.
    if !saw_end {
        return Err(HlapError {
            stream: Some(env.stream),
            err: "incomplete_stream".into(),
            code: HlapCode::Upstream,
            detail: Some("log stream ended without type:\"end\" envelope".into()),
        });
    }

    Ok(())
}
