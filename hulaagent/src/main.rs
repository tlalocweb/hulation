//! hulaagent — mTLS sidecar for hula.
//!
//! Tokio current-thread runtime + unix-socket accept loop + HLAP
//! banner emission + per-verb dispatch over the mTLS HTTP client.
//! The BUILD verb emits an initial OK envelope with `streaming:true`,
//! streams `log` envelopes for each line of hula's build output, and
//! ends with a terminal `done:true` envelope carrying the final
//! status. Other verbs follow the same multi-envelope shape where
//! applicable.
//!
//! See HULAAGENT_PLAN.md for the wire spec.

use clap::Parser;
use std::net::SocketAddr;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::ExitCode;
use std::sync::Arc;
use tokio::net::UnixListener;
use tokio::signal::unix::{signal, SignalKind};

mod client;
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

    /// Override DNS lookups for a host. Repeatable. Format:
    /// `HOST=IP:PORT` — e.g. `hula.example.com=10.0.0.5:443`. The
    /// host's SNI and Host header continue to use the canonical
    /// name from `hula_host`; only the socket-layer connect target
    /// changes. Mirrors curl's --resolve.
    #[arg(long = "resolve", value_name = "HOST=IP:PORT")]
    resolves: Vec<String>,

    /// Extra CA certificate (PEM) to trust for hula's serving cert
    /// verification. Repeatable. Useful behind private CAs or in
    /// e2e environments; production deployments with publicly-
    /// trusted serving certs don't need this.
    #[arg(long = "extra-ca", value_name = "PATH")]
    extra_ca: Vec<PathBuf>,
}

/// Parse a `--resolve HOST=IP:PORT` argument into the
/// (host, SocketAddr) pair reqwest expects.
fn parse_resolve(raw: &str) -> Result<(String, SocketAddr), String> {
    let (host, addr) = raw
        .split_once('=')
        .ok_or_else(|| format!("expected HOST=IP:PORT, got {:?}", raw))?;
    let host = host.trim();
    if host.is_empty() {
        return Err(format!("empty host in --resolve {:?}", raw));
    }
    let sock: SocketAddr = addr
        .trim()
        .parse()
        .map_err(|e| format!("invalid IP:PORT {:?}: {}", addr, e))?;
    Ok((host.to_string(), sock))
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

    // Parse --resolve overrides up-front so a bad value surfaces
    // before we touch any cryptographic material.
    let mut overrides = client::ClientOverrides::default();
    for raw in &args.resolves {
        match parse_resolve(raw) {
            Ok(entry) => overrides.resolves.push(entry),
            Err(e) => {
                eprintln!("hula-agent: --resolve: {}", e);
                return ExitCode::from(2);
            }
        }
    }
    overrides.extra_ca_paths = args.extra_ca.clone();

    // Build the mTLS HTTP client at startup so failures (malformed
    // PEMs, unreadable CA, etc.) surface as a clean exit-2 instead of
    // a per-request error on first BUILD. Successful construction is
    // also a useful smoke signal: the agent can't talk to hula yet
    // but the cryptographic material parsed.
    let hula_client = match client::HulaClient::new(&cfg, &overrides) {
        Ok(c) => Arc::new(c),
        Err(e) => {
            eprintln!("hula-agent: {}", e);
            return ExitCode::from(2);
        }
    };

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

    match rt.block_on(run(args, hula_client)) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("hula-agent: {}", e);
            ExitCode::from(1)
        }
    }
}

async fn run(args: Args, hula_client: Arc<client::HulaClient>) -> std::io::Result<()> {
    // Best-effort cleanup of a stale socket from a previous run. If
    // the path exists and isn't ours, the subsequent bind will fail
    // and we'll exit cleanly — that's the right behaviour (don't
    // overwrite a path we don't own).
    let _ = std::fs::remove_file(&args.socket);

    // Tighten umask before bind() so the kernel creates the socket
    // inode with mode 0600 from the start. A post-bind chmod alone
    // leaves a microsecond window where a same-UID process could
    // connect; this closes it. The post-bind chmod stays as a
    // belt-and-braces in case the process umask was something
    // unusual before this restore.
    // SAFETY: umask is async-signal-safe and process-global; we hold
    // the value across a single sync syscall and restore it before
    // any further work.
    let prev_umask = unsafe { libc::umask(0o177) };
    let listener_result = UnixListener::bind(&args.socket);
    unsafe { libc::umask(prev_umask) };
    let listener = listener_result?;

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
                let client_arc = Arc::clone(&hula_client);
                tokio::spawn(async move {
                    if let Err(e) = hlap::serve_connection(sock, client_arc).await {
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
    // continue until their connections close.
    let _ = std::fs::remove_file(&args.socket);
    Ok(())
}

/// Render the loaded config in a human-readable form for `--dump`.
/// Skips the mTLS PEM blobs (those are secrets and noisy).
fn print_dump(cfg: &config::AgentConfig) {
    println!("agent.id:        {}", cfg.agent.id);
    println!("agent.hula_host: {}", cfg.agent.hula_host);
    println!("agent.mTLS:      [configured]");
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
