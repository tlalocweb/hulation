package main

// `hula` key/secret subcommands. These run BEFORE the server boots (dispatched
// from main) and exit — they never start the listener. Each generates a fresh
// secret and either prints it to stdout (no -c) or surgically updates the named
// field in the given config file (-c), preserving all comments/formatting.
//
// These were moved here from hulactl so the generators live in one place (the
// server binary that ships in the container).

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	hulaopaque "github.com/tlalocweb/hulation/pkg/auth/opaque"
	"github.com/tlalocweb/hulation/pkg/keytool"
	"github.com/tlalocweb/hulation/utils"
)

// dispatchKeyCommand handles the key/secret subcommands. It returns false (so
// main falls through to the normal server path) when args[0] isn't one of them.
// Handlers exit the process directly.
func dispatchKeyCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "totp-key-update":
		runScalarKeyCmd(args[1:], "totp-key-update", []string{"totp_encryption_key"}, utils.GenerateTOTPEncryptionKey)
	case "jwt-key-update":
		runScalarKeyCmd(args[1:], "jwt-key-update", []string{"jwt_key"}, func() (string, error) {
			return utils.GenerateBase64RandomString(32)
		})
	case "noise-static-key-update":
		runKeypairCmd(args[1:], "noise-static-key-update", "noise_static_key")
	case "visitor-chat-key-update":
		runKeypairCmd(args[1:], "visitor-chat-key-update", "visitor_chat_key")
	case "opaque-seed-update":
		runOpaqueSeedCmd(args[1:])
	case "genteamcerts":
		runGenTeamCertsCmd(args[1:])
	default:
		return false
	}
	return true
}

func keyFatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// reportUpdate prints a uniform "updated X in FILE (backup: …)" line to stderr,
// keeping stdout clean for any value (e.g. a public key) the caller emits.
func reportUpdate(cmd, field, file, backup string) {
	fmt.Fprintf(os.Stderr, "%s: updated %s in %s (backup: %s)\n", cmd, field, file, backup)
}

// runScalarKeyCmd: single generated value → stdout, or one field in -c.
func runScalarKeyCmd(args []string, name string, keyPath []string, gen func() (string, error)) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("c", "", "config file to update in place (default: print the new value to stdout)")
	force := fs.Bool("force", false, "overwrite an existing value (rotations are destructive)")
	_ = fs.Parse(args)

	val, err := gen()
	if err != nil {
		keyFatalf("%s: generate: %v", name, err)
	}
	if *cfgPath == "" {
		fmt.Println(val)
		return
	}
	bak, err := keytool.SetScalarsInPlace(*cfgPath,
		[]keytool.ScalarEdit{{KeyPath: keyPath, Value: val}}, *force, time.Now().Unix())
	if err != nil {
		keyFatalf("%s: %v", name, err)
	}
	reportUpdate(name, strings.Join(keyPath, "."), *cfgPath, bak)
}

// runKeypairCmd: X25519 keypair. The private half is the config value; the
// public is always printed (operators pin it on mobile clients).
func runKeypairCmd(args []string, name, field string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("c", "", "config file to update in place (default: print the new keypair to stdout)")
	force := fs.Bool("force", false, "overwrite an existing value (clients that pinned the old public must re-fetch)")
	_ = fs.Parse(args)

	priv, pub, err := generateX25519KeyPair()
	if err != nil {
		keyFatalf("%s: generate: %v", name, err)
	}
	if *cfgPath == "" {
		fmt.Printf("private: %s\npublic:  %s\n", priv, pub)
		return
	}
	bak, err := keytool.SetScalarsInPlace(*cfgPath,
		[]keytool.ScalarEdit{{KeyPath: []string{field}, Value: priv}}, *force, time.Now().Unix())
	if err != nil {
		keyFatalf("%s: %v", name, err)
	}
	reportUpdate(name, field, *cfgPath, bak)
	fmt.Printf("public: %s\n", pub) // pin this on clients
}

// runOpaqueSeedCmd: writes BOTH opaque.oprf_seed and opaque.ake_secret.
func runOpaqueSeedCmd(args []string) {
	const name = "opaque-seed-update"
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("c", "", "config file to update in place (default: print the seeds to stdout)")
	force := fs.Bool("force", false, "overwrite existing values (invalidates ALL existing OPAQUE records)")
	_ = fs.Parse(args)

	seed := hulaopaque.GenerateSeedB64()
	ake, err := hulaopaque.GenerateAKESecretB64()
	if err != nil {
		keyFatalf("%s: generate ake secret: %v", name, err)
	}
	if *cfgPath == "" {
		fmt.Printf("oprf_seed:  %s\nake_secret: %s\n", seed, ake)
		return
	}
	bak, err := keytool.SetScalarsInPlace(*cfgPath, []keytool.ScalarEdit{
		{KeyPath: []string{"opaque", "oprf_seed"}, Value: seed},
		{KeyPath: []string{"opaque", "ake_secret"}, Value: ake},
	}, *force, time.Now().Unix())
	if err != nil {
		keyFatalf("%s: %v", name, err)
	}
	reportUpdate(name, "opaque.oprf_seed + opaque.ake_secret", *cfgPath, bak)
	fmt.Fprintln(os.Stderr, "WARNING: this invalidates every existing OPAQUE record — all passwords must be re-set.")
}

// runGenTeamCertsCmd mirrors the old hulactl genteamcerts (writes a bundle
// directory; no -c, since it doesn't map to a single config field).
func runGenTeamCertsCmd(args []string) {
	fs := flag.NewFlagSet("genteamcerts", flag.ExitOnError)
	nodes := fs.String("nodes", "", "comma-separated node ids (required)")
	teamID := fs.String("team-id", "", "team UUID (auto-generated if empty)")
	validity := fs.String("validity", "8760h", "per-cert validity (Go duration)")
	out := fs.String("out", "./team-bundles", "output directory for the bundle")
	_ = fs.Parse(args)

	ids := splitCSV(*nodes)
	if len(ids) == 0 {
		keyFatalf("genteamcerts: --nodes is required (comma-separated node ids)")
	}
	res, err := keytool.GenTeamCerts(ids, *teamID, *validity, *out)
	if err != nil {
		keyFatalf("genteamcerts: %v", err)
	}
	fmt.Printf("Team CA + per-node bundle written to %s\n\n", res.OutDir)
	fmt.Printf("  team_id:        %s\n", res.TeamID)
	fmt.Printf("  validity:       %s\n", res.Validity)
	fmt.Printf("  nodes:          %s\n", strings.Join(res.Nodes, ", "))
	fmt.Printf("  bootstrap_token (base64): %s\n\n", res.BootstrapB64)
	fmt.Printf("Operator next steps:\n")
	fmt.Printf("  1. Move %s/ca.key into your secrets vault. NEVER deploy it to a node.\n", res.OutDir)
	fmt.Printf("  2. Distribute %s/<node-id>/ to the matching node (cert.pem, key.pem, ca.pem).\n", res.OutDir)
	fmt.Printf("  3. Set HULA_TEAM_BOOTSTRAP_TOKEN=%s on every node before first boot.\n", res.BootstrapB64)
	fmt.Printf("  4. Configure team.team_id, team.node_id, team.pki.{ca_cert,node_cert,node_key} on each node.\n")
}

// generateX25519KeyPair mints a fresh 32-byte X25519 private key (the config
// wire format used by noise_static_key / visitor_chat_key) and derives its
// public via curve25519 — the same derivation server/installation_identity.go
// runs at request time. Both halves are base64url-no-pad.
func generateX25519KeyPair() (privB64, pubB64 string, err error) {
	privB64, err = utils.GenerateNoiseStaticKey()
	if err != nil {
		return "", "", err
	}
	privBytes, err := utils.DecodeNoiseStaticKey(privB64)
	if err != nil {
		return "", "", fmt.Errorf("generated key does not round-trip: %w", err)
	}
	pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}
	pubB64 = base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(pubBytes)
	return privB64, pubB64, nil
}

// keyCommandsHelpText is appended to `hula -h` so the key/secret subcommands
// are discoverable alongside the server flags.
const keyCommandsHelpText = `
Key / secret subcommands (print the new value to stdout, or update a config
file in place with -c, comment-preserving; --force overwrites an existing value):
  jwt-key-update             generate/rotate jwt_key
  totp-key-update            generate/rotate totp_encryption_key
  noise-static-key-update    generate/rotate noise_static_key (also prints the public)
  visitor-chat-key-update    generate/rotate visitor_chat_key (also prints the public)
  opaque-seed-update         generate/rotate opaque.oprf_seed + opaque.ake_secret
  genteamcerts               Team CA + per-node mTLS bundles + bootstrap token

Run "<subcommand> -h" for a subcommand's own options
(e.g. "hula totp-key-update -h").
`

// installKeyCommandHelp augments the default flag usage so `hula -h` / `--help`
// shows the key/secret subcommands in addition to the server flags. Call once,
// before app.ParseFlags() (which triggers flag.Parse → flag.Usage on -h).
func installKeyCommandHelp() {
	base := os.Args[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintf(w, "Usage:\n")
		fmt.Fprintf(w, "  %s [flags]                 start the hula server\n", base)
		fmt.Fprintf(w, "  %s <subcommand> [options]  generate/rotate a secret (see below)\n\n", base)
		fmt.Fprintf(w, "Server flags:\n")
		flag.PrintDefaults()
		fmt.Fprint(w, keyCommandsHelpText)
	}
}

func splitCSV(s string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
