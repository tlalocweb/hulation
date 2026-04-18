package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/log"
)

const (
	cloudflareAPIBase = "https://api.cloudflare.com/client/v4"
	// Renew when less than this many days remain
	cfOriginCARenewThresholdDays = 30
)

// CloudflareOriginCAConfig configures automatic origin certificate provisioning via
// the Cloudflare Origin CA API. Hula generates a private key + CSR locally, submits
// the CSR to Cloudflare, and caches the signed certificate on disk.
//
// APIToken and ZoneID can be set explicitly (supports {{env:VAR}} substitution),
// or left empty to auto-resolve from environment variables keyed by the server's id:
//
//	CLOUDFLARE_API_TOKEN_{servers.id}
//	CLOUDFLARE_ZONE_ID_{servers.id}
//
// This allows multiple virtual hosts to use different Cloudflare zones and tokens.
type CloudflareOriginCAConfig struct {
	// Cloudflare API token with "Zone > SSL and Certificates > Edit" permission.
	// If empty, resolved from env var CLOUDFLARE_API_TOKEN_{servers.id}
	APIToken string `yaml:"api_token,omitempty"`
	// Cloudflare zone ID for the domain.
	// If empty, resolved from env var CLOUDFLARE_ZONE_ID_{servers.id}
	ZoneID string `yaml:"zone_id,omitempty"`
	// Directory to cache cert + key files (default: "certs")
	CacheDir string `yaml:"cache_dir,omitempty" default:"certs"`
	// Key type: "ecdsa" (P-256, default) or "rsa" (2048-bit)
	KeyType string `yaml:"key_type,omitempty" default:"ecdsa"`
	// Requested certificate validity in days: 7, 30, 90, 365, 730, 1095, or 5475 (default: 5475)
	ValidityDays int `yaml:"validity_days,omitempty" default:"5475"`
	// Allow connections from non-Cloudflare IPs (default: false).
	// When false, Hula drops TCP connections from IPs outside the Cloudflare ranges.
	// Set true for debugging or mixed-mode setups.
	AllowNonCFIPs bool `yaml:"allow_non_cf_ips,omitempty"`
}

// ProvisionOrLoadCert loads an existing origin CA cert from the cache directory,
// or provisions a new one from Cloudflare if none exists or the cached cert is expiring.
// The first hostname is used as the filename stem for cache files.
func (c *CloudflareOriginCAConfig) ProvisionOrLoadCert(hostnames []string) (*tls.Certificate, error) {
	if len(hostnames) == 0 {
		return nil, fmt.Errorf("cloudflare_origin_ca: no hostnames provided")
	}
	if c.APIToken == "" {
		return nil, fmt.Errorf("cloudflare_origin_ca: api_token is required")
	}
	if c.ZoneID == "" {
		return nil, fmt.Errorf("cloudflare_origin_ca: zone_id is required")
	}
	log.Infof("cloudflare_origin_ca: zone_id=%s...%s token=%s...%s hostnames=%v key_type=%s validity=%d",
		c.ZoneID[:min(6, len(c.ZoneID))], c.ZoneID[max(0, len(c.ZoneID)-4):],
		c.APIToken[:min(8, len(c.APIToken))], c.APIToken[max(0, len(c.APIToken)-4):],
		hostnames, c.KeyType, c.ValidityDays)

	// Resolve cache dir with confdir substitution
	cacheDir := c.CacheDir
	if cacheDir == "" {
		cacheDir = "certs"
	}
	cacheDir, _ = SubstConfVars(cacheDir, map[string]string{"confdir": confDir})

	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: failed to create cache dir %s: %w", cacheDir, err)
	}

	stem := hostnames[0]
	certPath := filepath.Join(cacheDir, stem+".crt")
	keyPath := filepath.Join(cacheDir, stem+".key")
	metaPath := filepath.Join(cacheDir, stem+".meta.json")

	// Try loading cached cert
	cert, err := c.loadCachedCert(certPath, keyPath)
	if err == nil {
		// Check expiry
		leaf, parseErr := x509.ParseCertificate(cert.Certificate[0])
		if parseErr == nil {
			daysLeft := time.Until(leaf.NotAfter).Hours() / 24
			if daysLeft > cfOriginCARenewThresholdDays {
				log.Infof("cloudflare_origin_ca: using cached cert for %s (expires %s, %.0f days left)",
					stem, leaf.NotAfter.Format("2006-01-02"), daysLeft)
				return cert, nil
			}
			log.Infof("cloudflare_origin_ca: cached cert for %s expires in %.0f days, renewing", stem, daysLeft)
		}
	} else {
		log.Infof("cloudflare_origin_ca: no cached cert for %s (%v), provisioning new cert", stem, err)
	}

	// Provision a new cert
	return c.provisionNewCert(hostnames, certPath, keyPath, metaPath)
}

// loadCachedCert tries to load a cert+key pair from disk.
func (c *CloudflareOriginCAConfig) loadCachedCert(certPath, keyPath string) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cached cert/key: %w", err)
	}
	return &cert, nil
}

// provisionNewCert generates a key+CSR, calls the Cloudflare Origin CA API, and caches the result.
func (c *CloudflareOriginCAConfig) provisionNewCert(hostnames []string, certPath, keyPath, metaPath string) (*tls.Certificate, error) {
	// Generate private key
	keyPEM, csrPEM, err := c.generateKeyAndCSR(hostnames)
	if err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: failed to generate key/CSR: %w", err)
	}

	// Call Cloudflare API to create the certificate
	certPEM, certID, err := c.createCertificate(csrPEM, hostnames)
	if err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: API error: %w", err)
	}

	// Write to cache
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: failed to write key to %s: %w", keyPath, err)
	}
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: failed to write cert to %s: %w", certPath, err)
	}

	// Write metadata
	meta := cfOriginCAMeta{
		CertID:    certID,
		Hostnames: hostnames,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		log.Warnf("cloudflare_origin_ca: failed to write meta to %s: %s", metaPath, err)
	}

	// Parse and return
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("cloudflare_origin_ca: failed to parse new cert/key: %w", err)
	}

	log.Infof("cloudflare_origin_ca: provisioned new cert for %v (id: %s)", hostnames, certID)
	return &cert, nil
}

type cfOriginCAMeta struct {
	CertID    string   `json:"cert_id"`
	Hostnames []string `json:"hostnames"`
	CreatedAt string   `json:"created_at"`
}

// generateKeyAndCSR creates a private key and CSR for the given hostnames.
func (c *CloudflareOriginCAConfig) generateKeyAndCSR(hostnames []string) (keyPEM []byte, csrPEM []byte, err error) {
	var privKey interface{}
	var keyDER []byte

	keyType := strings.ToLower(c.KeyType)
	if keyType == "" {
		keyType = "ecdsa"
	}

	switch keyType {
	case "ecdsa":
		key, gerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if gerr != nil {
			return nil, nil, fmt.Errorf("ecdsa key generation: %w", gerr)
		}
		privKey = key
		keyDER, err = x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal ecdsa key: %w", err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	case "rsa":
		key, gerr := rsa.GenerateKey(rand.Reader, 2048)
		if gerr != nil {
			return nil, nil, fmt.Errorf("rsa key generation: %w", gerr)
		}
		privKey = key
		keyDER = x509.MarshalPKCS1PrivateKey(key)
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
	default:
		return nil, nil, fmt.Errorf("unsupported key_type %q (use \"ecdsa\" or \"rsa\")", c.KeyType)
	}

	// Create CSR
	csrTemplate := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostnames[0]},
		DNSNames: hostnames,
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return keyPEM, csrPEM, nil
}

// --- Cloudflare API ---

type cfAPIResponse struct {
	Success  bool            `json:"success"`
	Errors   []cfAPIError    `json:"errors"`
	Result   json.RawMessage `json:"result"`
	Messages []interface{}   `json:"messages"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfCreateCertRequest struct {
	CSR               string   `json:"csr"`
	Hostnames         []string `json:"hostnames"`
	RequestType       string   `json:"request_type"`
	RequestedValidity int      `json:"requested_validity"`
}

type cfCertResult struct {
	ID          string `json:"id"`
	Certificate string `json:"certificate"`
	CSR         string `json:"csr"`
	ExpiresOn   string `json:"expires_on"`
}

// createCertificate calls the Cloudflare Origin CA API to sign the CSR.
func (c *CloudflareOriginCAConfig) createCertificate(csrPEM []byte, hostnames []string) (certPEM []byte, certID string, err error) {
	requestType := "origin-ecc"
	keyType := strings.ToLower(c.KeyType)
	if keyType == "rsa" {
		requestType = "origin-rsa"
	}

	validityDays := c.ValidityDays
	if validityDays == 0 {
		validityDays = 5475
	}

	reqBody := cfCreateCertRequest{
		CSR:               string(csrPEM),
		Hostnames:         hostnames,
		RequestType:       requestType,
		RequestedValidity: validityDays,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	apiURL := cloudflareAPIBase + "/certificates"
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIToken)

	log.Debugf("cloudflare_origin_ca: POST %s hostnames=%v type=%s validity=%d", apiURL, hostnames, requestType, validityDays)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	log.Debugf("cloudflare_origin_ca: response status=%d", resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	var apiResp cfAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, "", fmt.Errorf("parse response: %w (body: %s)", err, string(respBody[:min(len(respBody), 200)]))
	}

	if !apiResp.Success {
		errMsgs := make([]string, len(apiResp.Errors))
		for i, e := range apiResp.Errors {
			errMsgs[i] = fmt.Sprintf("[%d] %s", e.Code, e.Message)
		}
		return nil, "", fmt.Errorf("API returned errors: %s", strings.Join(errMsgs, "; "))
	}

	var result cfCertResult
	if err := json.Unmarshal(apiResp.Result, &result); err != nil {
		return nil, "", fmt.Errorf("parse cert result: %w", err)
	}

	if result.Certificate == "" {
		return nil, "", fmt.Errorf("API returned empty certificate")
	}

	return []byte(result.Certificate), result.ID, nil
}
