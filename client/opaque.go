package client

// OPAQUE register + login client for hulactl. Drives the
// /api/v1/auth/opaque/* RPCs added in stage 3.
//
// Same bytemare/opaque library the server uses — interop is by
// construction. base64url unpadded everywhere (matches the wire
// format the browser side speaks via @serenity-kit; OPAQUE_PLAN §18).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/bytemare/opaque"

	hulaopaque "github.com/tlalocweb/hulation/pkg/auth/opaque"
)

// b64encU returns raw base64url for OPAQUE wire bytes.
func b64encU(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// b64decU permissive decoder — accepts any base64 alphabet/padding.
func b64decU(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("not valid base64")
}

func (c *Client) postJSON(path string, body any, out any) (httpStatus int, err error) {
	url := c.apiUrl + path
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return res.StatusCode, fmt.Errorf("HTTP %d: %s", res.StatusCode, string(rb))
	}
	if out != nil {
		if err := json.Unmarshal(rb, out); err != nil {
			return res.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return res.StatusCode, nil
}

// --- Registration -------------------------------------------------

type opaqueRegInitReq struct {
	Username string `json:"username"`
	Provider string `json:"provider"`
	M1B64    string `json:"m1_b64"`
}
type opaqueRegInitResp struct {
	M2B64 string `json:"m2_b64"`
}
type opaqueRegFinishReq struct {
	Username string `json:"username"`
	Provider string `json:"provider"`
	M3B64    string `json:"m3_b64"`
}
type opaqueRegFinishResp struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// OpaqueRegister sets a password for a user via the OPAQUE
// registration round-trip. The server stores the resulting envelope
// keyed on (provider, username); the password itself never leaves
// this process.
//
// provider: "admin" | "internal"
func (c *Client) OpaqueRegister(provider, username, password string) error {
	cfg := opaque.DefaultConfiguration()
	cli, err := cfg.Client()
	if err != nil {
		return fmt.Errorf("opaque client: %w", err)
	}
	regReq, err := cli.RegistrationInit([]byte(password))
	if err != nil {
		return fmt.Errorf("RegistrationInit: %w", err)
	}

	var initResp opaqueRegInitResp
	if _, err := c.postJSON("/api/v1/auth/opaque/register/init",
		opaqueRegInitReq{
			Username: username, Provider: provider,
			M1B64: b64encU(regReq.Serialize()),
		}, &initResp); err != nil {
		return fmt.Errorf("register init RPC: %w", err)
	}
	m2Bytes, err := b64decU(initResp.M2B64)
	if err != nil {
		return fmt.Errorf("decode M2: %w", err)
	}

	d, _ := cfg.Deserializer()
	m2, err := d.RegistrationResponse(m2Bytes)
	if err != nil {
		return fmt.Errorf("deserialize M2: %w", err)
	}
	rec, _, err := cli.RegistrationFinalize(m2, []byte(username), []byte(hulaopaque.ServerIdentity))
	if err != nil {
		return fmt.Errorf("RegistrationFinalize: %w", err)
	}

	var finResp opaqueRegFinishResp
	if _, err := c.postJSON("/api/v1/auth/opaque/register/finish",
		opaqueRegFinishReq{
			Username: username, Provider: provider,
			M3B64: b64encU(rec.Serialize()),
		}, &finResp); err != nil {
		return fmt.Errorf("register finish RPC: %w", err)
	}
	if !finResp.Ok {
		return fmt.Errorf("server reported failure: %s", finResp.Error)
	}
	return nil
}

// --- Login --------------------------------------------------------

type opaqueLoginInitReq struct {
	Username string `json:"username"`
	Provider string `json:"provider"`
	Ke1B64   string `json:"ke1_b64"`
}
type opaqueLoginInitResp struct {
	Ke2B64          string `json:"ke2_b64"`
	SessionId       string `json:"session_id"`
	LegacyAvailable bool   `json:"legacy_available"`
}
type opaqueLoginFinishReq struct {
	SessionId string `json:"session_id"`
	Ke3B64    string `json:"ke3_b64"`
}
type opaqueLoginFinishResp struct {
	Admintoken   string `json:"admintoken"`
	Token        string `json:"token"`
	TotpRequired bool   `json:"totp_required"`
	Error        string `json:"error,omitempty"`
}

// OpaqueLoginResult mirrors the legacy AuthResponse field names so
// the caller in main.go can switch flows transparently.
type OpaqueLoginResult struct {
	JWT             string
	TotpRequired    bool
	LegacyAvailable bool
}

// OpaqueLogin attempts an OPAQUE login. On legacy_available=true,
// returns Result{LegacyAvailable: true} so the caller can fall
// back to the legacy /api/auth/login flow.
func (c *Client) OpaqueLogin(provider, username, password string) (*OpaqueLoginResult, error) {
	cfg := opaque.DefaultConfiguration()
	cli, err := cfg.Client()
	if err != nil {
		return nil, fmt.Errorf("opaque client: %w", err)
	}
	ke1, err := cli.GenerateKE1([]byte(password))
	if err != nil {
		return nil, fmt.Errorf("GenerateKE1: %w", err)
	}

	var initResp opaqueLoginInitResp
	if _, err := c.postJSON("/api/v1/auth/opaque/login/init",
		opaqueLoginInitReq{
			Username: username, Provider: provider,
			Ke1B64: b64encU(ke1.Serialize()),
		}, &initResp); err != nil {
		return nil, fmt.Errorf("login init RPC: %w", err)
	}
	if initResp.LegacyAvailable && initResp.Ke2B64 == "" {
		return &OpaqueLoginResult{LegacyAvailable: true}, nil
	}
	if initResp.Ke2B64 == "" {
		return nil, fmt.Errorf("server returned no KE2 and no legacy fallback")
	}
	ke2Bytes, err := b64decU(initResp.Ke2B64)
	if err != nil {
		return nil, fmt.Errorf("decode KE2: %w", err)
	}
	d, _ := cfg.Deserializer()
	ke2, err := d.KE2(ke2Bytes)
	if err != nil {
		return nil, fmt.Errorf("deserialize KE2: %w", err)
	}
	ke3, _, _, err := cli.GenerateKE3(ke2, []byte(username), []byte(hulaopaque.ServerIdentity))
	if err != nil {
		return nil, fmt.Errorf("GenerateKE3: %w", err)
	}

	var finResp opaqueLoginFinishResp
	if _, err := c.postJSON("/api/v1/auth/opaque/login/finish",
		opaqueLoginFinishReq{
			SessionId: initResp.SessionId,
			Ke3B64:    b64encU(ke3.Serialize()),
		}, &finResp); err != nil {
		return nil, fmt.Errorf("login finish RPC: %w", err)
	}
	if finResp.Error != "" {
		return nil, fmt.Errorf("%s", finResp.Error)
	}
	jwt := finResp.Admintoken
	if jwt == "" {
		jwt = finResp.Token
	}
	return &OpaqueLoginResult{
		JWT:          jwt,
		TotpRequired: finResp.TotpRequired,
	}, nil
}
