//! Agent configuration — the yaml that `hulactl create-agent`
//! produces and that `hula-agent -c <path>` consumes.
//!
//! The schema is documented in HULAAGENT_PLAN.md. This module is the
//! Rust-side authority on the layout; the Go-side hulactl writer must
//! produce something this can deserialize.

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::Path;

/// Top-level agent config, deserialized from the yaml hulactl produces.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AgentConfig {
    pub agent: AgentIdentity,
    pub sites: BTreeMap<String, SiteAllow>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AgentIdentity {
    /// Server-issued unique ID. Embedded in the cert's CN as
    /// `agent:<id>`; hula uses it to look up permissions on each
    /// connection.
    pub id: String,
    /// Server hula listens on (host:port). The agent dials this and
    /// pins the server cert via the CA bundle below.
    pub hula_host: String,
    /// mTLS material — inline PEM so a single yaml is the whole
    /// distribution unit.
    #[serde(rename = "mTLS")]
    pub mtls: MtlsBundle,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct MtlsBundle {
    /// PEM-encoded CA certificate hula's serving cert chains to.
    /// Used to verify the SERVER side of the mTLS handshake.
    pub ca: String,
    /// PEM-encoded client cert presented to hula. Subject CN must
    /// be `agent:<id>`; signed by hula's Agent CA.
    pub cert: String,
    /// PEM-encoded private key matching `cert`. Operator-secret —
    /// the yaml file inherits the same handling-class as a JWT or
    /// SSH key.
    pub key: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct SiteAllow {
    /// Per-verb allow list. The string value is the option payload
    /// for that verb (empty = bare allow). See HULAAGENT_PLAN.md
    /// for the verbs and the semantics.
    pub allow: BTreeMap<String, String>,
}

impl AgentConfig {
    /// Load + parse a config from a yaml file on disk. Errors
    /// propagate verbatim so the caller can render whichever stage
    /// failed (open vs parse).
    pub fn load(path: &Path) -> Result<Self, ConfigError> {
        let bytes = std::fs::read(path).map_err(ConfigError::Read)?;
        let cfg: AgentConfig = serde_yaml::from_slice(&bytes).map_err(ConfigError::Parse)?;
        cfg.validate()?;
        Ok(cfg)
    }

    /// Sanity checks: bail fast on configs hula would also reject.
    /// Cheaper to fail at boot with a clear message than to surface
    /// the same error per-RPC later.
    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.agent.id.is_empty() {
            return Err(ConfigError::Validation("agent.id is empty"));
        }
        if self.agent.hula_host.is_empty() {
            return Err(ConfigError::Validation("agent.hula_host is empty"));
        }
        if self.agent.mtls.ca.is_empty()
            || self.agent.mtls.cert.is_empty()
            || self.agent.mtls.key.is_empty()
        {
            return Err(ConfigError::Validation(
                "agent.mTLS.{ca,cert,key} must all be present",
            ));
        }
        Ok(())
    }

    /// Returns true iff this agent is allowed to invoke `verb` on
    /// `site`. The option string registered for the verb is what
    /// hulaagent will send on the wire; HLAP callers do not get to
    /// override it.
    pub fn is_allowed(&self, site: &str, verb: &str) -> Option<&str> {
        self.sites
            .get(site)
            .and_then(|s| s.allow.get(verb))
            .map(String::as_str)
    }
}

/// Errors raised during config load. Distinguished so the caller can
/// log differently for "couldn't open" vs "yaml shape wrong" vs
/// "schema-valid but nonsensical."
#[derive(Debug)]
pub enum ConfigError {
    Read(std::io::Error),
    Parse(serde_yaml::Error),
    Validation(&'static str),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Read(e) => write!(f, "read config: {}", e),
            Self::Parse(e) => write!(f, "parse config: {}", e),
            Self::Validation(msg) => write!(f, "config validation: {}", msg),
        }
    }
}

impl std::error::Error for ConfigError {}

#[cfg(test)]
mod tests {
    use super::*;

    const SAMPLE: &str = r#"
agent:
  id: testid123
  hula_host: hula.example.com:443
  mTLS:
    ca: |
      -----BEGIN CERTIFICATE-----
      DUMMY
      -----END CERTIFICATE-----
    cert: |
      -----BEGIN CERTIFICATE-----
      DUMMY
      -----END CERTIFICATE-----
    key: |
      -----BEGIN EC PRIVATE KEY-----
      DUMMY
      -----END EC PRIVATE KEY-----
sites:
  gravhl:
    allow:
      build: ""
      staging-build: "OPT1,OPT2"
  tlaloc:
    allow:
      build: ""
"#;

    #[test]
    fn parses_sample() {
        let cfg: AgentConfig = serde_yaml::from_str(SAMPLE).expect("parse");
        cfg.validate().expect("validation");
        assert_eq!(cfg.agent.id, "testid123");
        assert_eq!(cfg.is_allowed("gravhl", "build"), Some(""));
        assert_eq!(cfg.is_allowed("gravhl", "staging-build"), Some("OPT1,OPT2"));
        assert_eq!(cfg.is_allowed("tlaloc", "staging-build"), None);
        assert_eq!(cfg.is_allowed("nope", "build"), None);
    }

    #[test]
    fn rejects_missing_id() {
        let bad = SAMPLE.replace("id: testid123", "id: \"\"");
        let cfg: AgentConfig = serde_yaml::from_str(&bad).expect("parse");
        let e = cfg.validate().unwrap_err();
        assert!(matches!(e, ConfigError::Validation(_)));
    }
}
