//! hulaagent — mTLS sidecar for hula.
//!
//! Phase 1 scope (this commit): arg-parse + config-load + dump the
//! resolved permissions. Subsequent phases wire the unix-socket HLAP
//! server, the mTLS client to hula, and per-verb dispatch. See
//! HULAAGENT_PLAN.md for the staged rollout.

use clap::Parser;
use std::path::PathBuf;
use std::process::ExitCode;

mod config;

#[derive(Parser, Debug)]
#[command(
    name = "hula-agent",
    about = "Lightweight mTLS sidecar for hula.",
    long_about = "Reads an agent config produced by `hulactl create-agent`, opens a unix-socket, and forwards permitted commands to hula via mTLS. See HULAAGENT_PLAN.md."
)]
struct Args {
    /// Path to the agent config yaml.
    #[arg(short = 'c', long = "config")]
    config: PathBuf,

    /// Unix-socket path to listen on for HLAP commands. (Phase 4+;
    /// ignored in Phase 1.)
    #[arg(long, default_value = "/tmp/hulaagent.sock")]
    socket: PathBuf,

    /// Print the resolved config + allow map and exit. Useful for
    /// verifying a freshly-produced agent yaml without going through
    /// the full HLAP loop.
    #[arg(long)]
    dump: bool,
}

fn main() -> ExitCode {
    let args = Args::parse();

    let cfg = match config::AgentConfig::load(&args.config) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("hula-agent: {}", e);
            return ExitCode::from(2);
        }
    };

    if args.dump {
        print_dump(&cfg);
        return ExitCode::SUCCESS;
    }

    // Phase 1 stops here. Phase 4 wires the unix-socket HLAP server
    // and the mTLS client to hula. Until then, refuse to "run" so an
    // operator who tries `hula-agent -c foo.yaml` against an old
    // build sees a clear "not yet" error instead of a silent no-op.
    eprintln!(
        "hula-agent: HLAP server not implemented yet (Phase 4). \
         Use --dump to verify the config is parseable."
    );
    ExitCode::from(64)
}

/// Render the loaded config in a human-readable form for `--dump`.
/// Skips the mTLS PEM blobs (those are secrets and noisy).
fn print_dump(cfg: &config::AgentConfig) {
    println!("agent.id:        {}", cfg.agent.id);
    println!("agent.hula_host: {}", cfg.agent.hula_host);
    println!("agent.mTLS:      ca={} bytes, cert={} bytes, key={} bytes",
        cfg.agent.mtls.ca.len(), cfg.agent.mtls.cert.len(), cfg.agent.mtls.key.len());
    println!();
    println!("sites:");
    for (site, allow) in &cfg.sites {
        println!("  {}:", site);
        for (verb, opts) in &allow.allow {
            if opts.is_empty() {
                println!("    allow.{}", verb);
            } else {
                println!("    allow.{}: {}", verb, opts);
            }
        }
    }
}
