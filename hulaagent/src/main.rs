//! hulaagent — mTLS sidecar for hula.
//!
//! Phase 4 step 1 (this commit): tokio runtime + unix-socket accept
//! loop + HLAP banner emission + JSON envelope decoding. Verb dispatch
//! is a placeholder that returns `unknown_verb` for every envelope.
//! Step 2 layers in the mTLS client + BUILD verb; step 3 adds
//! multiplex + sessions; suite 12b gates the phase.
//!
//! See HULAAGENT_PLAN.md for the wire spec.

use clap::Parser;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::ExitCode;
use tokio::net::UnixListener;
use tokio::signal::unix::{signal, SignalKind};

mod config;
mod error;
mod hlap;

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

    /// Unix-socket path to listen on for HLAP commands.
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

    // Current-thread tokio runtime: hulaagent's job is to fan a
    // small number of unix-socket clients into one outbound HTTP
    // connection, not to saturate cores. Single-threaded keeps the
    // binary tiny and startup fast.
    let rt = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(r) => r,
        Err(e) => {
            eprintln!("hula-agent: tokio runtime: {}", e);
            return ExitCode::from(1);
        }
    };

    match rt.block_on(run(args, cfg)) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("hula-agent: {}", e);
            ExitCode::from(1)
        }
    }
}

async fn run(args: Args, _cfg: config::AgentConfig) -> std::io::Result<()> {
    // Best-effort cleanup of a stale socket from a previous run. If
    // the path exists and isn't ours, the subsequent bind will fail
    // and we'll exit cleanly — that's the right behaviour (don't
    // overwrite a path we don't own).
    let _ = std::fs::remove_file(&args.socket);

    let listener = UnixListener::bind(&args.socket)?;

    // Mode 0600 — only the running user's processes can connect.
    // Authorisation is enforced server-side at hula, but local
    // confinement gives us defence in depth.
    std::fs::set_permissions(&args.socket, std::fs::Permissions::from_mode(0o600))?;

    eprintln!(
        "hula-agent: HLAP v{} listening on {} (max_inflight={})",
        hlap::HLAP_VERSION,
        args.socket.display(),
        hlap::MAX_INFLIGHT
    );

    let mut sigterm = signal(SignalKind::terminate())?;
    let mut sigint = signal(SignalKind::interrupt())?;

    loop {
        tokio::select! {
            accept = listener.accept() => {
                let (sock, _addr) = accept?;
                tokio::spawn(async move {
                    if let Err(e) = hlap::serve_connection(sock).await {
                        // I/O error on a single connection is logged
                        // and the connection is torn down; other
                        // connections and the accept loop continue.
                        eprintln!("hula-agent: connection error: {}", e);
                    }
                });
            }
            _ = sigterm.recv() => {
                eprintln!("hula-agent: SIGTERM received, shutting down");
                break;
            }
            _ = sigint.recv() => {
                eprintln!("hula-agent: SIGINT received, shutting down");
                break;
            }
        }
    }

    // Cleanup: unlink the socket path on graceful shutdown so the
    // next start has a clean slate. In-flight handler tasks
    // continue until their connections close — step 1 has no
    // long-running verbs so this is fast.
    let _ = std::fs::remove_file(&args.socket);
    Ok(())
}

/// Render the loaded config in a human-readable form for `--dump`.
/// Skips the mTLS PEM blobs (those are secrets and noisy).
fn print_dump(cfg: &config::AgentConfig) {
    println!("agent.id:        {}", cfg.agent.id);
    println!("agent.hula_host: {}", cfg.agent.hula_host);
    println!(
        "agent.mTLS:      ca={} bytes, cert={} bytes, key={} bytes",
        cfg.agent.mtls.ca.len(),
        cfg.agent.mtls.cert.len(),
        cfg.agent.mtls.key.len()
    );
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
