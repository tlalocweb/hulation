//! HLAP wire protocol — banner emission, envelope decoding,
//! envelope writing.
//!
//! See HULAAGENT_PLAN.md §HLAP for the wire spec this implements.
//! In Phase 4 step 1 (this slice) we cover:
//!
//!   - Banner emitted exactly once on connect.
//!   - One-JSON-object-per-line decoding of incoming envelopes.
//!   - Outgoing envelope writers (`ok`, `err`, `log`).
//!   - A connection handler that responds with `unknown_verb` for
//!     anything that arrives — until step 2 wires the real verbs.
//!
//! Multiplexed bookkeeping (max_inflight, in-flight stream map) and
//! the session registry come in step 3.

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;

use crate::error::HlapError;

/// HLAP protocol version emitted in the banner. Bumped only on
/// breaking wire changes.
pub const HLAP_VERSION: u32 = 1;

/// hulaagent binary major version emitted in the banner. Informational;
/// clients should branch on `hlap` not `hulaagent`.
pub const HULAAGENT_VERSION: u32 = 1;

/// Per-connection cap on in-flight verbs. The accept loop will reject
/// new verb envelopes with `err:"max_inflight"` once this many are
/// in flight on a single connection. Phase 4 step 1 doesn't actually
/// enforce the cap (single-stream serial handling); step 3 wires it
/// in when the in-flight map lands.
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
    /// Verb-specific fields land here; step-2 verb implementations
    /// pluck `site`, `message`, `path`, etc. from this map. Step 1
    /// only routes on `verb` so this is unused for now.
    #[allow(dead_code)]
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
    // Re-extract the flattened extra: serde_json::Value → owned Map
    // minus the keys we've already pulled. Cheaper than running a
    // second deserialize pass against a strongly-typed struct.
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

/// Per-connection handler. Runs until the client closes the socket
/// or a fatal I/O error occurs.
///
/// Step-1 behaviour: emits the banner, decodes one envelope at a
/// time, replies with `unknown_verb` for everything (no verb
/// dispatch yet). The loop is sequential — step 3 will spawn verb
/// handlers concurrently keyed by `stream`.
pub async fn serve_connection(stream: UnixStream) -> std::io::Result<()> {
    let (reader, mut writer) = stream.into_split();
    let mut reader = BufReader::new(reader);

    // Banner first, before any read. The agent advertises capabilities
    // unconditionally; clients that don't grok the shape disconnect.
    write_envelope(&mut writer, &Banner::current()).await?;

    let mut line = String::new();
    loop {
        line.clear();
        let n = reader.read_line(&mut line).await?;
        if n == 0 {
            // EOF — client closed cleanly. Step-1 has no in-flight
            // state to drain; later steps will cancel any in-flight
            // verbs on this connection here.
            return Ok(());
        }
        // Skip blank lines silently — friendly to clients that send
        // trailing newlines or use heredocs.
        if line.trim().is_empty() {
            continue;
        }

        let env = match decode_envelope(line.trim_end_matches('\n')) {
            Ok(e) => e,
            Err(e) => {
                write_envelope(&mut writer, &err_envelope(&e)).await?;
                // Decode failure isn't fatal to the connection; the
                // client may have sent garbage in one envelope and
                // still want to use the connection for valid ones.
                continue;
            }
        };

        // Step-1: no verb is implemented. Every envelope gets
        // `unknown_verb`. Step 2 introduces BUILD; step 3 widens to
        // the full verb table.
        let e = HlapError::unknown_verb(env.stream, &env.verb);
        write_envelope(&mut writer, &err_envelope(&e)).await?;
    }
}
