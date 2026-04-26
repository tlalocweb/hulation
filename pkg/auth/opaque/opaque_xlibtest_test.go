package opaque_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bytemare/opaque"
	hulaopaque "github.com/tlalocweb/hulation/pkg/auth/opaque"
)

// TestCrossLibMaskingKey is the wire-compatibility regression for the
// bytemare↔serenity-kit interop fix. It stands up an in-process bytemare
// OPAQUE register server, then drives BOTH the Go-side bytemare client
// (with the opaque-ke compatibility options KSFSalt=16-zero, KSFLength=64)
// and the browser-side @serenity-kit/opaque library (via Node) through
// a register flow with the same password. The resulting masking_keys
// MUST match — they're both HKDF.Expand(randomized_pwd, "MaskingKey", 64)
// where randomized_pwd derives from the OPRF output and Argon2id-stretched
// version of it.
//
// Without the KSFSalt + KSFLength options, this test fails: bytemare's
// defaults (nil salt, 32-byte output) diverge from opaque-ke's (16-zero
// salt, 64-byte output).
func TestCrossLibMaskingKey(t *testing.T) {
	// Skip if Node + serenity-kit aren't available. This test is opt-in.
	const skLib = "/home/ubuntu/work/hulation/web/analytics/node_modules/@serenity-kit/opaque/esm/index.js"
	if _, err := os.Stat(skLib); err != nil {
		t.Skip("@serenity-kit/opaque not available; run `cd web/analytics && pnpm install` to enable")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}

	cfg := opaque.DefaultConfiguration()
	seed := cfg.GenerateOPRFSeed()
	priv, pub := cfg.KeyGen()
	srv, err := hulaopaque.New(seed, priv, pub.Encode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const password = "correct-horse-battery-staple"
	credID := hulaopaque.CredentialID("admin", "admin")

	// In-process register harness.
	mux := http.NewServeMux()
	mux.HandleFunc("/init", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			M1B64 string `json:"m1_b64"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m1, _ := base64.RawURLEncoding.DecodeString(body.M1B64)
		m2, err := srv.RegisterInit(credID, m1)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"m2_b64": base64.RawURLEncoding.EncodeToString(m2),
		})
	})
	mux.HandleFunc("/finish", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			M3B64 string `json:"m3_b64"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m3, _ := base64.RawURLEncoding.DecodeString(body.M3B64)
		rec, err := srv.RegisterFinish(m3)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// rec encoding: client_pub(32) || masking_key(64) || envelope(96)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"masking_key_hex": hex.EncodeToString(rec[32:96]),
		})
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go (&http.Server{Handler: mux}).Serve(ln)
	defer ln.Close()
	addr := "http://" + ln.Addr().String()
	time.Sleep(50 * time.Millisecond)

	// Go-side (bytemare with opaque-ke options) register.
	cli, _ := cfg.Client()
	opts := &opaque.ClientOptions{KSFSalt: make([]byte, 16), KSFLength: 64}
	regReq, _ := cli.RegistrationInit([]byte(password))
	m2Bytes, _ := srv.RegisterInit(credID, regReq.Serialize())
	d, _ := cfg.Deserializer()
	m2, _ := d.RegistrationResponse(m2Bytes)
	rec, _, err := cli.RegistrationFinalize(m2, []byte("admin"), []byte(hulaopaque.ServerIdentity), opts)
	if err != nil {
		t.Fatalf("RegistrationFinalize: %v", err)
	}
	goMaskingKey := hex.EncodeToString(rec.MaskingKey)

	// Node-side (serenity-kit) register against the same in-process server.
	nodeScript := fmt.Sprintf(`
const SK = '%s';
const opaque = await import(SK);
await opaque.ready;
const PASS = '%s';
const ADDR = '%s';
const r1 = opaque.client.startRegistration({ password: PASS });
async function post(p, b) {
  const r = await fetch(ADDR+p, {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(b)});
  return r.json();
}
const init = await post('/init', { m1_b64: r1.registrationRequest });
const r3 = opaque.client.finishRegistration({
  password: PASS,
  registrationResponse: init.m2_b64,
  clientRegistrationState: r1.clientRegistrationState,
  identifiers: { server: 'hula', client: 'admin' }
});
const fin = await post('/finish', { m3_b64: r3.registrationRecord });
console.log(fin.masking_key_hex);
`, skLib, password, addr)

	out, err := exec.Command("node", "--input-type=module", "-e", nodeScript).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	skMaskingKey := strings.TrimSpace(string(out))

	if goMaskingKey != skMaskingKey {
		t.Errorf("masking_keys differ — bytemare↔serenity-kit OPAQUE wire incompatibility\n  go: %s\n  sk: %s",
			goMaskingKey, skMaskingKey)
	}
}
