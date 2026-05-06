// Package pki generates and loads the Team CA + per-node mTLS
// material that Stage 3 of the HA work requires. See HA_PLAN3.md §3
// for the design and threat model. The CA private key is operator-
// secured and MUST NOT deploy to any node — the operator runs
// `hulactl genteamcerts` once, distributes the per-node bundles
// out-of-band, and keeps ca.key in their secrets vault.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	// SANInternalSuffix is appended to every per-node SAN. The unified
	// listener uses the suffix to spot "this is internal traffic" at
	// the SNI layer before requiring a Team-CA-signed client cert.
	SANInternalSuffix = ".team.internal"

	// DefaultValidity is the per-cert lifetime when the operator
	// doesn't pin a duration. One year matches the operational rotation
	// cadence documented in HA_PLAN3.md §15.6.
	DefaultValidity = 365 * 24 * time.Hour

	// BootstrapTokenBytes is the random byte length of the team
	// bootstrap token; base64-encoded in the bundle for operator paste.
	BootstrapTokenBytes = 32
)

// CA holds a generated Team CA. The cert is shipped to every node;
// the key stays with the operator.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
	KeyPEM  []byte
}

// NodeCert holds a per-node leaf cert + private key, signed by the
// Team CA. SAN includes the node id and team id so a wrong-node or
// wrong-team cert fails at TLS handshake.
type NodeCert struct {
	NodeID  string
	CertPEM []byte
	KeyPEM  []byte
}

// Bundle is what a running hula loads from disk at boot. Used by
// the unified listener's mTLS gate to verify peer certs and present
// our own.
type Bundle struct {
	CACertPEM []byte
	CAPool    *x509.CertPool
	Cert      []byte
	Key       []byte
}

// GenerateCA mints a fresh ECDSA P-256 self-signed CA scoped to a
// single Team. The team_id is encoded into the CN + SAN so two
// different Teams cannot have certs that look interchangeable to a
// future federation layer.
func GenerateCA(teamID string, validity time.Duration) (*CA, error) {
	if teamID == "" {
		return nil, errors.New("teamID is required")
	}
	if validity <= 0 {
		validity = DefaultValidity
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "hula-team-ca/" + teamID,
			Organization: []string{"hulation team"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		MaxPathLenZero:        false,
		DNSNames:              []string{teamID + SANInternalSuffix},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca create: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca reparse: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca key marshal: %w", err)
	}

	return &CA{
		Cert:    cert,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Key:     key,
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// GenerateNodeCert signs a per-node leaf cert. The SAN list carries
// both the node id (so the listener knows who's connecting) and the
// team id (so a stolen cert can't impersonate a node in a different
// Team). hostname is the public-ish name an operator put under
// team.node_hostname (used by the unified listener for SNI selection
// on internal traffic).
func GenerateNodeCert(ca *CA, teamID, nodeID, hostname string, validity time.Duration) (*NodeCert, error) {
	if ca == nil || ca.Key == nil || ca.Cert == nil {
		return nil, errors.New("ca is required")
	}
	if teamID == "" || nodeID == "" {
		return nil, errors.New("teamID and nodeID are required")
	}
	if validity <= 0 {
		validity = DefaultValidity
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("node key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	dns := []string{
		nodeID + SANInternalSuffix,
		teamID + "/" + nodeID + SANInternalSuffix,
	}
	if hostname != "" && hostname != nodeID+SANInternalSuffix {
		dns = append(dns, hostname)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   nodeID,
			Organization: []string{"hulation team " + teamID},
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(validity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:    dns,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("node create: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("node key marshal: %w", err)
	}

	return &NodeCert{
		NodeID:  nodeID,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// GenerateBootstrapToken returns BootstrapTokenBytes random bytes
// suitable for stuffing into the team's `_team/bootstrap_token` FSM
// key. The caller is responsible for base64-encoding for transport.
func GenerateBootstrapToken() ([]byte, error) {
	tok := make([]byte, BootstrapTokenBytes)
	if _, err := rand.Read(tok); err != nil {
		return nil, fmt.Errorf("bootstrap token: %w", err)
	}
	return tok, nil
}

// LoadBundle reads a node's mTLS material from disk paths configured
// under team.pki. Validates that node_cert is actually signed by
// ca_cert; returns a Bundle whose CAPool is ready for use as both
// RootCAs (verifying peers) and ClientCAs (the listener side).
func LoadBundle(caCertPath, nodeCertPath, nodeKeyPath string) (*Bundle, error) {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read ca cert %s: %w", caCertPath, err)
	}
	certPEM, err := os.ReadFile(nodeCertPath)
	if err != nil {
		return nil, fmt.Errorf("read node cert %s: %w", nodeCertPath, err)
	}
	keyPEM, err := os.ReadFile(nodeKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read node key %s: %w", nodeKeyPath, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("ca cert %s contains no valid CERTIFICATE blocks", caCertPath)
	}

	leaf, err := parseLeaf(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse node cert %s: %w", nodeCertPath, err)
	}
	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return nil, fmt.Errorf("node cert is not signed by ca cert: %w", err)
	}

	return &Bundle{
		CACertPEM: caPEM,
		CAPool:    pool,
		Cert:      certPEM,
		Key:       keyPEM,
	}, nil
}

// WriteBundle dumps a CA + a list of node certs into a directory
// structure that operators can `tar` and ship through their secrets
// pipeline. Layout:
//
//   <out>/ca.pem
//   <out>/ca.key                           (operator-secured)
//   <out>/bootstrap-token                  (32-byte random, no newline)
//   <out>/team-id                          (echoed for convenience)
//   <out>/<nodeID>/{cert.pem,key.pem,ca.pem}
//
// The function fails closed on any existing destination so an
// operator running it twice doesn't quietly trample a previous run.
func WriteBundle(outDir, teamID string, ca *CA, nodes []*NodeCert, bootstrapToken []byte) error {
	if outDir == "" {
		return errors.New("outDir is required")
	}
	if _, err := os.Stat(outDir); err == nil {
		entries, _ := os.ReadDir(outDir)
		if len(entries) > 0 {
			return fmt.Errorf("%s exists and is not empty — refusing to overwrite", outDir)
		}
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "ca.pem"), ca.CertPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "ca.key"), ca.KeyPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "bootstrap-token"), bootstrapToken, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "team-id"), []byte(teamID), 0o644); err != nil {
		return err
	}

	for _, n := range nodes {
		nodeDir := filepath.Join(outDir, n.NodeID)
		if err := os.MkdirAll(nodeDir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", nodeDir, err)
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "cert.pem"), n.CertPEM, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "key.pem"), n.KeyPEM, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "ca.pem"), ca.CertPEM, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func parseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	return x509.ParseCertificate(block.Bytes)
}

func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	return n, nil
}
