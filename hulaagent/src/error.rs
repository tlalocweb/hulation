//! HLAP-side error types.
//!
//! Distinct from config::ConfigError because config errors fire on
//! startup and exit the process, while HLAP errors fire per-connection
//! and are surfaced to the client as `{"err":..., "code":...}` envelopes
//! without tearing down the agent.

use std::fmt;

/// Codes that ride in the `code` field of an err envelope. Stable
/// strings so clients (including LLMs picking branches) can switch on
/// them. New codes are additive; never repurpose.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HlapCode {
    BadEnvelope,
    UnknownVerb,
    // MissingField is used by step-2 verbs whose envelopes have
    // required fields (e.g. BUILD's `site`). Constructor lives on
    // HlapError below.
    #[allow(dead_code)]
    MissingField,
    // InternalError is reserved for step-2's mTLS / HTTP failures.
    #[allow(dead_code)]
    InternalError,
}

impl HlapCode {
    pub fn as_str(self) -> &'static str {
        match self {
            HlapCode::BadEnvelope => "bad_envelope",
            HlapCode::UnknownVerb => "unknown_verb",
            HlapCode::MissingField => "missing_field",
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

    #[allow(dead_code)] // used by step-2 verb dispatch
    pub fn missing_field(stream: u32, field: &str) -> Self {
        Self {
            stream: Some(stream),
            err: "missing_field".into(),
            code: HlapCode::MissingField,
            detail: Some(format!("required field {:?} not present in envelope", field)),
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
