package pki

// Persistent Agent CA: load from disk if present, otherwise generate
// and persist. The CA lives at <data_dir>/agent-ca.{pem,key}; that
// path is shared across team members so the same Agent CA signs
// every agent regardless of which node handles create-agent (the
// Phase-2 expectation that the CA bootstrap eventually runs on a
// leader-elected path is reflected in HULAAGENT_PLAN.md).

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CAFilenames are the on-disk names for the Agent CA bundle. Operators
// occasionally back these up out-of-band; keep them stable.
const (
	caCertFile = "agent-ca.pem"
	caKeyFile  = "agent-ca.key"
)

// LoadOrCreateCA loads the Agent CA from dataDir, generating it if
// absent. The generated CA's validity is 10y by default — long
// enough that operators don't trip over it during normal team life,
// short enough that a forgotten key eventually rotates out.
//
// Permissions on the generated key file are 0o600 to match what
// callers expect of TLS private keys.
func LoadOrCreateCA(dataDir string) (*CA, error) {
	if dataDir == "" {
		return nil, errors.New("dataDir is required")
	}
	certPath := filepath.Join(dataDir, caCertFile)
	keyPath := filepath.Join(dataDir, caKeyFile)

	cert, key, err := readCABundle(certPath, keyPath)
	if err == nil {
		return &CA{
			Cert:    cert,
			CertPEM: mustReadFile(certPath),
			Key:     key,
			KeyPEM:  mustReadFile(keyPath),
		}, nil
	}
	if !os.IsNotExist(err) {
		// Cert file exists but is unreadable / malformed — surface
		// it rather than silently regenerating. An operator who
		// already issued agent certs would lose the trust chain on
		// a silent regenerate.
		return nil, fmt.Errorf("read existing agent ca: %w", err)
	}

	// Cert absent — generate fresh. 10y is the default we go with;
	// callers wanting a different duration call NewAgentCA directly.
	ca, err := NewAgentCA(10 * 365 * 24 * time.Hour)
	if err != nil {
		return nil, fmt.Errorf("generate agent ca: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	if err := os.WriteFile(certPath, ca.CertPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, ca.KeyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}
	return ca, nil
}

// readCABundle parses an existing on-disk Agent CA bundle. Returns
// os.ErrNotExist if either file is missing — the caller distinguishes
// "fresh install" from "bundle is broken" via os.IsNotExist.
func readCABundle(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("parse cert pem: not a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, nil, fmt.Errorf("parse key pem: not an EC PRIVATE KEY block")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse key: %w", err)
	}
	return cert, key, nil
}

// mustReadFile is the small "I just verified it exists, now hand me
// the bytes" helper. Used for re-reading the PEM blobs we just parsed
// so the returned CA's PEM fields aren't empty. Errors here would
// indicate a TOCTOU race with another process deleting the file
// between parse and re-read; in that case we'd rather surface the
// original parse-success path with empty PEM than crash.
func mustReadFile(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}
