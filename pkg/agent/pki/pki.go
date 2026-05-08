// Package pki is the agent-side PKI primitives — Agent CA + leaf cert
// generation. Mirrors pkg/team/pki for the team domain but lives in
// its own trust root: agent certs are NEVER team certs and must never
// authenticate against the team-internal listener gate.
//
// Phase 1 (this commit): offline ceremony only. `hulactl create-agent`
// generates a one-off CA + leaf each invocation. Phase 2 introduces a
// persistent Agent CA on the hula server and the create-agent flow
// hits an admin-authenticated API instead of generating locally.
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
	"time"
)

// DefaultValidity is the cert lifetime used when the operator doesn't
// pass --expires-in. One year matches the design doc default.
const DefaultValidity = 365 * 24 * time.Hour

// AgentSubjectPrefix prefixes the subject CN of every agent cert with
// "agent:" — hula uses the prefix to distinguish agent certs from
// team or human certs at handshake time. The remainder of the CN is
// the agent's unique ID.
const AgentSubjectPrefix = "agent:"

// CA holds an Agent CA bundle in memory. Phase 2 will persist
// (cert, key) under hula's data_dir; Phase 1 just generates and
// returns.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
	KeyPEM  []byte
}

// AgentCert is a leaf cert + matching private key for one agent.
// Both are PEM-encoded so the caller can stuff them straight into
// the agent yaml.
type AgentCert struct {
	AgentID string
	CertPEM []byte
	KeyPEM  []byte
}

// NewAgentCA generates a fresh Agent CA (ECDSA P-256). validity
// applies to the CA itself; per-agent leaf certs sign under it with
// possibly-shorter validities.
func NewAgentCA(validity time.Duration) (*CA, error) {
	if validity <= 0 {
		validity = DefaultValidity
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("agent ca key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "hula-agent-ca",
			Organization: []string{"hulation agents"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("agent ca create: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("agent ca reparse: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("agent ca key marshal: %w", err)
	}

	return &CA{
		Cert:    cert,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Key:     key,
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// GenerateAgentCert signs a leaf cert under ca for the given agentID.
// Subject CN becomes "agent:<agentID>" — hula's verification path
// extracts the ID from there.
func GenerateAgentCert(ca *CA, agentID string, validity time.Duration) (*AgentCert, error) {
	if ca == nil || ca.Key == nil || ca.Cert == nil {
		return nil, errors.New("agent ca is required")
	}
	if agentID == "" {
		return nil, errors.New("agentID is required")
	}
	if validity <= 0 {
		validity = DefaultValidity
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("agent leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   AgentSubjectPrefix + agentID,
			Organization: []string{"hulation agent"},
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(validity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("agent cert create: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("agent leaf key marshal: %w", err)
	}

	return &AgentCert{
		AgentID: agentID,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// AgentIDFromCert pulls the agent ID out of the cert's subject CN.
// Returns "" if the CN doesn't have the expected "agent:" prefix —
// callers treat that as "not an agent cert."
func AgentIDFromCert(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	cn := cert.Subject.CommonName
	if len(cn) <= len(AgentSubjectPrefix) {
		return ""
	}
	if cn[:len(AgentSubjectPrefix)] != AgentSubjectPrefix {
		return ""
	}
	return cn[len(AgentSubjectPrefix):]
}

// randomSerial returns a 128-bit random serial number, big enough to
// sidestep the birthday-collision concerns in cert reissue loops.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	return n, nil
}
