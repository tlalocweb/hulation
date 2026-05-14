//! HLAP-side error types.
//!
//! Distinct from config::ConfigError because config errors fire on
//! startup and exit the process, while HLAP errors fire per-connection
//! and are surfaced to the client as `{"err":..., "code":...}` envelopes
//! without tearing down the agent.

use std::fmt;

use crate::client::ClientError;

/// Codes that ride in the `code` field of an err envelope. Stable
/// strings so clients (including LLMs picking branches) can switch on
/// them. New codes are additive; never repurpose.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HlapCode {
    BadEnvelope,
    UnknownVerb,
    MissingField,
    /// Hula rejected the agent's request as not permitted (403). The
    /// authoritative permission map is server-side; hulaagent's local
    /// allow map is informational only.
    Forbidden,
    /// Hula's agent-mTLS middleware refused the cert at TLS-handshake
    /// or registry-lookup time (401). Distinct from Forbidden because
    /// the operator response is different — Unauthorized means
    /// re-mint or unrevoke the agent, Forbidden means widen the
    /// allow map.
    Unauthorized,
    /// Hula returned a non-2xx other than 401/403/409 — usually 4xx
    /// shape errors (bad request, not found) or 5xx upstream issues.
    Upstream,
    /// Build for this site is already running. Specific code so
    /// scripts can back off vs. surface to the user.
    BuildInProgress,
    /// Couldn't reach hula at all (DNS / TCP / TLS-handshake failure).
    Network,
    /// Generic server-side / hulaagent-side error; covers JSON decode
    /// failures, unexpected response shapes, etc.
    InternalError,
}

impl HlapCode {
    pub fn as_str(self) -> &'static str {
        match self {
            HlapCode::BadEnvelope => "bad_envelope",
            HlapCode::UnknownVerb => "unknown_verb",
            HlapCode::MissingField => "missing_field",
            HlapCode::Forbidden => "forbidden",
            HlapCode::Unauthorized => "agent_unauthorized",
            HlapCode::Upstream => "upstream_error",
            HlapCode::BuildInProgress => "build_in_progress",
            HlapCode::Network => "network_error",
            HlapCode::InternalError => "internal_error",
        }
    }
}

/// Error coming out of envelope decode or per-verb dispatch. Carries
/// enough information to render an err envelope; the connection
/// handler maps this to the wire form.
#[derive(Debug)]
pub struct HlapError {
    pub stream: Option<u32>,
    pub err: String,
    pub code: HlapCode,
    pub detail: Option<String>,
}

impl HlapError {
    pub fn bad_envelope(detail: impl Into<String>) -> Self {
        Self {
            stream: None,
            err: "bad_envelope".into(),
            code: HlapCode::BadEnvelope,
            detail: Some(detail.into()),
        }
    }

    pub fn unknown_verb(stream: u32, verb: &str) -> Self {
        Self {
            stream: Some(stream),
            err: "unknown_verb".into(),
            code: HlapCode::UnknownVerb,
            detail: Some(format!("verb {:?} is not implemented in this hula-agent build", verb)),
        }
    }

    pub fn missing_field(stream: u32, field: &str) -> Self {
        Self {
            stream: Some(stream),
            err: "missing_field".into(),
            code: HlapCode::MissingField,
            detail: Some(format!("required field {:?} not present in envelope", field)),
        }
    }

    /// Map a low-level ClientError from a verb dispatch into a wire-
    /// shaped HlapError. The HTTP-status branches carry hula's
    /// response body verbatim in the detail field; the network arm
    /// surfaces the reqwest error string.
    pub fn from_client_err(stream: u32, e: ClientError) -> Self {
        let (err, code, detail) = match e {
            ClientError::Network(msg) => (
                "network_error".to_string(),
                HlapCode::Network,
                Some(msg),
            ),
            ClientError::Http { status: 401, body } => (
                "agent_unauthorized".to_string(),
                HlapCode::Unauthorized,
                Some(body),
            ),
            ClientError::Http { status: 403, body } => (
                "forbidden".to_string(),
                HlapCode::Forbidden,
                Some(body),
            ),
            ClientError::Http { status: 409, body } => (
                "build_in_progress".to_string(),
                HlapCode::BuildInProgress,
                Some(body),
            ),
            ClientError::Http { status, body } => (
                "upstream_error".to_string(),
                HlapCode::Upstream,
                Some(format!("HTTP {}: {}", status, body)),
            ),
            ClientError::Decode(msg) => (
                "internal_error".to_string(),
                HlapCode::InternalError,
                Some(format!("response decode: {}", msg)),
            ),
            // Build errors are startup-only and shouldn't reach a verb
            // dispatcher — if they do, surface them as internal.
            ClientError::Build(msg) => (
                "internal_error".to_string(),
                HlapCode::InternalError,
                Some(format!("client setup: {}", msg)),
            ),
        };
        Self {
            stream: Some(stream),
            err,
            code,
            detail,
        }
    }
}

impl fmt::Display for HlapError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.err)?;
        if let Some(d) = &self.detail {
            write!(f, " ({})", d)?;
        }
        Ok(())
    }
}

impl std::error::Error for HlapError {}
