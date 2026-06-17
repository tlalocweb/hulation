package main

// relay-enroll — bind this hula install to a hula-push-relay.
//
// The relay (hula-push-relay) authenticates an enrollment with a one-time
// code, not a signature, and stores ONLY the installation's ed25519 public
// key — it never receives the private half. So this command generates the
// keypair locally, sends the public key + code to
// POST <relay>/v1/installations/enroll, and surfaces the installation_id the
// relay assigns plus the 32-byte signing seed (which becomes hula's
// push_relay.signing_key_b64). The seed is printed exactly once.
//
// Wire types mirror hula-push-relay's crates/relay/src/wire/enroll.rs. The
// relay decodes installation_public_key as STANDARD base64, so we encode the
// public key that way; the seed we emit matches what server/run_unified.go's
// buildPushRelayClient accepts (any base64 flavour, 32-byte ed25519 seed).

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/utils"
	"gopkg.in/yaml.v3"
)

// relayEnrollRequest mirrors wire::enroll::EnrollRequest.
type relayEnrollRequest struct {
	EnrollmentCode        string `json:"enrollment_code"`
	InstallationPublicKey string `json:"installation_public_key"`
	HulaVersion           string `json:"hula_version"`
	DisplayName           string `json:"display_name,omitempty"`
}

// relayEnrollResponse mirrors wire::enroll::EnrollResponse.
type relayEnrollResponse struct {
	InstallationID  string `json:"installation_id"`
	ServerAccountID string `json:"server_account_id"`
	RelayAPIBase    string `json:"relay_api_base"`
	Status          string `json:"status"`
}

func runRelayEnroll(cfg *HulactlConfig, argz []string) {
	if len(argz) < 2 {
		fmt.Println(getCommandUsage(CMD_RELAYENROLL))
		os.Exit(1)
	}
	base := strings.TrimRight(strings.TrimSpace(argz[1]), "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}

	// Code from the --code flag (which, per hulactl convention, must precede
	// the command) or the positional argument after the URL.
	code, err := resolveEnrollCode(cfg.RelayCode, argz)
	if err != nil {
		fmt.Printf("relay-enroll: %s\n", err.Error())
		fmt.Println("Use either:")
		fmt.Println("  hulactl relay-enroll <relay-base-url> <enrollment-code>")
		fmt.Println("  hulactl --code <enrollment-code> relay-enroll <relay-base-url>   (flags before the command)")
		os.Exit(1)
	}

	// 1. Generate the installation's ed25519 keypair. The relay stores only
	//    the public half; priv.Seed() (32 bytes) becomes signing_key_b64.
	pub, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		fmt.Printf("relay-enroll: generate keypair: %s\n", err.Error())
		os.Exit(1)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)          // relay decodes STANDARD base64
	seedB64 := base64.StdEncoding.EncodeToString(priv.Seed()) // 32-byte seed → signing_key_b64

	hulaVersion := strings.TrimSpace(config.Version)
	if hulaVersion == "" {
		hulaVersion = "hulactl"
	}
	reqBody, err := json.Marshal(relayEnrollRequest{
		EnrollmentCode:        code,
		InstallationPublicKey: pubB64,
		HulaVersion:           hulaVersion,
		DisplayName:           strings.TrimSpace(cfg.RelayLabel),
	})
	if err != nil {
		fmt.Printf("relay-enroll: marshal request: %s\n", err.Error())
		os.Exit(1)
	}

	// 2. Redeem the code at the relay.
	url := base + "/v1/installations/enroll"
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg.RelayInsecure {
		// Clone the default transport so we keep ProxyFromEnvironment,
		// connection pooling, and HTTP/2 — only relax cert verification.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // operator opt-in via --insecure for dev relays
		httpClient.Transport = tr
	}
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Printf("relay-enroll: build request: %s\n", err.Error())
		os.Exit(1)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		fmt.Printf("relay-enroll: POST %s: %s\n", url, err.Error())
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("relay-enroll: relay returned HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
		os.Exit(1)
	}

	var er relayEnrollResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		fmt.Printf("relay-enroll: decode response: %s (body: %s)\n", err.Error(), strings.TrimSpace(string(respBody)))
		os.Exit(1)
	}
	if strings.TrimSpace(er.InstallationID) == "" {
		fmt.Printf("relay-enroll: relay response missing installation_id (body: %s)\n", strings.TrimSpace(string(respBody)))
		os.Exit(1)
	}

	// 3a. --write: persist the push_relay block into the hula config file.
	if cfg.RelayWrite {
		path := strings.TrimSpace(cfg.HulationConfigPath)
		if path == "" {
			fmt.Println("relay-enroll: --write needs -hulaconf <path> to the hula config file")
			os.Exit(1)
		}
		if err := writePushRelayBlock(path, base, er.InstallationID, seedB64); err != nil {
			fmt.Printf("relay-enroll: %s\n", err.Error())
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "relay-enroll: wrote push_relay block to %s (installation_id=%s, status=%s)\n",
			path, er.InstallationID, er.Status)
		return
	}

	// 3b. Default: print the block for the operator to paste. The signing
	//     seed is shown exactly once — the relay never holds it.
	fmt.Fprintf(os.Stderr, "relay-enroll: enrolled OK (installation_id=%s, account=%s, status=%s)\n",
		er.InstallationID, er.ServerAccountID, er.Status)
	fmt.Println("# --- add to hula's config.yaml (signing_key_b64 shown once — store it securely) ---")
	fmt.Println("push_relay:")
	fmt.Printf("    base_url: %q\n", base)
	fmt.Printf("    installation_id: %q\n", er.InstallationID)
	fmt.Printf("    signing_key_b64: %q\n", seedB64)
}

// resolveEnrollCode picks the enrollment code from the --code flag (which, per
// hulactl convention, must precede the command) or the positional argument
// after the URL (argz[2]). A value starting with "-" is almost always a flag
// mistakenly placed after the command — which Go's flag package leaves
// unparsed — so reject it rather than send "--code" as the code.
func resolveEnrollCode(flagCode string, argz []string) (string, error) {
	code := strings.TrimSpace(flagCode)
	if code == "" && len(argz) >= 3 {
		code = strings.TrimSpace(argz[2])
	}
	if code == "" {
		return "", fmt.Errorf("missing enrollment code")
	}
	if strings.HasPrefix(code, "-") {
		return "", fmt.Errorf("%q looks like a flag, not an enrollment code (flags go before the command)", code)
	}
	return code, nil
}

// writePushRelayBlock writes push_relay.{base_url,installation_id,signing_key_b64}
// into the hula config at path, preserving the file's original permission bits.
// ModifyYamlFile rewrites the file at mode 0644; since the file now holds the
// ed25519 signing seed (and likely other secrets), restore the original mode —
// defaulting to 0600 when it can't be determined — so the write never widens
// access.
func writePushRelayBlock(path, base, installationID, seedB64 string) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	writes := []struct {
		keys []string
		val  string
	}{
		{[]string{"push_relay", "base_url"}, base},
		{[]string{"push_relay", "installation_id"}, installationID},
		{[]string{"push_relay", "signing_key_b64"}, seedB64},
	}
	for _, w := range writes {
		if err := utils.ModifyYamlFile(path, w.keys, &yaml.Node{Kind: yaml.ScalarNode, Value: w.val}); err != nil {
			return fmt.Errorf("write %s: %w", strings.Join(w.keys, "."), err)
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("restore mode on %s: %w", path, err)
	}
	return nil
}
